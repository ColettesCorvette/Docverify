#!/bin/bash
# Provisionne un Mac en unite docverify autonome pour la flotte phase 2.
#
# Topologie shared-nothing : chaque Mac est independant.
#   k3d (docverify, 2 replicas)  ->  DMR Metal local via host.k3d.internal:12434
# Aucun appel LLM ne traverse le LAN. Le LB de flotte ne fait que repartir
# le trafic HTTP entrant sur les :8680 des Macs. Voir deploy/fleet/README.md.
#
# A lancer depuis la racine du repo sur CHAQUE Mac, une fois Docker Desktop
# installe et lance.
#
# Prerequis MANUELS non scriptables :
#   - Docker Desktop installe, version 4.62+.
#   - Reglage GUI "Start Docker Desktop when you sign in" coche
#     dans Settings > General. C'est ce qui remplace le LaunchAgent : le
#     demarrage de DMR depend du demarrage de Docker Desktop a la session.
#   - Auto-login de session active dans Reglages Systeme. Le login item
#     Docker Desktop ne se lance qu'APRES ouverture de session : sans
#     auto-login, un Mac sans clavier reste sur l'ecran de connexion au reboot,
#     tout eteint. FileVault rend l'auto-login inoperant. Voir README.md.
#
# Variables d'env :
#   MODEL    modele DMR a precharger      defaut docker.io/ai/gemma3:4B-Q4_K_M
#   CLUSTER  nom du cluster k3d            defaut mini-cluster
#   IMAGE    tag image docverify           defaut docverify-docverify:latest

set -euo pipefail

MODEL="${MODEL:-ai/gemma3:4B-Q4_K_M}"
CLUSTER="${CLUSTER:-mini-cluster}"
IMAGE="${IMAGE:-docverify-docverify:latest}"

cd "$(dirname "$0")/../.."

step() { echo ""; echo "==> $1"; }

# Docker Desktop place docker/kubectl dans /usr/local/bin, absent du PATH d'un
# shell non interactif. On force aussi ~/.local/bin ou k3d s'installe sans sudo.
export PATH="$HOME/.local/bin:/opt/homebrew/bin:/usr/local/bin:$PATH"

step "0. Verifier et installer les outils"
docker info >/dev/null 2>&1 || { echo "Docker Desktop n'est pas joignable. Lance-le puis relance."; exit 1; }
echo "docker OK, context $(docker context show)"
command -v kubectl >/dev/null || { echo "kubectl absent. Active Kubernetes dans Docker Desktop ou installe kubectl."; exit 1; }
if ! command -v k3d >/dev/null; then
	echo "k3d absent, installation sans sudo dans ~/.local/bin"
	mkdir -p "$HOME/.local/bin"
	curl -fsSL https://raw.githubusercontent.com/k3d-io/k3d/main/install.sh \
		| USE_SUDO=false K3D_INSTALL_DIR="$HOME/.local/bin" bash
fi
k3d version | head -1

step "2. Activer DMR en TCP host-side"
# Selon la version de Docker Desktop le TCP est deja actif par defaut, et la
# commande enable peut echouer sur une cle de settings inconnue. On gate donc
# sur la joignabilite reelle de :12434 plutot que sur le retour de enable.
if curl -fsS -o /dev/null --max-time 5 http://localhost:12434/engines/status; then
	echo "DMR deja joignable sur :12434"
else
	docker desktop enable model-runner --tcp=12434 || echo "enable KO, verifier le reglage GUI Docker Desktop"
	curl -fsS -o /dev/null --max-time 15 http://localhost:12434/engines/status \
		|| { echo "DMR injoignable sur :12434, abandon"; exit 1; }
fi
docker model status 2>/dev/null | head -3 || true

step "3. Precharger le modele $MODEL"
docker model pull "$MODEL"

step "4. Creer le cluster k3d $CLUSTER si absent"
if k3d cluster list 2>/dev/null | awk 'NR>1{print $1}' | grep -qx "$CLUSTER"; then
	echo "Cluster $CLUSTER deja present."
else
	k3d cluster create "$CLUSTER" --api-port 6443 \
		-p "8680:8680@loadbalancer" -p "8001:8001@loadbalancer" \
		--registry-create k3d-registry:0.0.0.0:5000
fi

step "4b. Resolution durable de host.k3d.internal (survie au reboot)"
kubectl apply -f deploy/k8s/coredns-host-alias.yaml
kubectl rollout restart deploy/coredns -n kube-system
kubectl rollout status deploy/coredns -n kube-system --timeout=60s

step "5. Build + import de l'image docverify"
# Validation locale facultative : le Dockerfile multi-stage compile le binaire
# dans l'image, donc Go n'est pas requis sur un Mac worker.
if command -v go >/dev/null; then
	go build -o /tmp/docverify-build ./cmd/docverify
else
	echo "go absent, on saute la pre-validation, le build se fait dans Docker"
fi
docker build -f deploy/Dockerfile -t "$IMAGE" .
k3d image import "$IMAGE" -c "$CLUSTER"

step "6. Deployer docverify"
kubectl apply -f deploy/k8s/docverify-deployment.yaml
if ! kubectl rollout status deploy/docverify --timeout=120s; then
	kubectl rollout restart deploy/docverify
	kubectl rollout status deploy/docverify --timeout=120s
fi

step "7. Verification de bout en bout"
ip=$(ipconfig getifaddr en0 || echo 127.0.0.1)
if curl -fsS "http://localhost:8680/health" >/dev/null; then
	echo "Noeud pret. docverify joignable sur http://$ip:8680"
	echo "Ajoute cette IP au LB de flotte : deploy/fleet/Caddyfile"
else
	echo "/health KO, verifier : kubectl get pods"
	exit 1
fi
