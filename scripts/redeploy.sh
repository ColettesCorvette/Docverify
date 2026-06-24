#!/bin/bash
# Redéploiement LOCAL de docverify dans le cluster k3d de dev.
# Agrège : go build (validation) -> docker build -> k3d image import -> override
# local du Deployment -> rollout.
# Usage : ./scripts/redeploy.sh
#
# Pourquoi un override local : le manifest versionné cible le registre interne
# (alpha.srv.orbinea:5000/docverify:latest, imagePullPolicy Always) — c'est la
# config de la FLOTTE. En dev on NE passe PAS par le registre : on build en
# local, on importe l'image dans k3d, et on patche le Deployment pour qu'il
# utilise l'image locale en Never. Ça garde la boucle de dev rapide et isolée,
# sans polluer le tag :latest que la flotte tire. Pour déployer sur la flotte,
# voir scripts/deploy-fleet.sh.
#
# Variables d'env :
#   IMAGE        — tag de l'image LOCALE (défaut : docverify-docverify:latest)
#   CLUSTER      — nom du cluster k3d (défaut : mini-cluster)
#   DEPLOY       — nom du Deployment (défaut : docverify)
#   SKIP_GOBUILD — si =1, saute la validation go build locale
#
# Prérequis : Docker Desktop + k3d up, kubectl pointant sur le cluster de dev.
# Le backend LLM est Docker Model Runner côté hôte, atteint par les pods via la
# passerelle hôte 192.168.65.254:12434 (TCP host-side activé dans Docker Desktop).

set -euo pipefail

# Charge la config centrale si un .env racine existe (cf. .env.example).
ENV_FILE="$(cd "$(dirname "$0")/.." && pwd)/.env"
if [ -f "$ENV_FILE" ]; then set -a; . "$ENV_FILE"; set +a; fi

IMAGE="${IMAGE:-docverify-docverify:latest}"
CLUSTER="${CLUSTER:-mini-cluster}"
DEPLOY="${DEPLOY:-docverify}"
MANIFEST="deploy/k8s/docverify-deployment.yaml"

GREEN='\033[0;32m'
BLUE='\033[0;34m'
RED='\033[0;31m'
NC='\033[0m'

step() { echo -e "${BLUE}==>${NC} $1"; }

cd "$(dirname "$0")/.."
start=$(date +%s)

if [ "${SKIP_GOBUILD:-0}" != "1" ]; then
	step "go build (validation locale)"
	go build -o /tmp/docverify-build ./cmd/docverify
fi

step "docker build -> $IMAGE"
docker build -f deploy/Dockerfile -t "$IMAGE" .

step "k3d image import -> $CLUSTER"
k3d image import "$IMAGE" -c "$CLUSTER"

step "apply manifest (crée le Deployment s'il manque)"
kubectl apply -f "$MANIFEST"

# Override local : image importée + Never, pour ne dépendre ni du registre ni du
# réseau. Un seul patch (image + pullPolicy) → le rollout suivant prend l'image
# locale au lieu de celle du registre.
step "override local (image $IMAGE, pull Never)"
kubectl patch "deploy/$DEPLOY" --type=strategic -p \
	"{\"spec\":{\"template\":{\"spec\":{\"containers\":[{\"name\":\"$DEPLOY\",\"image\":\"$IMAGE\",\"imagePullPolicy\":\"Never\"}]}}}}"

# Le tag local est stable (:latest) : forcer le rollout pour que les pods
# reprennent l'image fraîchement importée même si le spec n'a pas changé.
step "rollout restart deploy/$DEPLOY"
kubectl rollout restart "deploy/$DEPLOY"
kubectl rollout status "deploy/$DEPLOY" --timeout=120s

step "health check"
if curl -fsS http://localhost:8680/health >/dev/null 2>&1; then
	echo -e "${GREEN}/health OK${NC}"
else
	echo -e "${RED}/health KO — vérifier kubectl get pods${NC}"
fi

echo -e "${GREEN}Terminé en $(($(date +%s) - start))s${NC}"
