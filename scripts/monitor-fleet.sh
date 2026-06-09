#!/usr/bin/env bash
#
# scripts/monitor-fleet.sh
# Surveille l'état de santé de la flotte docverify.
# Extrait automatiquement les nœuds configurés dans le Caddyfile du Load Balancer.
#

set -euo pipefail

# Couleurs
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[0;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color
BOLD='\033[1m'

# Chemin vers le Caddyfile
CADDYFILE="deploy/fleet/Caddyfile"

if [ ! -f "$CADDYFILE" ]; then
    echo -e "${RED}Erreur : Caddyfile introuvable à l'emplacement $CADDYFILE${NC}"
    exit 1
fi

echo -e "${BOLD}${BLUE} Extraction des nœuds actifs de la flotte depuis $CADDYFILE...${NC}"

# Extrait les lignes "to <adresse>:<port>" (non commentées)
nodes=$(grep -E '^\s*to\s+[a-zA-Z0-9.-]+:[0-9]+' "$CADDYFILE" | awk '{print $2}')

if [ -z "$nodes" ]; then
    echo -e "${YELLOW}Aucun nœud actif trouvé dans le Caddyfile (tous commentés ou absents).${NC}"
    exit 0
fi

echo -e "${BOLD}Nœuds détectés :${NC}"
for node in $nodes; do
    echo "  - $node"
done
echo ""

echo -e "╠═══════════════════════════════════════════════════════════════════╣"
echo -e "║                    RAPPORT DE SANTÉ DE LA FLOTTE                  ║"
echo -e "╠═══════════════════════════════════════════════════════════════════╣"

has_error=0

for node in $nodes; do
    # On isole l'hôte et le port
    host=$(echo "$node" | cut -d: -f1)
    port=$(echo "$node" | cut -d: -f2)
    
    url="http://${host}:${port}/health"
    
    # Appel HTTP avec timeout de 2.0s
    start_time=$(date +%s%N 2>/dev/null || date +%s)
    response=$(curl -s --max-time 2.5 -w "\n%{http_code}" "$url" 2>/dev/null || true)
    end_time=$(date +%s%N 2>/dev/null || date +%s)
    
    # Calcul de la latence en ms
    if [[ "$start_time" =~ ^[0-9]+$ ]] && [[ "$end_time" =~ ^[0-9]+$ ]]; then
        latency=$(( (end_time - start_time) / 1000000 ))
    else
        latency="?"
    fi

    # Séparation du body et du status code
    http_status=$(echo "$response" | tail -n1)
    body=$(echo "$response" | sed '$d')

    if [ "$http_status" = "200" ]; then
        # Check si c'est du JSON valide et s'il y a un statut dégradé
        if echo "$body" | grep -q "degraded"; then
            echo -e "║  ${YELLOW}%-25s${NC} | status: DEGRADED | ${latency}ms\n║      -> DMR local injoignable !"
            has_error=1
        else
            echo -e "║  ${GREEN}%-25s${NC} | status: OK       | ${latency}ms" | sed "s/%-25s/$(printf '%-25s' "$node")/"
        fi
    elif [ -z "$http_status" ] || [ "$http_status" = "000" ]; then
        echo -e "║  ${RED}%-25s${NC} | status: TIMEOUT  | >2500ms\n║      -> Service docverify injoignable (éteint ou problème réseau)" | sed "s/%-25s/$(printf '%-25s' "$node")/"
        has_error=1
    else
        echo -e "║  ${RED}%-25s${NC} | status: HTTP %-3s | ${latency}ms\n║      -> %s" | sed -e "s/%-25s/$(printf '%-25s' "$node")/" -e "s/%-3s/$http_status/" -e "s/%s/$body/"
        has_error=1
    fi
done

echo -e "╚═══════════════════════════════════════════════════════════════════╝"

if [ $has_error -eq 0 ]; then
    echo -e "\n${GREEN}${BOLD} Tous les nœuds de la flotte sont opérationnels et les DMR sont fonctionnels.${NC}"
    exit 0
else
    echo -e "\n${RED}${BOLD} Problèmes détectés sur un ou plusieurs nœuds de la flotte.${NC}"
    exit 1
fi
