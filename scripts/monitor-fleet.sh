#!/usr/bin/env bash
#
# scripts/monitor-fleet.sh
# Dashboard temps réel de la santé de la flotte docverify.
# Nœuds extraits du Caddyfile du Load Balancer. Sonde /health de chaque nœud
# (status, LLM, workers, queue, in-flight, version, uptime) et, si le LB tourne
# localement, la distribution des requêtes en cours par backend.
#
# Usage :
#   ./scripts/monitor-fleet.sh            # dashboard live, refresh 2s (Ctrl-C pour quitter)
#   ./scripts/monitor-fleet.sh 5          # live, refresh 5s
#   ./scripts/monitor-fleet.sh --once     # snapshot unique + code retour (0 ok / 1 souci) pour CI
#
# Prérequis : jq.

set -uo pipefail

GREEN='\033[0;32m'; RED='\033[0;31m'; YELLOW='\033[0;33m'
BLUE='\033[0;34m'; CYAN='\033[0;36m'; DIM='\033[2m'; BOLD='\033[1m'; NC='\033[0m'

CADDYFILE="${CADDYFILE:-deploy/fleet/Caddyfile}"
LB_CONTAINER="${LB_CONTAINER:-docverify-lb}"

INTERVAL=0.5
ONCE=0
EOL=$'\033[K'  # efface du curseur à la fin de ligne (refresh en place, sans flicker)
case "${1:-}" in
	--once) ONCE=1 ;;
	[0-9]*) INTERVAL="$1" ;;
	"") ;;
	*) echo "Usage: $0 [intervalle_s | --once]"; exit 2 ;;
esac

command -v jq >/dev/null || { echo -e "${RED}jq requis (brew install jq)${NC}"; exit 1; }

if [ ! -f "$CADDYFILE" ]; then
	echo -e "${RED}Caddyfile introuvable : $CADDYFILE${NC}"; exit 1
fi

nodes=$(grep -E '^\s*to\s+[a-zA-Z0-9.-]+:[0-9]+' "$CADDYFILE" | awk '{print $2}')
if [ -z "$nodes" ]; then
	echo -e "${YELLOW}Aucun nœud actif dans le Caddyfile.${NC}"; exit 0
fi

# Sonde un nœud. Renvoie sur stdout une ligne formatée. Échos les compteurs
# d'agrégat via les variables globales agg_*.
sample_node() {
	local node="$1" host port out meta body code ttime lat
	host=${node%%:*}; port=${node##*:}

	out=$(curl -s -m 1 -w $'\n%{http_code} %{time_total}' "http://${host}:${port}/health" 2>/dev/null)
	meta=$(printf '%s' "$out" | tail -1)
	body=$(printf '%s' "$out" | sed '$d')
	code=${meta%% *}
	ttime=${meta##* }
	lat=$(awk -v t="${ttime:-0}" 'BEGIN{printf "%d", t*1000}')

	if [ -z "$code" ] || [ "$code" = "000" ]; then
		agg_down=$((agg_down + 1))
		printf "${RED}%-18s${NC}  %-9s  %-13s  %7s  %-9s  %-10s  %-8s %8s${EOL}\n" \
			"$node" "DOWN" "-" ">1000ms" "-" "-" "-" "-"
		return
	fi

	# Parse JSON enrichi (tolérant si un champ manque)
	local status llm ver wk q inf cap up
	status=$(printf '%s' "$body" | jq -r '.status // "?"' 2>/dev/null)
	llm=$(printf '%s'    "$body" | jq -r '.llm // "?"' 2>/dev/null)
	ver=$(printf '%s'    "$body" | jq -r '.version // "?"' 2>/dev/null)
	wk=$(printf '%s'     "$body" | jq -r '.workers // 0' 2>/dev/null)
	q=$(printf '%s'      "$body" | jq -r '.queued // 0' 2>/dev/null)
	inf=$(printf '%s'    "$body" | jq -r '.inflight // 0' 2>/dev/null)
	cap=$(printf '%s'    "$body" | jq -r '.capacity // 0' 2>/dev/null)
	up=$(printf '%s'     "$body" | jq -r '.uptime_s // 0' 2>/dev/null)

	local scol llmcol uptime_h
	if [ "$status" = "ok" ]; then
		scol="$GREEN"; agg_up=$((agg_up + 1))
	else
		scol="$YELLOW"; agg_degraded=$((agg_degraded + 1))
	fi
	# Couleur LLM en dehors du %s : la valeur reste brute pour que le padding
	# de colonne soit calculé sur le texte visible, pas sur les codes ANSI.
	if [ "$llm" = "ok" ]; then llmcol="$GREEN"; else llmcol="$RED"; fi

	agg_inflight=$((agg_inflight + inf))
	agg_queued=$((agg_queued + q))
	agg_workers=$((agg_workers + wk))

	# uptime lisible
	if [ "$up" -ge 3600 ]; then uptime_h="$((up/3600))h$(((up%3600)/60))m"
	elif [ "$up" -ge 60 ]; then uptime_h="$((up/60))m$((up%60))s"
	else uptime_h="${up}s"; fi

	printf "${scol}%-18s${NC}  ${scol}%-9s${NC}  ${llmcol}%-13s${NC}  %5sms  ${BOLD}%4s${NC}/%-4s  %4s/%-5s  %-8s %8s${EOL}\n" \
		"$node" "$status" "$llm" "$lat" "$inf" "$wk" "$q" "$cap" "$ver" "$uptime_h"
}

# Distribution des requêtes en cours par backend, vue par le LB (best effort).
lb_distribution() {
	command -v docker >/dev/null || return 0
	docker ps --format '{{.Names}}' 2>/dev/null | grep -q "^${LB_CONTAINER}$" || return 0
	local ups
	ups=$(docker exec "$LB_CONTAINER" wget -qO- http://127.0.0.1:2019/reverse_proxy/upstreams 2>/dev/null) || return 0
	[ -z "$ups" ] && return 0
	echo -e "${DIM}─────────────────────────────────────────────────────────────────────────────${NC}${EOL}"
	echo -e "${CYAN}LB ${LB_CONTAINER}${NC} — requêtes en cours par backend :${EOL}"
	printf '%s' "$ups" | jq -r '.[] | "   \(.address)  in-flight=\(.num_requests)  fails=\(.fails)"' 2>/dev/null | sed "s/\$/${EOL}/"
}

render() {
	agg_up=0; agg_degraded=0; agg_down=0; agg_inflight=0; agg_queued=0; agg_workers=0
	local total; total=$(echo "$nodes" | wc -w | tr -d ' ')

	printf '\033[H'  # home (pas de clear total → refresh en place, sans flicker)
	echo -e "${BOLD}${BLUE}  DASHBOARD FLOTTE docverify${NC}   ${DIM}$(date '+%Y-%m-%d %H:%M:%S')   refresh ${INTERVAL}s   Ctrl-C pour quitter${NC}${EOL}"
	echo -e "${DIM}─────────────────────────────────────────────────────────────────────────────${NC}${EOL}"
	printf "${BOLD}%-18s  %-9s  %-13s  %7s  %-9s  %-10s  %-8s %8s${NC}${EOL}\n" \
		"NŒUD" "STATUS" "LLM" "LATENCE" "INFLT/WK" "QUEUE/CAP" "VERSION" "UPTIME"
	echo -e "${DIM}─────────────────────────────────────────────────────────────────────────────${NC}${EOL}"

	local node
	for node in $nodes; do sample_node "$node"; done

	echo -e "${DIM}─────────────────────────────────────────────────────────────────────────────${NC}${EOL}"
	printf "  ${GREEN}%d ok${NC}  ${YELLOW}%d degraded${NC}  ${RED}%d down${NC}   /  %d nœuds   |   in-flight total: ${BOLD}%d${NC}   queue: %d   capacité workers: %d${EOL}\n" \
		"$agg_up" "$agg_degraded" "$agg_down" "$total" "$agg_inflight" "$agg_queued" "$agg_workers"

	lb_distribution
	printf '\033[0J'  # efface tout résidu sous le dashboard (lignes LB en moins, etc.)
}

printf '\033[2J'  # clear unique au démarrage ; ensuite on rafraîchit en place

if [ "$ONCE" = "1" ]; then
	render
	[ "$agg_down" -eq 0 ] && [ "$agg_degraded" -eq 0 ] && exit 0 || exit 1
fi

trap 'printf "\033[?25h\n"; exit 0' INT TERM   # restaure le curseur à la sortie
printf '\033[?25l'  # cache le curseur pendant le live
while true; do
	render
	sleep "$INTERVAL"
done
