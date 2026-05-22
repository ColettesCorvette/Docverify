#!/usr/bin/env bash
# cleanup-vm.sh — réduit la taille du disque AVANT shutdown + capture OCI
#
# À exécuter dans la VM macOS source juste avant `sudo shutdown -h now`.
# Sert deux objectifs :
#   1. Supprimer ce qui ne sert plus à runtime (caches, logs, build artefacts).
#   2. Zero-fill les blocs libres APFS pour que la compression d'oras-macos-vz
#      ait le maximum d'entropie à compresser.
#
# Usage :
#   ssh test@<vm-ip> 'bash -s' < scripts/cleanup-vm.sh
#   ssh test@<vm-ip> 'sudo shutdown -h now'

set -euo pipefail

step() { printf "\n=== %s ===\n" "$1"; }

step "Avant ménage"
df -h / | tail -1

step "Caches Python (uv, pip)"
rm -rf "$HOME/.cache/uv" "$HOME/.cache/pip" 2>/dev/null || true

step "Caches Homebrew"
brew cleanup -s 2>/dev/null || true
rm -rf "$(brew --cache)" 2>/dev/null || true

step "Caches utilisateur macOS"
rm -rf "$HOME/Library/Caches"/* 2>/dev/null || true

step ".git de vllm-mlx (optionnel — gain ~200 Mo)"
rm -rf "$HOME/vllm-mlx/.git" 2>/dev/null || true

step "Logs système (avec sudo, ne pas faire si tu as besoin du forensic)"
sudo rm -rf /private/var/log/asl/*.asl 2>/dev/null || true
sudo rm -rf /private/var/log/DiagnosticMessages/* 2>/dev/null || true
sudo rm -rf /Library/Logs/DiagnosticReports/* 2>/dev/null || true

step "Caches /Library"
sudo rm -rf /Library/Caches/* 2>/dev/null || true
sudo rm -rf /private/var/folders/*/T/* 2>/dev/null || true

step "Pré-shutdown : zero-fill des blocs libres APFS"
echo "Ça va remplir / avec un fichier de zéros pour aider la compression oras."
echo "Le fichier est supprimé immédiatement après."
sudo dd if=/dev/zero of=/var/zerofile bs=1m status=progress 2>&1 || true
sudo rm -f /var/zerofile
sync

step "Après ménage"
df -h / | tail -1
du -sh "$HOME/.cache/huggingface" 2>/dev/null || true
du -sh "$HOME/vllm-mlx-env" 2>/dev/null || true

echo
echo "Prêt pour shutdown + push. Sur le host :"
echo "  ssh test@\$(kubectl get pod macos -o jsonpath='{.status.podIP}') 'sudo shutdown -h now'"
echo "  # attendre l'arrêt de la VM, puis ré-exécuter oras-macos-vz push"
