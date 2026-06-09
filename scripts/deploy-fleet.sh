#!/usr/bin/env bash
#
# scripts/deploy-fleet.sh
# Déploie la nouvelle version de docverify sur l'ensemble de la flotte via SSH.
# Extrait automatiquement les nœuds actifs depuis le Caddyfile.
#

set -euo pipefail

# Couleurs
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[0;33m'
BLUE='\033[0;34m'
NC='\033[0m'
BOLD='\033[1m'

CADDYFILE="deploy/fleet/Caddyfile"
PROJECT_DIR="${PROJECT_DIR:-~/Desktop/LLM/ocr/docverify}"
SSH_USER="${SSH_USER:-$USER}"

if [ ! -f "$CADDYFILE" ]; then
    echo -e "${RED}Erreur : Caddyfile introuvable à l'emplacement $CADDYFILE${NC}"
    exit 1
fi

echo -e "${BOLD}${BLUE} Préparation du déploiement de la flotte depuis $CADDYFILE...${NC}"

# Extrait les hôtes actifs (on ne garde que l'IP/hostname sans le port :8680)
hosts=$(grep -E '^\s*to\s+[a-zA-Z0-9.-]+:[0-9]+' "$CADDYFILE" | awk '{print $2}' | cut -d: -f1)

if [ -z "$hosts" ]; then
    echo -e "${YELLOW}Aucun hôte actif trouvé dans le Caddyfile pour le déploiement.${NC}"
    exit 0
fi

echo -e "${BOLD}Hôtes cibles détectés :${NC}"
for host in $hosts; do
    echo "  - $host (SSH user: $SSH_USER)"
done
echo ""

read -p "Voulez-vous lancer le déploiement sur ces hôtes ? (o/N) " -n 1 -r
echo ""
if [[ ! $REPLY =~ ^[Oo]$ ]]; then
    echo -e "${YELLOW}Déploiement annulé.${NC}"
    exit 0
fi

has_error=0

for host in $hosts; do
    echo -e "\n${BOLD}${BLUE}─────────────────────────────────────────────────────────────────${NC}"
    echo -e "${BOLD}Déploiement sur : $host...${NC}"
    echo -e "${BOLD}${BLUE}─────────────────────────────────────────────────────────────────${NC}"
    
    # Commande à exécuter sur le nœud distant
    # 1. Aller dans le projet
    # 2. Mettre à jour avec git pull
    # 3. Exécuter redeploy.sh en sautant la validation go build si Go n'est pas présent
    remote_cmd="cd $PROJECT_DIR && \
                echo '=== git pull ===' && git pull && \
                echo '=== redeploy ===' && SKIP_GOBUILD=1 ./scripts/redeploy.sh"
    
    # Connexion SSH non interactive (sauf mot de passe s'il n'y a pas de clés SSH configurées)
    if ssh -o ConnectTimeout=5 -o BatchMode=no "${SSH_USER}@${host}" "${remote_cmd}"; then
        echo -e "\n${GREEN} Déploiement réussi sur $host !${NC}"
    else
        echo -e "\n${RED} ÉCHEC du déploiement sur $host !${NC}"
        has_error=1
    fi
done

echo -e "\n${BOLD}${BLUE}─────────────────────────────────────────────────────────────────${NC}"
if [ $has_error -eq 0 ]; then
    echo -e "${GREEN}${BOLD} Déploiement de la flotte terminé avec SUCCÈS sur tous les hôtes !${NC}"
    exit 0
else
    echo -e "${RED}${BOLD} Le déploiement s'est terminé avec des erreurs sur certains hôtes.${NC}"
    exit 1
fi
