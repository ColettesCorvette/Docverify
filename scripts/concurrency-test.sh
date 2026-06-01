#!/bin/bash
# Test de concurrence : envoie une rafale de requêtes SIMULTANÉES et détecte
# la contamination cross-batch (fuite du contenu d'un document sur un autre).
#
# Principe : chaque document a une date de naissance unique qui sert de
# signature. On poste tous les documents en parallèle, puis on vérifie pour
# chaque réponse qu'elle contient SA signature et AUCUNE signature étrangère.
#
# Usage : ./concurrency-test.sh [rounds]   (défaut 2 → 8 requêtes simultanées)
# Env   : API (défaut http://localhost:8680), MODEL

set -u
API="${API:-http://localhost:8680}"
ROUTE="/api/v1/supporting-documents"
SAMPLES="/Users/dev/Desktop/LLM/ocr/samples"
MODEL="${MODEL:-mlx-community/Qwen2.5-VL-7B-Instruct-4bit}"
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
    fire "cni8493" index_8493_1_1200.png   -F "name=BEN AMAR"                       > "$OUT/cni8493.$r.json" &
    fire "solange" carte_identite_Solange.png -F "name=DURAND" -F "surname=SOLANGE" > "$OUT/solange.$r.json" &
    fire "cni8498" index_8498_1_1200.png   -F "name=DELAUNAY" -F "surname=CAMILLE"> "$OUT/cni8498.$r.json" &
    fire "lucie"   carte_identite_Lucie.jpg -F "name=DELAUNAY" -F "surname=LUCIE"> "$OUT/lucie.$r.json" &
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
