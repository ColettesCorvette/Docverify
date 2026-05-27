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
#   scp scripts/setup-vm.sh deploy/launchd/com.orbinea.vllm-mlx.plist test@<vm-ip>:~/
#   ssh test@<vm-ip> 'bash ~/setup-vm.sh'

set -euo pipefail

if [[ "$(whoami)" != "test" ]]; then
  echo "Ce script doit tourner sous le user 'test' dans la VM. User actuel : $(whoami)" >&2
  exit 1
fi

PLIST_LOCAL="$HOME/com.orbinea.vllm-mlx.plist"
PLIST_DEST="/Library/LaunchDaemons/com.orbinea.vllm-mlx.plist"

step() { printf "\n=== %s ===\n" "$1"; }

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

if sudo launchctl print "system/com.orbinea.vllm-mlx" >/dev/null 2>&1; then
  sudo launchctl bootout "system/com.orbinea.vllm-mlx" || true
fi
sudo launchctl bootstrap system "$PLIST_DEST"

step "Validation"
sleep 5
sudo launchctl print "system/com.orbinea.vllm-mlx" | grep -E "state|active count|last exit" || true

echo
echo "Le modèle 32B prend 30-60 s à charger en MLX. Vérifie ensuite :"
echo "  tail -f ~/Library/Logs/vllm-mlx/stderr.log"
echo "  curl -s http://localhost:8000/v1/models | jq"
echo
echo "Si tout est OK, shutdown propre depuis le host :"
echo "  ssh test@<vm-ip> 'sudo shutdown -h now'"
echo "Puis re-push de l'image OCI avec oras-macos-vz."
