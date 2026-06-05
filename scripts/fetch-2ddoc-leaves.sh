#!/bin/bash
# Récupère les certificats feuilles 2D-DOC publiés par chaque autorité et les
# embarque dans internal/docverify/assets/leaves/. Les vrais cachets sont signés
# par ces feuilles (ECDSA P-256), pas par les certs d'AC de la TSL. Pré-fetch au
# build → vérification 100 % offline dans le binaire (go:embed).
#
# Les URLs d'annuaire (TSPInformationURI) sont extraites de la TSL embarquée, pas
# codées en dur : si la TSL change, ce script suit. À relancer pour rafraîchir.
#
# Usage : ./scripts/fetch-2ddoc-leaves.sh

set -euo pipefail
cd "$(dirname "$0")/.."

TSL=internal/docverify/assets/tsl_signed.xml
OUT=internal/docverify/assets/leaves
mkdir -p "$OUT"

# AC -> première URL http(s) d'annuaire, depuis la TSL.
mapping=$(python3 - "$TSL" <<'PY'
import sys, re, xml.etree.ElementTree as ET
def ln(e): return e.tag.split('}')[-1]
root = ET.parse(sys.argv[1]).getroot()
for tsp in root.iter():
    if ln(tsp) != 'TrustServiceProvider':
        continue
    ac = None
    for n in tsp.iter():
        if ln(n) in ('TSPTradeName', 'TSPName'):
            for nm in n.iter():
                if ln(nm) == 'Name' and (nm.text or '').strip():
                    m = re.search(r'FR\d{2}', nm.text)
                    if m:
                        ac = m.group(0)
    url = None
    for e in tsp.iter():
        if ln(e) == 'URI' and (e.text or '').strip().startswith('http'):
            t = e.text.strip()
            if t.endswith(('.der', '.cer', '.crt', '.pem')) or 'search' in t or 'cert' in t.lower():
                url = t
                break
    if ac and url:
        print(f"{ac}\t{url}")
PY
)

echo "$mapping" | while IFS=$'\t' read -r ac url; do
	[ -z "$ac" ] && continue
	printf "%s  %-55s " "$ac" "$url"
	if curl -fsSL --max-time 20 "$url" -o "$OUT/$ac.der" 2>/dev/null; then
		echo "OK ($(wc -c < "$OUT/$ac.der" | tr -d ' ')o)"
	else
		echo "ÉCHEC (ignoré)"
		rm -f "$OUT/$ac.der"
	fi
done

echo "Feuilles dans $OUT :"
ls -l "$OUT"
