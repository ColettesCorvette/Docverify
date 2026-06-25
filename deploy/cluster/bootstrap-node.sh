#!/usr/bin/env bash
#
# deploy/cluster/bootstrap-node.sh
# Onboarde un Mac neuf dans le cluster docverify. À lancer depuis le Mac de
# CONTRÔLE (celui qui a le repo + le CA registre), il pilote le nœud par SSH.
#
#   ./deploy/cluster/bootstrap-node.sh <ip-du-noeud> [user]
#
# Modèle registre : le nœud ne porte ni repo, ni source, ni toolchain Go. Il
# tire l'image docverify du registre interne rhea. bootstrap pousse la config
# durable dans ~/.config/docverify du nœud, installe k3d/kubectl, précharge le
# modèle DMR, puis lance provision-node-registry.sh (recette registre prouvée).
#
# Compte macOS commun `cluster` sur tout le cluster : c'est l'user qui fait
# tourner Docker Desktop (son socket vit dans son home), donc l'user SSH. Voir
# scripts/deploy-cluster.sh, même défaut.
#
# Ce script automatise tout ce qui est joignable par SSH. Les prérequis GUI
# d'un Mac neuf ne le sont pas et restent manuels : voir la checklist affichée.
#
set -euo pipefail

GREEN='\033[0;32m'; RED='\033[0;31m'; YELLOW='\033[0;33m'; BLUE='\033[0;34m'
BOLD='\033[1m'; NC='\033[0m'

IP="${1:-}"
SSH_USER="${2:-cluster}"
MODEL="${MODEL:-hf.co/unsloth/gemma-4-e4b-it-gguf:Q4_K_M}"
# Source de vérité du CA = celui que Docker truste déjà sur le contrôle.
CA_SRC="${CA_SRC:-$HOME/.docker/certs.d/alpha.srv.orbinea:5000/ca.crt}"

cd "$(dirname "$0")/../.."
REGISTRIES_SRC="deploy/cluster/rhea-registries.yaml"
DEPLOY_SRC="deploy/k8s/docverify-deployment.yaml"
PROVISION_SRC="deploy/cluster/provision-node-registry.sh"

if [ -z "$IP" ]; then
    echo -e "${RED}Usage : $0 <ip-du-noeud> [user]${NC}"
    exit 1
fi
TARGET="${SSH_USER}@${IP}"

echo -e "${BOLD}${BLUE}Prérequis MANUELS sur le Mac neuf (non scriptables par SSH) :${NC}"
cat <<EOF
  1. Compte macOS primaire nommé '${SSH_USER}', auto-login activé.
  2. Docker Desktop installé, "Start Docker Desktop when you sign in" coché.
  3. FileVault désactivé. Sinon l'auto-login est inopérant au reboot.
  4. Partage > Connexion à distance ON, pour la clé SSH de ce poste.
  5. IP ${IP} réservée en DHCP côté routeur.
EOF
echo ""
read -p "Ces 5 points sont faits sur le nœud ? (o/N) " -n 1 -r; echo ""
[[ $REPLY =~ ^[Oo]$ ]] || { echo -e "${YELLOW}Onboarding annulé.${NC}"; exit 0; }

echo -e "\n${BOLD}== 1. Joignabilité SSH de $TARGET ==${NC}"
if ! ssh -o ConnectTimeout=5 -o BatchMode=yes "$TARGET" true 2>/dev/null; then
    echo -e "${RED}SSH KO. Vérifie Connexion à distance + clé + compte '${SSH_USER}'.${NC}"
    exit 1
fi
echo "SSH OK."

echo -e "\n${BOLD}== 2. Vérif fichiers de config côté contrôle ==${NC}"
for f in "$CA_SRC" "$REGISTRIES_SRC" "$DEPLOY_SRC" "$PROVISION_SRC"; do
    [ -f "$f" ] || { echo -e "${RED}MANQUE : $f${NC}"; exit 1; }
    echo "  ok $f"
done

echo -e "\n${BOLD}== 3. Push config dans ~/.config/docverify du nœud ==${NC}"
ssh "$TARGET" 'mkdir -p ~/.config/docverify'
scp "$CA_SRC"          "$TARGET:.config/docverify/rhea-ca.crt"
scp "$REGISTRIES_SRC"  "$TARGET:.config/docverify/rhea-registries.yaml"
scp "$DEPLOY_SRC"      "$TARGET:.config/docverify/docverify-deployment.yaml"
scp "$PROVISION_SRC"   "$TARGET:.config/docverify/provision-node-registry.sh"

echo -e "\n${BOLD}== 4. Install outils + DMR + provisioning (SSH) ==${NC}"
# bash -s : on exécute le bloc distant ci-dessous. MODEL passé en positionnel
# pour garder le heredoc 'REMOTE' littéral (pas d'expansion côté contrôle).
ssh "$TARGET" "bash -s -- '$MODEL'" <<'REMOTE'
set -euo pipefail
MODEL="$1"
# Docker Desktop met docker/kubectl dans /usr/local/bin, k3d s'installe sans
# sudo dans ~/.local/bin ; un shell SSH non-login n'a aucun des deux dans PATH.
export PATH="$HOME/.local/bin:/usr/local/bin:/opt/homebrew/bin:$PATH"

echo "-- docker"
docker info >/dev/null 2>&1 || { echo "Docker Desktop injoignable. Lance-le et relance."; exit 1; }

echo "-- kubectl"
command -v kubectl >/dev/null || { echo "kubectl absent. Active Kubernetes dans Docker Desktop."; exit 1; }

echo "-- k3d"
if ! command -v k3d >/dev/null; then
    mkdir -p "$HOME/.local/bin"
    curl -fsSL https://raw.githubusercontent.com/k3d-io/k3d/main/install.sh \
        | USE_SUDO=false K3D_INSTALL_DIR="$HOME/.local/bin" bash
fi
k3d version | head -1

echo "-- DMR TCP :12434"
# Selon la version, le TCP host-side est déjà actif ; on gate sur la
# joignabilité réelle, pas sur le retour de enable.
if ! curl -fsS -o /dev/null --max-time 5 http://localhost:12434/engines/status; then
    docker desktop enable model-runner --tcp=12434 || echo "enable KO, vérifier le réglage GUI"
    curl -fsS -o /dev/null --max-time 15 http://localhost:12434/engines/status \
        || { echo "DMR injoignable sur :12434, abandon"; exit 1; }
fi

echo "-- pull modèle $MODEL"
docker model pull "$MODEL"

echo "-- provisioning registre (provision-node-registry.sh)"
chmod +x ~/.config/docverify/provision-node-registry.sh
bash ~/.config/docverify/provision-node-registry.sh
REMOTE

echo -e "\n${GREEN}${BOLD}Nœud $IP prêt.${NC}"
echo -e "${BOLD}Dernier pas, côté contrôle :${NC} ajoute la ligne au Caddyfile puis recharge :"
echo -e "  ${BLUE}deploy/cluster/Caddyfile${NC} →  to $IP:8680"
echo -e "  caddy reload --config deploy/cluster/Caddyfile"
