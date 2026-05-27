#!/bin/bash
# Compare un JSONL produit par bench-models.sh à samples/groundtruth.json.
#
# Pour chaque sample :
#   - charge la vérité de terrain depuis samples/groundtruth.json
#   - compare champ par champ (doc_type, name_found, postal_found, date_found, ok, unreadable, mrz_valid)
#   - ignore les champs où la vérité de terrain est null (à compléter manuellement)
#   - sort matches / total + liste des mismatches
#
# Usage :
#   ./scripts/score-bench.sh /tmp/bench-qwen32.jsonl
#
# Variables d'env :
#   GROUNDTRUTH — chemin du JSON vérité (défaut : samples/groundtruth.json relatif au script)

set -u

JSONL="${1:-}"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
GROUNDTRUTH="${GROUNDTRUTH:-$SCRIPT_DIR/../samples/groundtruth.json}"

if [[ -z "$JSONL" ]]; then
    echo "Usage: $0 <bench-jsonl>" >&2
    exit 1
fi
if [[ ! -f "$JSONL" ]]; then
    echo "Fichier $JSONL introuvable" >&2
    exit 1
fi
if [[ ! -f "$GROUNDTRUTH" ]]; then
    echo "Groundtruth $GROUNDTRUTH introuvable" >&2
    exit 1
fi

jq --slurpfile gt "$GROUNDTRUTH" '
  . as $row
  | ($gt[0].samples[$row.sample]) as $entry
  | if $entry == null then
      {sample: $row.sample, error: "absent du groundtruth"}
    else
      ($entry.expected // {}) as $exp
      | ($row.response // {}) as $resp
      | [
          {field: "doc_type",      want: $exp.doc_type,      got: $resp.doc_type},
          {field: "name_found",    want: $exp.name_found,    got: $resp.name_found},
          {field: "surname_found", want: $exp.surname_found, got: $resp.surname_found},
          {field: "postal_found",  want: $exp.postal_found,  got: $resp.postal_found},
          {field: "date_found",    want: $exp.date_found,    got: $resp.date_found},
          {field: "ok",            want: $exp.ok,            got: $resp.ok},
          {field: "unreadable",    want: $exp.unreadable,    got: ($resp.unreadable // false)},
          {field: "mrz_valid",     want: $exp.mrz_valid,     got: ($resp.mrz_valid // false)}
        ]
      | map(select(.want != null))
      | . as $checks
      | ($checks | length) as $total
      | ($checks | map(select(.want == .got)) | length) as $matches
      | {sample: $row.sample,
         elapsed_s: $row.elapsed_s,
         http: $row.http,
         matches: $matches,
         total: $total,
         mismatches: ($checks | map(select(.want != .got) | "\(.field): want=\(.want|tostring) got=\(.got|tostring)"))}
    end
' "$JSONL" \
| jq -s '
  ([.[].elapsed_s] | map(select(. != null)) | sort) as $lat
  | ($lat | length) as $nlat
  | {
      model: (try (input_filename | sub("\\.jsonl$"; "") | sub(".*/"; "")) catch null),
      samples: .,
      summary: {
        n: length,
        matches_total: ([.[].matches // 0] | add),
        checks_total: ([.[].total // 0] | add),
        justesse: (([.[].matches // 0] | add) as $m
                  | ([.[].total // 0] | add) as $t
                  | if $t > 0 then ($m / $t) else 0 end),
        passed_all: ([.[] | select(.total != null and .matches == .total)] | length),
        p50_s: (if $nlat > 0 then $lat[$nlat / 2 | floor] else null end),
        p95_s: (if $nlat > 0 then $lat[($nlat - 1) * 95 / 100 | floor] else null end),
        max_s: (if $nlat > 0 then $lat[$nlat - 1] else null end)
      }
    }
'
