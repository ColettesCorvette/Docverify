#!/usr/bin/env bash
# push-wiki.sh — synchronise les pages locales wiki/ vers le wiki Gitea/Gogs.
#
# Le wiki est un dépôt git SÉPARÉ (…/docverify.wiki.git). Ce script le clone (ou
# le met à jour), recopie les pages depuis wiki/ en respectant le nommage des
# pages déjà en ligne (les pages live ont des accents — « Déploiement… » — que
# les fichiers locaux n'ont pas), puis commit + push.
#
# Auth SSH interactive : ta passphrase sera demandée (l'agent doit avoir la clé,
# sinon `ssh-add ~/.ssh/id_ed25519` d'abord).
#
# Usage : ./scripts/push-wiki.sh ["message de commit"]
# Env   : WIKI_REMOTE, WIKI_DIR (défaut ~/docverify-wiki)

set -euo pipefail

WIKI_REMOTE="${WIKI_REMOTE:-ssh://git@git.int.orbinea.example:2222/tfuchs/docverify.wiki.git}"
WIKI_DIR="${WIKI_DIR:-$HOME/docverify-wiki}"
MSG="${1:-maj wiki depuis le repo docverify}"

GREEN='\033[0;32m'; BLUE='\033[0;34m'; YEL='\033[0;33m'; RED='\033[0;31m'; NC='\033[0m'
step() { echo -e "\n${BLUE}==>${NC} $1"; }
warn() { echo -e "${YEL}!${NC} $1"; }
die()  { echo -e "${RED}✗ $1${NC}" >&2; exit 1; }

cd "$(dirname "$0")/.."
SRC="$PWD/wiki"
[ -d "$SRC" ] || die "dossier source introuvable : $SRC"
command -v perl >/dev/null || die "perl requis (normalisation des noms accentués)"

# norm : nom sans accent ni casse, pour matcher « Deploiement » <-> « Déploiement »
norm() { printf '%s' "$1" | perl -CSD -MUnicode::Normalize -ne 'print lc(NFD($_) =~ s/\p{M}//gr)'; }

# --- 1. clone ou mise à jour du wiki ------------------------------------
if [ -d "$WIKI_DIR/.git" ]; then
  step "Mise à jour du clone wiki ($WIKI_DIR)"
  git -C "$WIKI_DIR" pull --ff-only
else
  step "Clone du wiki ($WIKI_REMOTE)"
  git clone "$WIKI_REMOTE" "$WIKI_DIR"
fi

# --- 2. copie des pages en respectant le nommage existant ---------------
step "Copie des pages locales -> wiki"
for f in "$SRC"/*.md; do
  base="$(basename "$f")"
  nb="$(norm "$base")"
  target="$base"
  for w in "$WIKI_DIR"/*.md; do
    [ -e "$w" ] || continue
    if [ "$(norm "$(basename "$w")")" = "$nb" ]; then target="$(basename "$w")"; break; fi
  done
  cp "$f" "$WIKI_DIR/$target"
  [ "$target" = "$base" ] && echo "  $base" || echo "  $base -> $target"
done

# --- 3. commit + push si changements ------------------------------------
cd "$WIKI_DIR"
if [ -z "$(git status --porcelain)" ]; then
  step "Aucun changement — rien à pousser."
  exit 0
fi
step "Changements à pousser :"
git status --short
git add -A
git commit -m "$MSG"
git push
echo -e "\n${GREEN}Wiki poussé.${NC}"
