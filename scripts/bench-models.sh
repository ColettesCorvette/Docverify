#!/bin/bash
# Bench multi-modèles docverify.
#
# Pour chaque sample, lance une requête docverify en mesurant la latence, et
# capture la réponse. Sortie : JSON lignes (un objet par sample) sur stdout.
#
# Usage :
#   MODEL=huggingface.co/unsloth/gemma-4-e4b-it-gguf:Q5_K_M ./scripts/bench-models.sh > /tmp/bench-gemma4.jsonl
#   
#
# Comparer ensuite :
#   jq -s 'group_by(.sample) | map({sample: .[0].sample, latencies: [.[].elapsed_s]})' /tmp/bench-*.jsonl
#
# Pré-requis :
#   - docverify lancé en cluster (kubectl get pods)
#   - Docker Model Runner actif avec le modèle pullé (docker model ls)
#   - jq disponible localement

set -u

# Charge la config centrale si un .env racine existe (cf. .env.example).
ENV_FILE="$(cd "$(dirname "$0")/.." && pwd)/.env"
if [ -f "$ENV_FILE" ]; then set -a; . "$ENV_FILE"; set +a; fi

API="${API:-http://localhost:8680}"
ROUTE="/api/v1/supporting-documents"
SAMPLES="${SAMPLES:-$HOME/Desktop/LLM/ocr/samples}"
MODEL="${MODEL:-huggingface.co/unsloth/gemma-4-e4b-it-gguf:Q5_K_M}"

# Vérité de terrain par sample : champs attendus pour mesurer la justesse.
# Chaque ligne = sample|file|champs multipart à passer.
SPECS=(
    "edf|facture-EDF.jpg|name=DUPONT&postal_code=31520"
    "freepro|Facture_FreePro_F202603049358_20260301.pdf|name=ORBINEA&postal_code=99000"
    "sfr|sfr-facture-1.pdf|name=ORBINEA&postal_code=99000"
    "lucie|carte_identite_Lucie.jpg|name=DELAUNAY&surname=LUCIE"
    "cni8493|index_8493_1_1200.png|name=BEN AMAR"
    "cni8498|index_8498_1_1200.png|name=DELAUNAY&surname=CAMILLE"
    "justif-jpg|justifs.jpg|name=LEROY&surname=PIERRE&postal_code=17999"
    "justif-pdf|justif2.pdf|name=ORBINEA&postal_code=99000"
    "solange|carte_identite_Solange.png|name=DURAND&surname=SOLANGE"
    "impot|impot_exemple.pdf|name=DURAND&surname=SOLANGE&postal_code=99000"
)

run_one() {
    local sample="$1"
    local file="$2"
    local fields="$3"

    local form_args=(-F "doc=@$SAMPLES/$file" -F "model=$MODEL")
    if [[ -n "$fields" ]]; then
        local OLD_IFS="$IFS"
        IFS='&'
        for kv in $fields; do
            form_args+=(-F "$kv")
        done
        IFS="$OLD_IFS"
    fi

    local tmp
    tmp="$(mktemp)"
    local meta http_code elapsed
    meta=$(curl -s -o "$tmp" -w "%{http_code} %{time_total}" -X POST "$API$ROUTE" "${form_args[@]}" || echo "000 0")
    http_code="${meta%% *}"
    elapsed="${meta##* }"

    local body
    body="$(cat "$tmp")"
    rm -f "$tmp"

    jq -n \
        --arg sample "$sample" \
        --arg model "$MODEL" \
        --arg elapsed "$elapsed" \
        --arg http "$http_code" \
        --argjson resp "${body:-null}" \
        '{sample: $sample, model: $model, elapsed_s: ($elapsed | tonumber? // null), http: ($http | tonumber? // null), response: $resp}'
}

for spec in "${SPECS[@]}"; do
    IFS='|' read -r sample file fields <<< "$spec"
    run_one "$sample" "$file" "$fields"
done
