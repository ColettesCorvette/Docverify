#!/usr/bin/env bash
#
# deploy/cluster/install-wg-autostart.sh
# Installe le LaunchDaemon WireGuard sur un nœud DÉJÀ onboardé (clés/conf wg0
# présentes), pour que le tunnel remonte seul au reboot. C'est l'étape 5 de
# bootstrap-node.sh extraite, à rejouer sur les Macs antérieurs au correctif.
#
#   ./deploy/cluster/install-wg-autostart.sh <ip> [user]
#   for ip in 10.40.1.10 10.40.1.11 10.40.1.12; do "$0" "$ip"; done
#
# Le payload contient des sudo : on l'envoie d'abord (scp, sans TTY) puis on le
# lance via 'ssh -t' AVEC stdin = ton terminal, pour que sudo lise le mot de
# passe. (Un 'ssh -t host bash -s <<EOF' échoue : le heredoc occupe stdin,
# sudo n'a pas de TTY → « a terminal is required ».)
set -euo pipefail

IP="${1:-}"
SSH_USER="${2:-cluster}"
[ -n "$IP" ] || { echo "Usage : $0 <ip> [user]"; exit 1; }
TARGET="${SSH_USER}@${IP}"

echo "== WireGuard autostart sur ${TARGET} =="

TMP=$(mktemp)
trap 'rm -f "$TMP"' EXIT
cat > "$TMP" <<'REMOTE'
set -euo pipefail
export PATH="/opt/homebrew/bin:/opt/homebrew/sbin:/usr/bin:/bin:/usr/sbin:/sbin"

CONF=/opt/homebrew/etc/wireguard/wg0.conf
[ -f "$CONF" ] || { echo "wg0.conf absent ($CONF), provisionne les clés d'abord."; exit 1; }
command -v wg-quick >/dev/null || brew install wireguard-tools

# AMFI bloque les binaires Homebrew non signés lancés par launchd au boot
# (« has no CMS blob »). Signature ad-hoc → AMFI les accepte. À refaire après
# 'brew upgrade wireguard-tools/wireguard-go' (réinstalle des binaires non signés).
for b in wg wireguard-go; do
    p=$(readlink -f "/opt/homebrew/bin/$b" 2>/dev/null || python3 -c "import os;print(os.path.realpath('/opt/homebrew/bin/$b'))")
    [ -x "$p" ] && sudo codesign --force --sign - "$p"
done

PLIST=/Library/LaunchDaemons/com.wireguard.wg0.plist
sudo tee "$PLIST" >/dev/null <<'PLISTEOF'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>com.wireguard.wg0</string>
  <key>ProgramArguments</key>
  <array>
    <string>/opt/homebrew/bin/wg-quick</string>
    <string>up</string>
    <string>wg0</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key><string>/opt/homebrew/bin:/opt/homebrew/sbin:/usr/bin:/bin:/usr/sbin:/sbin</string>
  </dict>
  <key>RunAtLoad</key><true/>
  <!-- Endpoint en DNS + race de boot : wg-quick échoue tant que le réseau/DNS
       n'est pas prêt. SuccessfulExit=false → launchd relance sur exit≠0 et
       s'arrête au premier exit 0 (tunnel monté). -->
  <key>KeepAlive</key>
  <dict><key>SuccessfulExit</key><false/></dict>
  <key>ThrottleInterval</key><integer>5</integer>
  <key>StandardErrorPath</key><string>/var/log/wg0.err.log</string>
  <key>StandardOutPath</key><string>/var/log/wg0.out.log</string>
</dict>
</plist>
PLISTEOF
sudo chown root:wheel "$PLIST"
sudo chmod 644 "$PLIST"

sudo launchctl bootout system/com.wireguard.wg0 2>/dev/null || true
sudo wg-quick down wg0 2>/dev/null || true
sudo launchctl bootstrap system "$PLIST"
sleep 2
sudo launchctl print system/com.wireguard.wg0 | grep -E 'state|last exit'
wg show wg0 | head -3
REMOTE

scp -q "$TMP" "${TARGET}:/tmp/wg-autostart.sh"
ssh -t "$TARGET" 'bash /tmp/wg-autostart.sh; rm -f /tmp/wg-autostart.sh'
echo "-> ${IP} OK"
