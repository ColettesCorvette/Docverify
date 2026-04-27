#!/bin/bash
# Script de test de l'API docverify
# Usage : ./test-api.sh [health|check|batch|all]
#
# Pré-requis : serveur docverify lancé (go run .) sur :8680, Ollama sur :11434

set -e

API="http://localhost:8680"
SAMPLES="/Users/dev/Desktop/LLM/ocr/samples"
MODEL="mlx-community/Qwen2.5-VL-7B-Instruct-4bit"
OLLAMA="http://localhost:11434"

# Couleurs
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
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

# --- /check sur chaque sample -------------------------------------------

test_check_facture_edf() {
    separator "POST /check — Facture EDF (DUPONT, 31520)"
    curl -s -X POST "$API/check" \
        -F "doc=@$SAMPLES/facture-EDF.jpg" \
        -F "name=DUPONT" \
        -F "postal_code=31520" \
        -F "model=$MODEL" | jq
}

test_check_facture_freepro() {
    separator "POST /check — Facture FreePro (ORBINEA, 99000)"
    curl -s -X POST "$API/check" \
        -F "doc=@$SAMPLES/Facture_FreePro_F202603049358_20260301.pdf" \
        -F "name=ORBINEA" \
        -F "postal_code=99000" \
        -F "model=$MODEL" | jq
}

test_check_facture_sfr() {
    separator "POST /check — Facture SFR (ORBINEA, 99000)"
    curl -s -X POST "$API/check" \
        -F "doc=@$SAMPLES/sfr-facture-1.pdf" \
        -F "name=ORBINEA" \
        -F "postal_code=99000" \
        -F "model=$MODEL" | jq
}

test_check_cni_lucie() {
    separator "POST /check — CNI Lucie (DELAUNAY)"
    curl -s -X POST "$API/check" \
        -F "doc=@$SAMPLES/carte_identite_Lucie.jpg" \
        -F "name=DELAUNAY" \
        -F "surname=LUCIE" \
        -F "model=$MODEL" | jq
}

test_check_cni_8493() {
    separator "POST /check — CNI index_8493 (BEN AMAR)"
    curl -s -X POST "$API/check" \
        -F "doc=@$SAMPLES/index_8493_1_1200.png" \
        -F "name=BEN AMAR" \
        -F "model=$MODEL" | jq
}

test_check_cni_8498() {
    separator "POST /check — CNI index_8498 (DELAUNAY CAMILLE)"
    curl -s -X POST "$API/check" \
        -F "doc=@$SAMPLES/index_8498_1_1200.png" \
        -F "name=DELAUNAY" \
        -F "surname=CAMILLE" \
        -F "model=$MODEL" | jq
}

test_check_justif_jpg() {
    separator "POST /check — justifs.jpg (LEROY PIERRE, 17999)"
    curl -s -X POST "$API/check" \
        -F "doc=@$SAMPLES/justifs.jpg" \
        -F "name=LEROY" \
        -F "surname=PIERRE" \
        -F "postal_code=17999" \
        -F "model=$MODEL" | jq
}

test_check_justif_pdf() {
    separator "POST /check — justif2.pdf"
    curl -s -X POST "$API/check" \
        -F "doc=@$SAMPLES/justif2.pdf" \
        -F "name=ORBINEA" \
        -F "postal_code=99000" \
        -F "model=$MODEL" | jq
}

# --- /batch (inspiré de batch1.json) ------------------------------------

test_batch_famille_orbinea() {
    separator "POST /batch — Famille ORBINEA (3 documents)"
    CONFIG=$(cat <<EOF
{
  "individuals": [
    {
      "id": "1",
      "name": "ORBINEA",
      "surname": "CONCEPT",
      "postal_codes": ["99000", "99100"],
      "cities": ["VILLEFRANCHE"],
      "referent": true
    },
    {
      "id": "2",
      "name": "DELAUNAY",
      "surname": "CAMILLE",
      "birth_date": "19/11/2018"
    },
    {
      "id": "3",
      "name": "DELAUNAY",
      "surname": "LUCIE",
      "birth_date": "07/06/2013"
    }
  ],
  "documents": [
    {
      "id": "1",
      "path": "",
      "individual": "1",
      "checks": ["name", "surname", "postal", "date", "birth_date"],
      "label": "Facture FreePro",
      "valid_months": 3
    },
    {
      "id": "2",
      "path": "",
      "individual": "2",
      "checks": ["name", "surname", "postal", "date", "birth_date"],
      "label": "CNI Camille"
    },
    {
      "id": "3",
      "path": "",
      "individual": "3",
      "checks": ["name", "surname", "postal", "date", "birth_date"],
      "label": "CNI Lucie"
    }
  ],
  "model": "$MODEL"
}
EOF
    )
    curl -s -X POST "$API/batch" \
        -F "config=$CONFIG" \
        -F "doc_1=@$SAMPLES/Facture_FreePro_F202603049358_20260301.pdf" \
        -F "doc_2=@$SAMPLES/index_8498_1_1200.png" \
        -F "doc_3=@$SAMPLES/carte_identite_Lucie.jpg" | jq
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
        ;;
    batch)
        test_batch_famille_orbinea
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
        test_batch_famille_orbinea
        ;;
    edf)        test_check_facture_edf ;;
    freepro)    test_check_facture_freepro ;;
    sfr)        test_check_facture_sfr ;;
    lucie)      test_check_cni_lucie ;;
    cni8493)    test_check_cni_8493 ;;
    cni8498)    test_check_cni_8498 ;;
    justif-jpg) test_check_justif_jpg ;;
    justif-pdf) test_check_justif_pdf ;;
    *)
        echo "Usage: $0 [health|check|batch|all|edf|freepro|sfr|lucie|cni8493|cni8498|justif-jpg|justif-pdf]"
        exit 1
        ;;
esac

echo ""
echo -e "${GREEN}✓ Tests terminés${NC}"
