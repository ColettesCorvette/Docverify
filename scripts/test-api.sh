#!/bin/bash
# Script de test de l'API docverify (V1).
# Usage : ./test-api.sh [health|check|all|edf|freepro|sfr|lucie|cni8493|cni8498|justif-jpg|justif-pdf|solange|impot]
#
# Variables d'env :
#   MODEL — override du modèle LLM (défaut : Qwen2.5-VL-7B-Instruct-4bit)
#   API   — override de l'URL docverify (défaut : http://localhost:8680)
#
# Cible la route principale POST /api/v1/supporting-documents.
# Les routes legacy /check et /batch sont commentées dans server.go ;
# pour les réactiver, décommenter les lignes mux.HandleFunc correspondantes.

set -e

API="${API:-http://localhost:8680}"
ROUTE="/api/v1/supporting-documents"
SAMPLES="/Users/dev/Desktop/LLM/ocr/samples"
MODEL="${MODEL:-mlx-community/Qwen2.5-VL-7B-Instruct-4bit}"

# Couleurs
GREEN='\033[0;32m'
BLUE='\033[0;34m'
NC='\033[0m'

separator() {
    echo ""
    echo -e "${BLUE}═══════════════════════════════════════════════════════════${NC}"
    echo -e "${BLUE} $1${NC}"
    echo -e "${BLUE}═══════════════════════════════════════════════════════════${NC}"
}

# --- /health -------------------------------------------------------------

test_health() {
    separator "GET /health"
    curl -s "$API/health" | jq
}

# --- POST /api/v1/supporting-documents sur chaque sample ----------------

test_check_facture_edf() {
    separator "POST $ROUTE — Facture EDF (DUPONT, 31520)"
    curl -s -X POST "$API$ROUTE" \
        -F "doc=@$SAMPLES/facture-EDF.jpg" \
        -F "name=DUPONT" \
        -F "postal_code=31520" \
        -F "model=$MODEL" | jq
}

test_check_facture_freepro() {
    separator "POST $ROUTE — Facture FreePro (ORBINEA, 99000)"
    curl -s -X POST "$API$ROUTE" \
        -F "doc=@$SAMPLES/Facture_FreePro_F202603049358_20260301.pdf" \
        -F "name=ORBINEA" \
        -F "postal_code=99000" \
        -F "model=$MODEL" | jq
}

test_check_facture_sfr() {
    separator "POST $ROUTE — Facture SFR (ORBINEA, 99000)"
    curl -s -X POST "$API$ROUTE" \
        -F "doc=@$SAMPLES/sfr-facture-1.pdf" \
        -F "name=ORBINEA" \
        -F "postal_code=99000" \
        -F "model=$MODEL" | jq
}

test_check_cni_lucie() {
    separator "POST $ROUTE — CNI Lucie (DELAUNAY)"
    curl -s -X POST "$API$ROUTE" \
        -F "doc=@$SAMPLES/carte_identite_Lucie.jpg" \
        -F "name=DELAUNAY" \
        -F "surname=LUCIE" \
        -F "model=$MODEL" | jq
}

test_check_cni_8493() {
    separator "POST $ROUTE — CNI index_8493 (BEN AMAR)"
    curl -s -X POST "$API$ROUTE" \
        -F "doc=@$SAMPLES/index_8493_1_1200.png" \
        -F "name=BEN AMAR" \
        -F "model=$MODEL" | jq
}

test_check_cni_8498() {
    separator "POST $ROUTE — CNI index_8498 (DELAUNAY CAMILLE)"
    curl -s -X POST "$API$ROUTE" \
        -F "doc=@$SAMPLES/index_8498_1_1200.png" \
        -F "name=DELAUNAY" \
        -F "surname=CAMILLE" \
        -F "model=$MODEL" | jq
}

test_check_justif_jpg() {
    separator "POST $ROUTE — justifs.jpg (LEROY PIERRE, 17999)"
    curl -s -X POST "$API$ROUTE" \
        -F "doc=@$SAMPLES/justifs.jpg" \
        -F "name=LEROY" \
        -F "surname=PIERRE" \
        -F "postal_code=17999" \
        -F "model=$MODEL" | jq
}

test_check_justif_pdf() {
    separator "POST $ROUTE — justif2.pdf"
    curl -s -X POST "$API$ROUTE" \
        -F "doc=@$SAMPLES/justif2.pdf" \
        -F "name=ORBINEA" \
        -F "postal_code=99000" \
        -F "model=$MODEL" | jq
}

test_check_cni_solange() {
    separator "POST $ROUTE — CNI Solange (DURAND SOLANGE)"
    curl -s -X POST "$API$ROUTE" \
        -F "doc=@$SAMPLES/carte_identite_Solange.png" \
        -F "name=DURAND" \
        -F "surname=SOLANGE" \
        -F "model=$MODEL" | jq
}

test_check_impot() {
    separator "POST $ROUTE — impot_exemple.pdf (DURAND SOLANGE, 99000)"
    curl -s -X POST "$API$ROUTE" \
        -F "doc=@$SAMPLES/impot_exemple.pdf" \
        -F "name=DURAND" \
        -F "surname=SOLANGE" \
        -F "postal_code=99000" \
        -F "model=$MODEL" | jq
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
    *)
        echo "Usage: $0 [health|check|all|edf|freepro|sfr|lucie|cni8493|cni8498|justif-jpg|justif-pdf|solange|impot]"
        exit 1
        ;;
esac

echo ""
echo -e "${GREEN}✓ Tests terminés${NC}"
