#!/usr/bin/env bash
# recapture-vm.sh — recapture l'image OCI de la VM vllm-mlx avec BUMP AUTO du tag.
#
# Orchestre la procédure validée (deploy/vm/README.md) de bout en bout depuis le
# HOST, en réutilisant setup-vm.sh / cleanup-vm.sh DANS la VM. Le tag est lu dans
# le manifeste puis incrémenté automatiquement (26.7 -> 26.8) : plus besoin
# d'éditer le YAML à la main, et on garde la traçabilité + le rollback par version.
#
# Flux : delete ds -> boot VM source -> scp+setup -> vérif -> cleanup -> shutdown
#        -> attente arrêt -> purge registry -> oras push <new-tag>
#        -> patch macos.yaml -> apply.
#
# Usage : ./scripts/recapture-vm.sh [--yes] [--push-only]
#   --yes       : enchaîne sans demander les confirmations.
#   --push-only : reprend directement à l'étape 6 (purge+push+deploy), pour une VM
#                 déjà arrêtée et nettoyée (reprise après échec sans re-booter).
#
# Env (défauts) : VM_IP(192.168.20.10) VM_USER(test) VM_DIR(~/docverify-vm)
#   ORAS(~/oras-macos-vz/bin/darwin/arm64/oras) REGISTRY(localhost:5000)
#   IMAGE_REPO(macos-vm) MANIFEST(deploy/k8s/macos.yaml)
#
# Prérequis : colima+k3d up ; ~/docverify-vm/{disk.img,aux.img,vm.json,macosvm} ;
#   binaire oras ; SSH activé dans la VM (user test). Le SSH est multiplexé :
#   le mot de passe n'est demandé QU'UNE fois.

set -euo pipefail

# Charge la config centrale si un .env racine existe (cf. .env.example).
ENV_FILE="$(cd "$(dirname "$0")/.." && pwd)/.env"
if [ -f "$ENV_FILE" ]; then set -a; . "$ENV_FILE"; set +a; fi

VM_IP="${VM_IP:-192.168.20.10}"
VM_USER="${VM_USER:-test}"
VM_DIR="${VM_DIR:-$HOME/docverify-vm}"
ORAS="${ORAS:-$HOME/oras-macos-vz/bin/darwin/arm64/oras}"
REGISTRY="${REGISTRY:-localhost:5000}"
IMAGE_REPO="${IMAGE_REPO:-macos-vm}"
MANIFEST="${MANIFEST:-deploy/k8s/macos.yaml}"
REG_CONTAINER="${REG_CONTAINER:-k3d-registry}"
ASSUME_YES=0; PUSH_ONLY=0
for a in "$@"; do case "$a" in --yes) ASSUME_YES=1 ;; --push-only) PUSH_ONLY=1 ;; esac; done

GREEN='\033[0;32m'; BLUE='\033[0;34m'; RED='\033[0;31m'; YEL='\033[0;33m'; NC='\033[0m'
step()  { echo -e "\n${BLUE}==>${NC} $1"; }
warn()  { echo -e "${YEL}!${NC} $1"; }
die()   { echo -e "${RED}✗ $1${NC}" >&2; exit 1; }
confirm() { [ "$ASSUME_YES" = 1 ] && return 0; read -r -p "$1 [y/N] " a; [ "$a" = y ] || [ "$a" = Y ]; }

# prune_registry conserve $1 (tag courant) + NEW_TAG et supprime les autres tags
# de l'image, puis garbage-collect pour libérer l'espace disque. Passe par le
# volume car l'API delete du registry k3d est désactivée. À ne lancer que sans
# push concurrent : ici la VM est arrêtée et le pod macos supprimé, donc aucun
# pull/push en vol sur le registry.
prune_registry() {
  local keep="$1" t tags store="/var/lib/registry/docker/registry/v2"
  step "Purge registry : conserve ${keep} + ${NEW_TAG}, supprime le reste"
  tags="$(curl -s "http://${REGISTRY}/v2/${IMAGE_REPO}/tags/list" | jq -r '.tags[]?' 2>/dev/null)"
  for t in $tags; do
    case "$t" in "$keep"|"$NEW_TAG") continue ;; esac
    echo "  - suppression du tag $t"
    docker exec "$REG_CONTAINER" rm -rf "${store}/repositories/${IMAGE_REPO}/_manifests/tags/${t}" || warn "rm tag $t échoué"
  done
  echo "  garbage-collect (delete-untagged)…"
  docker exec "$REG_CONTAINER" registry garbage-collect --delete-untagged /etc/docker/registry/config.yml >/dev/null || warn "garbage-collect échoué"
  docker exec "$REG_CONTAINER" du -sh /var/lib/registry 2>/dev/null | sed 's/^/  espace registry après purge : /' || true
}

cd "$(dirname "$0")/.."
[ -f "$MANIFEST" ] || die "manifeste introuvable : $MANIFEST"
[ -x "$ORAS" ]     || die "binaire oras introuvable : $ORAS"
[ -f "$VM_DIR/vm.json" ] || die "VM source introuvable : $VM_DIR/vm.json"
command -v nc >/dev/null || die "nc requis pour sonder l'état de la VM"

# --- Bump du tag ---------------------------------------------------------
CUR_TAG="$(grep -oE "${IMAGE_REPO}:[0-9]+\.[0-9]+" "$MANIFEST" | head -1 | cut -d: -f2)"
[ -n "$CUR_TAG" ] || die "tag courant introuvable dans $MANIFEST"
NEW_TAG="${CUR_TAG%.*}.$(( ${CUR_TAG##*.} + 1 ))"
IMG="${REGISTRY}/${IMAGE_REPO}:${NEW_TAG}"

echo -e "${BLUE}Recapture VM${NC} : ${IMAGE_REPO} ${CUR_TAG} -> ${GREEN}${NEW_TAG}${NC}"
echo "  Source : ${VM_USER}@${VM_IP} (${VM_DIR})   Image : ${IMG}"
confirm "Lancer la recapture ?" || die "annulé"

if [ "$PUSH_ONLY" = 1 ]; then
  warn "--push-only : étapes 1-5 sautées (VM supposée déjà arrêtée et nettoyée)."
  pgrep -x macosvm >/dev/null 2>&1 && die "macosvm tourne encore — la VM n'est pas arrêtée, push dangereux."
else

# --- SSH multiplexé (un seul mot de passe) -------------------------------
CP="$(mktemp -u /tmp/recapture-ssh.XXXX)"
SSH=(ssh -o "ControlPath=$CP" -o ControlMaster=auto -o ControlPersist=900 -o StrictHostKeyChecking=accept-new)
SCP=(scp -o "ControlPath=$CP")
close_master() { "${SSH[@]}" -O exit "${VM_USER}@${VM_IP}" 2>/dev/null || true; rm -f "$CP"; }
trap close_master EXIT
ssh_port() { nc -z -G 2 "$VM_IP" 22 >/dev/null 2>&1; }   # nc macOS : -G = timeout connexion

# --- 1. Libérer la VM source --------------------------------------------
step "1/7 — libérer la VM source"
if kubectl get daemonset vllm-mlx >/dev/null 2>&1; then
  confirm "Supprimer le DaemonSet vllm-mlx, ce qui coupe le LLM pendant la recapture ?" || die "annulé"
  kubectl delete daemonset vllm-mlx --wait=true
fi

# --- 2. Booter la VM source ---------------------------------------------
step "2/7 — boot de la VM source"
if ssh_port; then
  echo "Port SSH déjà ouvert sur $VM_IP — réutilisation."
else
  echo "Démarrage de macosvm en arrière-plan (log: /tmp/macosvm-boot.log)…"
  ( cd "$VM_DIR" && ./macosvm -g vm.json ) >/tmp/macosvm-boot.log 2>&1 &
  for i in $(seq 1 60); do ssh_port && break; sleep 2; [ "$i" = 60 ] && die "VM injoignable après 120s (voir /tmp/macosvm-boot.log)"; done
fi
"${SSH[@]}" "${VM_USER}@${VM_IP}" true   # établit le master : mot de passe demandé ICI, une seule fois

# --- 3. Provisionner -----------------------------------------------------
step "3/7 — provisioning (scp deploy/vm/* + setup-vm.sh)"
"${SCP[@]}" deploy/vm/* "${VM_USER}@${VM_IP}:~/"
"${SSH[@]}" -t "${VM_USER}@${VM_IP}" 'bash ~/setup-vm.sh'

# --- 4. Vérifier ---------------------------------------------------------
step "4/7 — vérification vllm-mlx"
"${SSH[@]}" "${VM_USER}@${VM_IP}" 'curl -s http://127.0.0.1:8000/v1/models' | jq . \
  || warn "vllm ne répond pas encore (le modèle peut être en cours de chargement)"

# --- 5. Cleanup + shutdown ----------------------------------------------
step "5/7 — cleanup + shutdown"
"${SSH[@]}" -t "${VM_USER}@${VM_IP}" 'bash ~/cleanup-vm.sh'
"${SSH[@]}" -t "${VM_USER}@${VM_IP}" 'sudo shutdown -h now' || true
close_master
echo "  attente de la fin de macosvm (le push lit disk.img : interdit tant qu'il tourne)…"
for i in $(seq 1 120); do pgrep -x macosvm >/dev/null 2>&1 || break; sleep 2; [ "$i" = 120 ] && die "macosvm encore actif après 240s — NE PAS pousser, vérifier à la main"; done
echo "VM arrêtée (macosvm terminé)."

fi   # fin des étapes 1-5 (sautées si --push-only)

# --- 6. Purge registry + push de l'image OCI ----------------------------
step "6/7 — purge registry + oras push (5-15 min)"
prune_registry "$CUR_TAG"
confirm "Pousser ${IMG} ?" || die "annulé avant push"
"$ORAS" push --plain-http \
  --disk-path "$VM_DIR/disk.img" --aux-path "$VM_DIR/aux.img" \
  --hardware-model "$(jq -r .hardwareModel "$VM_DIR/vm.json")" \
  --machine-id "$(jq -r .machineId "$VM_DIR/vm.json")" \
  "$IMG"
curl -s "http://${REGISTRY}/v2/${IMAGE_REPO}/tags/list" | grep -q "$NEW_TAG" || die "tag $NEW_TAG absent du registry après push"

# --- 7. Bump du manifeste + redeploy ------------------------------------
step "7/7 — patch ${MANIFEST} (${CUR_TAG} -> ${NEW_TAG}) + apply"
sed -i '' "s#${IMAGE_REPO}:${CUR_TAG}#${IMAGE_REPO}:${NEW_TAG}#g" "$MANIFEST"
kubectl apply -f "$MANIFEST"
kubectl rollout status daemonset/vllm-mlx --timeout=300s || warn "DaemonSet vllm-mlx pas prêt — kubectl describe ds vllm-mlx"

echo -e "\n${GREEN}Recapture OK : ${IMAGE_REPO}:${NEW_TAG} déployé.${NC}"
echo "À committer : git add ${MANIFEST}"
