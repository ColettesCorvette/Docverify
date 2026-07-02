#!/bin/bash
# Script de test de l'API docverify (V1).
# Usage : ./test-api.sh [health|check|all|edf|freepro|sfr|lucie|cni8493|cni8498|justif-jpg|justif-pdf|solange|impot|async]
#
# Config : API, MODEL, SAMPLES viennent du .env racine (cf. .env.example), avec
# repli sur les défauts ci-dessous. Override ponctuel : API=… ./test-api.sh …
#
# Chaque appel synchrone affiche l'en-tête X-Request-Id renvoyé par le serveur,
# puis le verdict JSON, pour qu'on visualise l'identifiant de chaque requête. Le
# cas async vérifie le contrat webhook (202 + job_id) ; la livraison du callback
# est couverte par go test → TestPostCallback.

set -e

# Charge la config centrale si un .env racine existe (cf. .env.example).
ENV_FILE="$(cd "$(dirname "$0")/.." && pwd)/.env"
if [ -f "$ENV_FILE" ]; then set -a; . "$ENV_FILE"; set +a; fi

API="${API:-http://localhost:8680}"
ROUTE="/api/v1/supporting-documents"
SAMPLES="${SAMPLES:-$HOME/Desktop/LLM/ocr/samples}"
MODEL="${MODEL:-huggingface.co/unsloth/gemma-4-e4b-it-gguf:Q5_K_M}"

GREEN='\033[0;32m'
BLUE='\033[0;34m'
RED='\033[0;31m'
NC='\033[0m'

separator() {
    echo ""
    echo -e "${BLUE}═══════════════════════════════════════════════════════════${NC}"
    echo -e "${BLUE} $1${NC}"
    echo -e "${BLUE}═══════════════════════════════════════════════════════════${NC}"
}

# post_doc envoie un document en POST synchrone, affiche l'en-tête X-Request-Id
# puis le verdict JSON. Tous les champs multipart sont passés en arguments.
post_doc() {
    local headers
    headers="$(mktemp)"
    local body
    body="$(curl -s -D "$headers" -X POST "$API$ROUTE" "$@")"
    grep -i '^X-Request-Id:' "$headers" | tr -d '\r'
    rm -f "$headers"
    echo "$body" | jq
}

test_health() {
    separator "GET /health"
    curl -s "$API/health" | jq
}

test_check_facture_edf() {
    separator "Facture EDF — DUPONT, 31520"
    post_doc -F "doc=@$SAMPLES/facture-EDF.jpg" -F "name=DUPONT" -F "postal_code=31520" -F "model=$MODEL"
}

test_check_facture_freepro() {
    separator "Facture FreePro — ORBINEA, 99000"
    post_doc -F "doc=@$SAMPLES/Facture_FreePro_F202603049358_20260301.pdf" -F "name=ORBINEA" -F "postal_code=99000" -F "model=$MODEL"
}

test_check_facture_sfr() {
    separator "Facture SFR — ORBINEA, 99000"
    post_doc -F "doc=@$SAMPLES/sfr-facture-1.pdf" -F "name=ORBINEA" -F "postal_code=99000" -F "model=$MODEL"
}

test_check_cni_lucie() {
    separator "CNI Lucie — DELAUNAY LUCIE"
    post_doc -F "doc=@$SAMPLES/carte_identite_Lucie.jpg" -F "name=DELAUNAY" -F "surname=LUCIE" -F "postal_code=99000" -F "model=$MODEL"
}

test_check_cni_8493() {
    separator "CNI index_8493 — BEN AMAR"
    post_doc -F "doc=@$SAMPLES/index_8493_1_1200.png" -F "name=BEN AMAR" -F "postal_code=99000" -F "model=$MODEL"
}

test_check_cni_8498() {
    separator "CNI index_8498 — DELAUNAY CAMILLE"
    post_doc -F "doc=@$SAMPLES/index_8498_1_1200.png" -F "name=DELAUNAY" -F "surname=CAMILLE" -F "postal_code=99000" -F "model=$MODEL"
}

test_check_justif_jpg() {
    separator "justifs.jpg — LEROY PIERRE, 17999"
    post_doc -F "doc=@$SAMPLES/justifs.jpg" -F "name=LEROY" -F "surname=PIERRE" -F "postal_code=17999" -F "model=$MODEL"
}

test_check_justif_pdf() {
    separator "justif2.pdf — ORBINEA, 99000"
    post_doc -F "doc=@$SAMPLES/justif2.pdf" -F "name=ORBINEA" -F "postal_code=99000" -F "model=$MODEL"
}

test_check_cni_solange() {
    separator "CNI Solange — DURAND SOLANGE"
    post_doc -F "doc=@$SAMPLES/carte_identite_Solange.png" -F "name=DURAND" -F "surname=SOLANGE" -F "postal_code=99000" -F "model=$MODEL"
}

test_check_impot() {
    separator "impot_exemple.pdf — DURAND SOLANGE, 99000"
    post_doc -F "doc=@$SAMPLES/impot_exemple.pdf" -F "name=DURAND" -F "surname=SOLANGE" -F "postal_code=99000" -F "model=$MODEL"
}

test_check_async() {
    separator "Webhook asynchrone — 202 + job_id"
    echo "→ Sync : verdict précédé de son en-tête X-Request-Id"
    post_doc -F "doc=@$SAMPLES/justifs.jpg" -F "name=LEROY" -F "surname=PIERRE" -F "postal_code=17999" -F "model=$MODEL"
    echo ""
    echo "→ Async : un callback_url bascule la route en 202 + job_id."
    echo "  La livraison du callback est couverte par 'go test ./...' (TestPostCallback)."
    local tmp code body job
    tmp="$(mktemp)"
    code="$(curl -s -o "$tmp" -w '%{http_code}' -X POST "$API$ROUTE" \
        -F "doc=@$SAMPLES/justifs.jpg" \
        -F "name=LEROY" -F "surname=PIERRE" -F "postal_code=17999" \
        -F "callback_url=http://regia.invalid/cb" -F "model=$MODEL" || echo 000)"
    body="$(cat "$tmp")"; rm -f "$tmp"
    echo "$body" | jq
    job="$(echo "$body" | jq -r '.job_id // empty')"
    if [ "$code" = "202" ] && [ -n "$job" ]; then
        echo -e "${GREEN}✓ async OK : HTTP 202, job_id=$job${NC}"
    else
        echo -e "${RED}✗ async KO : HTTP ${code:-?}, job_id='${job:-absent}'${NC}"
        return 1
    fi
}

# --- Dispatcher ---------------------------------------------------------

case "${1:-all}" in
    health)
        test_health
        ;;
    check)
        test_check_facture_edf
        test_check_facture_freepro
        test_check_facture_sfr
        test_check_cni_lucie
        test_check_cni_8493
        test_check_cni_8498
        test_check_justif_jpg
        test_check_justif_pdf
        test_check_cni_solange
        test_check_impot
        ;;
    all)
        test_health
        test_check_facture_edf
        test_check_facture_freepro
        test_check_facture_sfr
        test_check_cni_lucie
        test_check_cni_8493
        test_check_cni_8498
        test_check_justif_jpg
        test_check_justif_pdf
        test_check_cni_solange
        test_check_impot
        test_check_async
        ;;
    edf)        test_check_facture_edf ;;
    freepro)    test_check_facture_freepro ;;
    sfr)        test_check_facture_sfr ;;
    lucie)      test_check_cni_lucie ;;
    cni8493)    test_check_cni_8493 ;;
    cni8498)    test_check_cni_8498 ;;
    justif-jpg) test_check_justif_jpg ;;
    justif-pdf) test_check_justif_pdf ;;
    solange)    test_check_cni_solange ;;
    impot)      test_check_impot ;;
    async)      test_check_async ;;
    *)
        echo "Usage: $0 [health|check|all|edf|freepro|sfr|lucie|cni8493|cni8498|justif-jpg|justif-pdf|solange|impot|async]"
        exit 1
        ;;
esac

echo ""
echo -e "${GREEN}✓ Tests terminés${NC}"
