#!/usr/bin/env bash
# vllm-watchdog.sh — détecte un vllm-mlx en GPU hang Metal et le relance.
#
# Lancé périodiquement par le LaunchDaemon com.orbinea.vllm-watchdog
# (StartInterval), DANS la VM macOS, en root (pour pouvoir kickstart le service
# system com.orbinea.vllm-mlx).
#
# Pourquoi pas un simple /health : lors d'un GPU hang Metal, le serveur HTTP
# répond encore (FastAPI vivant) alors que l'inférence GPU est bloquée. Un
# GET /v1/models reviendrait 200 et masquerait le hang. On envoie donc une
# vraie inférence minimale (max_tokens=1) et on vérifie qu'elle répond dans le
# délai. C'est aussi ce qui rattrape le charabia/contamination que K8s ne voit
# pas (le pod VM n'accepte pas de probe — voir setup/supervision.md).
#
# Comptage d'échecs consécutifs persisté dans un fichier d'état : on ne tue le
# serveur qu'après THRESHOLD échecs d'affilée, pour laisser passer le chargement
# du modèle au boot (~30-60 s) sans kickstart prématuré.

set -uo pipefail

PORT=8000
SERVICE="system/com.orbinea.vllm-mlx"
STATE="/tmp/vllm-watchdog-fails"
THRESHOLD=3      # échecs consécutifs avant kickstart (3 × StartInterval)
INFER_TIMEOUT=30 # secondes accordées à une inférence minimale

log() { echo "$(date '+%Y-%m-%dT%H:%M:%S') vllm-watchdog: $*"; }

fails=$(cat "$STATE" 2>/dev/null || echo 0)
case "$fails" in ''|*[!0-9]*) fails=0 ;; esac

# Modèle servi (appel léger, pas d'inférence). S'il ne répond pas, le serveur
# HTTP est lui-même down → échec.
model=$(curl -s -m 5 "http://localhost:$PORT/v1/models" \
  | sed -n 's/.*"id":"\([^"]*\)".*/\1/p' | head -1)

ok=0
if [ -n "$model" ]; then
  # Inférence minimale : seule façon de détecter un GPU hang (HTTP répond mais
  # l'inférence bloque). Réponse attendue en quelques secondes si le GPU est OK.
  resp=$(curl -s -m "$INFER_TIMEOUT" "http://localhost:$PORT/v1/chat/completions" \
    -H 'Content-Type: application/json' \
    -d "{\"model\":\"$model\",\"max_tokens\":1,\"messages\":[{\"role\":\"user\",\"content\":\"ping\"}]}")
  echo "$resp" | grep -q '"choices"' && ok=1
fi

if [ "$ok" = 1 ]; then
  [ "$fails" != 0 ] && log "vllm-mlx répond de nouveau (après $fails échec(s))"
  echo 0 > "$STATE"
  exit 0
fi

fails=$((fails + 1))
echo "$fails" > "$STATE"
log "échec inférence $fails/$THRESHOLD (model='${model:-injoignable}')"

if [ "$fails" -ge "$THRESHOLD" ]; then
  log "seuil atteint → launchctl kickstart -k $SERVICE"
  launchctl kickstart -k "$SERVICE"
  echo 0 > "$STATE"
fi
