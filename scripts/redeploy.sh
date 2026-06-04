#!/bin/bash
# Redéploiement rapide de docverify dans le cluster k3d.
# Agrège : go build (validation) -> docker build -> k3d image import -> rollout.
# Usage : ./scripts/redeploy.sh
#
# Variables d'env :
#   IMAGE        — tag de l'image (défaut : docverify-docverify:latest)
#   CLUSTER      — nom du cluster k3d (défaut : mini-cluster)
#   DEPLOY       — nom du Deployment (défaut : docverify)
#   SKIP_GOBUILD — si =1, saute la validation go build locale
#
# Prérequis : Docker Desktop + k3d up (context desktop-linux), kubectl pointant
# sur le cluster. Le backend LLM est Docker Model Runner côté hôte, atteint par
# les pods via host.k3d.internal:12434 (TCP host-side activé dans Docker Desktop).

set -euo pipefail

# Charge la config centrale si un .env racine existe (cf. .env.example).
ENV_FILE="$(cd "$(dirname "$0")/.." && pwd)/.env"
if [ -f "$ENV_FILE" ]; then set -a; . "$ENV_FILE"; set +a; fi

IMAGE="${IMAGE:-docverify-docverify:latest}"
CLUSTER="${CLUSTER:-mini-cluster}"
DEPLOY="${DEPLOY:-docverify}"

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
