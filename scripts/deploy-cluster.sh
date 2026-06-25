#!/usr/bin/env bash
#
# scripts/deploy-cluster.sh
# Déploie docverify sur tout le cluster via le registre interne.
#
# Modèle : on build + push l'image UNE fois ici (Mac de contrôle) vers le
# registre interne. Chaque nœud a un k3d configuré pour pull depuis ce registre
# (imagePullPolicy: Always) ; déployer = déclencher un `kubectl rollout restart`
# par nœud, k3d re-pull l'image. Aucun transfert d'image par SSH, aucun git ni
# build côté nœud.
#
# Nœuds extraits du Caddyfile (source de vérité du cluster). Compte SSH commun
# `cluster` sur tous les Macs (overridable via SSH_USER ; vide => ~/.ssh/config).
#

set -euo pipefail

# Charge la config centrale si un .env racine existe (cf. .env.example).
ENV_FILE="$(cd "$(dirname "$0")/.." && pwd)/.env"
if [ -f "$ENV_FILE" ]; then set -a; . "$ENV_FILE"; set +a; fi

# Couleurs
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[0;33m'
BLUE='\033[0;34m'
NC='\033[0m'
BOLD='\033[1m'

CADDYFILE="deploy/cluster/Caddyfile"
REGISTRY="${REGISTRY:-alpha.srv.orbinea:5000}"
IMAGE_NAME="${IMAGE_NAME:-docverify}"
TAG="${TAG:-latest}"
# ponytail: tag :latest mutable, suffisant avec imagePullPolicy:Always + rollout
# restart. Passer à un tag par sha git (TAG=$(git rev-parse --short HEAD)) si on
# veut des rollbacks déterministes.
REGISTRY_IMAGE="$REGISTRY/$IMAGE_NAME:$TAG"
DEPLOY="${DEPLOY:-docverify}"
# Compte macOS standard du cluster (celui qui fait tourner Docker Desktop sur
# chaque Mac). Overridable : SSH_USER=xxx ./scripts/deploy-cluster.sh
# Transition : SSH_USER= (vide) → on SSH par host seul, ~/.ssh/config choisit le
# user par Mac (utile tant que les nœuds actuels ne sont pas standardisés `cluster`).
SSH_USER="${SSH_USER-cluster}"

cd "$(dirname "$0")/.."

if [ ! -f "$CADDYFILE" ]; then
    echo -e "${RED}Erreur : Caddyfile introuvable à l'emplacement $CADDYFILE${NC}"
    exit 1
fi

# Extrait les hôtes actifs (IP/hostname sans le port :8680). Ignore les lignes
# commentées (grep ancré sur `to` en début de ligne, après espaces).
hosts=$(grep -E '^\s*to\s+[a-zA-Z0-9.-]+:[0-9]+' "$CADDYFILE" | awk '{print $2}' | cut -d: -f1)

if [ -z "$hosts" ]; then
    echo -e "${YELLOW}Aucun hôte actif trouvé dans le Caddyfile pour le déploiement.${NC}"
    exit 0
fi

echo -e "${BOLD}${BLUE}Build + push de l'image (une fois) : $REGISTRY_IMAGE${NC}"
docker build -f deploy/Dockerfile -t "$REGISTRY_IMAGE" .
docker push "$REGISTRY_IMAGE"

echo -e "\n${BOLD}Hôtes cibles détectés :${NC}"
for host in $hosts; do
    echo "  - $host (SSH user: ${SSH_USER:-via ~/.ssh/config})"
done
echo ""

read -p "Lancer le rollout sur ces hôtes ? (o/N) " -n 1 -r
echo ""
if [[ ! $REPLY =~ ^[Oo]$ ]]; then
    echo -e "${YELLOW}Déploiement annulé.${NC}"
    exit 0
fi

has_error=0

for host in $hosts; do
    echo -e "\n${BOLD}${BLUE}─────────────────────────────────────────────────────────────────${NC}"
    echo -e "${BOLD}Rollout sur : $host...${NC}"
    echo -e "${BOLD}${BLUE}─────────────────────────────────────────────────────────────────${NC}"

    # k3d re-pull l'image du registre via le rollout. Le node ne fait NI git NI
    # build NI réception d'image. PATH explicite : un shell SSH non-login n'a ni
    # /usr/local/bin (kubectl Docker Desktop) ni ~/.local/bin (k3d), sans quoi
    # kubectl ressort en 'command not found'.
    rollout_cmd="set -e; \
        export PATH=\"\$HOME/.local/bin:/usr/local/bin:/opt/homebrew/bin:\$PATH\"; \
        echo '=== rollout ==='; kubectl rollout restart deploy/'$DEPLOY'; \
        kubectl rollout status deploy/'$DEPLOY' --timeout=120s"

    # Le principal est cette machine : kubectl en local, pas de SSH. Le Caddyfile
    # le désigne par host.docker.internal (LB conteneurisé), à traiter comme local.
    if [ "$host" = "127.0.0.1" ] || [ "$host" = "localhost" ] || [ "$host" = "host.docker.internal" ]; then
        run() { bash -c "$rollout_cmd"; }
    else
        target="${host}"
        [ -n "$SSH_USER" ] && target="${SSH_USER}@${host}"
        run() { ssh -o ConnectTimeout=5 -o BatchMode=no "$target" "$rollout_cmd"; }
    fi

    if run; then
        echo -e "\n${GREEN} Rollout réussi sur $host !${NC}"
    else
        echo -e "\n${RED} ÉCHEC du rollout sur $host !${NC}"
        has_error=1
    fi
done

echo -e "\n${BOLD}${BLUE}─────────────────────────────────────────────────────────────────${NC}"
if [ "$has_error" -eq 0 ]; then
    echo -e "${GREEN}Déploiement terminé sur tout le cluster.${NC}"
else
    echo -e "${YELLOW}Déploiement terminé avec des erreurs sur certains hôtes.${NC}"
fi
exit "$has_error"
