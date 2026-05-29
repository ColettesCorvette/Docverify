#!/usr/bin/env bash
# setup-vm.sh — automatise l'install vllm-mlx + LaunchDaemon dans une VM macOS
#
# Prérequis manuels (avant ce script) :
#   - VM macOS bootée via `macosvm -g vm.json` (Setup Assistant n'est pas scriptable)
#   - User `test` créé (compte admin)
#   - SSH activé : Réglages > Général > Partage > Connexion à distance, "Autoriser tous les utilisateurs"
#   - `xcode-select --install` fait + popup acceptée
#
# Usage côté host (depuis le repo docverify) :
#   scp deploy/vm/setup-vm.sh deploy/vm/vllm-watchdog.sh \
#       deploy/vm/com.orbinea.vllm-mlx.plist \
#       deploy/vm/com.orbinea.vllm-watchdog.plist test@<vm-ip>:~/
#   ssh test@<vm-ip> 'bash ~/setup-vm.sh'

set -euo pipefail

if [[ "$(whoami)" != "test" ]]; then
  echo "Ce script doit tourner sous le user 'test' dans la VM. User actuel : $(whoami)" >&2
  exit 1
fi

PLIST_LOCAL="$HOME/com.orbinea.vllm-mlx.plist"
PLIST_DEST="/Library/LaunchDaemons/com.orbinea.vllm-mlx.plist"
WD_PLIST_LOCAL="$HOME/com.orbinea.vllm-watchdog.plist"
WD_PLIST_DEST="/Library/LaunchDaemons/com.orbinea.vllm-watchdog.plist"

step() { printf "\n=== %s ===\n" "$1"; }

# reload_daemon (re)charge un LaunchDaemon de façon idempotente. Sur un service
# déjà actif, bootout est asynchrone : on attend son déchargement effectif avant
# bootstrap, sinon launchctl renvoie "Bootstrap failed: 5: Input/output error".
reload_daemon() {
  local label="$1" dest="$2"
  if sudo launchctl print "system/$label" >/dev/null 2>&1; then
    sudo launchctl bootout "system/$label" 2>/dev/null || true
    for _ in 1 2 3 4 5 6 7 8 9 10; do
      sudo launchctl print "system/$label" >/dev/null 2>&1 || break
      sleep 1
    done
  fi
  sudo launchctl bootstrap system "$dest"
}

step "Homebrew"
if ! command -v brew >/dev/null 2>&1; then
  /bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"
  eval "$(/opt/homebrew/bin/brew shellenv)"
  echo 'eval "$(/opt/homebrew/bin/brew shellenv)"' >> ~/.zprofile
fi

step "Python + uv + git"
brew install python@3.13 uv git

step "vllm-mlx"
if [[ ! -d "$HOME/vllm-mlx" ]]; then
  git clone https://github.com/waybarrios/vllm-mlx.git "$HOME/vllm-mlx"
fi

if [[ ! -d "$HOME/vllm-mlx-env" ]]; then
  uv venv "$HOME/vllm-mlx-env" --python 3.13
fi

# shellcheck disable=SC1091
source "$HOME/vllm-mlx-env/bin/activate"
uv pip install -e "$HOME/vllm-mlx[vision]"
# transformers >=5.6 ignore silencieusement les kwargs du processor de mlx-vlm 0.5.0 -> vision en charabia
uv pip install --no-deps "transformers==5.5.0"

step "Modèle Qwen2.5-VL-7B-Instruct-4bit (pré-téléchargement)"
python - <<'PY'
from huggingface_hub import snapshot_download
snapshot_download("mlx-community/Qwen2.5-VL-7B-Instruct-4bit")
PY

step "LaunchDaemon vllm-mlx"
if [[ ! -f "$PLIST_LOCAL" ]]; then
  echo "Plist introuvable à $PLIST_LOCAL — scp-le depuis le host avant de relancer." >&2
  exit 1
fi

sudo cp "$PLIST_LOCAL" "$PLIST_DEST"
sudo chown root:wheel "$PLIST_DEST"
sudo chmod 644 "$PLIST_DEST"

mkdir -p "$HOME/Library/Logs/vllm-mlx"

reload_daemon com.orbinea.vllm-mlx "$PLIST_DEST"

step "Watchdog vllm-mlx (détection GPU hang)"
if [[ -f "$HOME/vllm-watchdog.sh" && -f "$WD_PLIST_LOCAL" ]]; then
  chmod +x "$HOME/vllm-watchdog.sh"
  sudo cp "$WD_PLIST_LOCAL" "$WD_PLIST_DEST"
  sudo chown root:wheel "$WD_PLIST_DEST"
  sudo chmod 644 "$WD_PLIST_DEST"
  reload_daemon com.orbinea.vllm-watchdog "$WD_PLIST_DEST"
else
  echo "Watchdog non déployé (vllm-watchdog.sh ou plist absent de ~)." >&2
fi

step "Validation"
sleep 5
sudo launchctl print "system/com.orbinea.vllm-mlx" | grep -E "state|active count|last exit" || true

echo
echo "Le modèle 7B prend 30-60 s à charger en MLX. Vérifie ensuite :"
echo "  tail -f ~/Library/Logs/vllm-mlx/stderr.log"
echo "  curl -s http://localhost:8000/v1/models | jq"
echo
echo "Si tout est OK, shutdown propre depuis le host :"
echo "  ssh test@<vm-ip> 'sudo shutdown -h now'"
echo "Puis re-push de l'image OCI avec oras-macos-vz."
