#!/bin/bash
# Test de concurrence et garde anti-régression de la contamination cross-batch.
#
# Chaque document a une date de naissance unique qui sert de signature. On poste
# tous les documents en parallèle, puis on vérifie que chaque réponse contient SA
# signature et AUCUNE signature étrangère. Une fuite signale une contamination
# d'une requête à l'autre côté backend LLM. C'est aussi la base du test de
# charge sous rafales courtes attendu côté tuteur.
#
# Usage : ./concurrency-test.sh [rounds]   (défaut 2 → 8 requêtes simultanées)
# Config : API, MODEL, SAMPLES viennent du .env racine (cf. .env.example).

set -u

# Charge la config centrale si un .env racine existe (cf. .env.example).
ENV_FILE="$(cd "$(dirname "$0")/.." && pwd)/.env"
if [ -f "$ENV_FILE" ]; then set -a; . "$ENV_FILE"; set +a; fi

API="${API:-http://localhost:8680}"
ROUTE="/api/v1/supporting-documents"
SAMPLES="${SAMPLES:-$(cd "$(dirname "$0")/.." && pwd)/samples}"
MODEL="${MODEL:-huggingface.co/unsloth/gemma-4-e4b-it-gguf:Q5_K_M}"
ROUNDS="${1:-2}"
OUT="$(mktemp -d)"

ALL_SIGS="14/05/2011 03/07/1962 19/11/2018 07/06/2013"

birth_of() {
    case "$1" in
        cni8493*) echo "14/05/2011" ;;
        solange*) echo "03/07/1962" ;;
        cni8498*) echo "19/11/2018" ;;
        lucie*)   echo "07/06/2013" ;;
    esac
}

fire() { # label fichier champs...
    local label="$1" file="$2"; shift 2
    curl -s -X POST "$API$ROUTE" -F "doc=@$SAMPLES/$file" "$@" -F "model=$MODEL"
}

echo "Rafale : $((ROUNDS * 4)) requêtes simultanées vers $API"
start=$(date +%s)
for r in $(seq 1 "$ROUNDS"); do
    fire "cni8493" index_8493_1_1200.png   -F "name=BEN AMAR" -F "postal_code=99000"                       > "$OUT/cni8493.$r.json" &
    fire "solange" carte_identite_Solange.png -F "name=DURAND" -F "surname=SOLANGE" -F "postal_code=99000" > "$OUT/solange.$r.json" &
    fire "cni8498" index_8498_1_1200.png   -F "name=DELAUNAY" -F "surname=CAMILLE" -F "postal_code=99000"> "$OUT/cni8498.$r.json" &
    fire "lucie"   carte_identite_Lucie.jpg -F "name=DELAUNAY" -F "surname=LUCIE" -F "postal_code=99000"> "$OUT/lucie.$r.json" &
done
wait
elapsed=$(( $(date +%s) - start ))

ok=0; leaks=0; other=0
echo "----------------------------------------------------------------"
for f in "$OUT"/*.json; do
    base="$(basename "$f" .json)"; label="${base%.*}"
    own="$(birth_of "$label")"
    body="$(tr -d '\n' < "$f")"
    foreign=""
    for sig in $ALL_SIGS; do
        [ "$sig" = "$own" ] && continue
        case "$body" in *"$sig"*) foreign="$foreign $sig" ;; esac
    done
    summary="$(echo "$body" | jq -c '{ok,doc_type,birth_date,errors}' 2>/dev/null || echo "$body")"
    if [ -n "$foreign" ]; then
        leaks=$((leaks + 1))
        echo "FUITE  $base (sig propre=$own, étrangère=$foreign)"
        echo "       $summary"
    elif case "$body" in *"$own"*) true ;; *) false ;; esac; then
        ok=$((ok + 1))
        echo "OK     $base  $summary"
    else
        other=$((other + 1))
        echo "AUTRE  $base (signature $own absente)"
        echo "       $summary"
    fi
done
echo "----------------------------------------------------------------"
echo "OK=$ok  FUITES=$leaks  AUTRES=$other  (rafale traitée en ${elapsed}s)"
rm -rf "$OUT"
[ "$leaks" -eq 0 ]
