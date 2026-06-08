#!/bin/bash
# Provisionne un Mac en nœud du cluster Chemin A : un nœud k3s dans une VM Linux
# lima bridgée sur le LAN, qui rejoint un cluster K8s multi-nœuds. Distinct de
# provision-mac-node.sh, qui reste pour le déploiement k3d shared-nothing de prod
# tant que la bascule n'est pas faite.
#
# Recette validée au spike du 2026-06-05 (mémoire project_chemin_a_lima_k3s_spike).
# vz suffit pour le réseau bridgé, pas besoin de qemu. macOS ne fait pas tourner
# k3s nativement, d'où la VM Linux ; le DMR Metal reste host-natif sur le Mac.
#
# Usage :
#   ROLE=server ./deploy/fleet/provision-mac-k3s.sh
#   ROLE=agent SERVER_IP=192.168.30.25 TOKEN=K10... ./deploy/fleet/provision-mac-k3s.sh
#
# Variables :
#   ROLE       server | agent              (obligatoire)
#   SERVER_IP  IP LAN du serveur k3s        (agent : obligatoire)
#   TOKEN      token de join k3s            (agent : obligatoire ; sur le serveur :
#              cat /var/lib/rancher/k3s/server/node-token dans la VM)
#   IFACE      interface LAN bridgée        (défaut en0)
#   VM         nom de l'instance lima       (défaut k3s-node)
#
# Le script fait les étapes automatisables et s'arrête en indiquant la commande
# sudo exacte si un prérequis root manque. Idempotent : relancer après le sudo.

set -euo pipefail
cd "$(dirname "$0")/../.."

ROLE="${ROLE:?définir ROLE=server|agent}"
IFACE="${IFACE:-en0}"
VM="${VM:-k3s-node}"
LOCALBIN="$HOME/.local/bin"
export PATH="$LOCALBIN:/opt/homebrew/bin:/usr/local/bin:$PATH"

step() { echo ""; echo "==> $1"; }

step "1. lima"
if ! command -v limactl >/dev/null; then
	echo "Installation de lima en standalone dans ~/.local"
	mkdir -p "$HOME/.local"
	ver=2.1.2
	curl -fsSL "https://github.com/lima-vm/lima/releases/download/v${ver}/lima-${ver}-Darwin-$(uname -m | sed 's/arm64/arm64/;s/x86_64/x86_64/').tar.gz" -o /tmp/lima.tar.gz
	tar -xzf /tmp/lima.tar.gz -C "$HOME/.local"
fi
limactl --version

step "2. socket_vmnet root-owned (prérequis manuel si absent)"
if [ ! -x /opt/socket_vmnet/bin/socket_vmnet ]; then
	echo "socket_vmnet absent de /opt/socket_vmnet (doit appartenir à root)."
	echo "Copier le binaire depuis un Mac de référence puis l'installer en root :"
	echo "  scp <ref-mac>:/opt/socket_vmnet/bin/socket_vmnet* /tmp/"
	echo "  sudo mkdir -p /opt/socket_vmnet/bin && sudo install -o root -g wheel -m 0755 /tmp/socket_vmnet /opt/socket_vmnet/bin/socket_vmnet"
	exit 1
fi

step "3. networks.yaml"
mkdir -p "$HOME/.lima/_config"
cat > "$HOME/.lima/_config/networks.yaml" <<EOF
group: everyone
paths:
  socketVMNet: /opt/socket_vmnet/bin/socket_vmnet
  varRun: /private/var/run/lima
networks:
  bridged:
    mode: bridged
    interface: $IFACE
EOF

step "4. sudoers lima + probe (prérequis manuel si désync)"
if ! limactl sudoers --check /etc/sudoers.d/lima >/dev/null 2>&1; then
	limactl sudoers > /tmp/lima-sudoers
	printf '%%everyone ALL=(root:wheel) NOPASSWD: /usr/bin/true\n' > /tmp/lima-probe-sudoers
	echo "Installer les sudoers, puis relancer ce script :"
	echo "  sudo install -o root /tmp/lima-sudoers /etc/sudoers.d/lima"
	echo "  sudo install -o root /tmp/lima-probe-sudoers /etc/sudoers.d/lima-probe"
	exit 1
fi

step "5. VM lima bridgée (vz)"
if limactl list 2>/dev/null | awk 'NR>1{print $1}' | grep -qx "$VM"; then
	limactl start "$VM" >/dev/null 2>&1 || true
else
	cat > /tmp/$VM.yaml <<EOF
base: template:default
vmType: vz
cpus: 2
memory: "4GiB"
disk: "20GiB"
networks:
  - lima: bridged
EOF
	limactl start --tty=false --name="$VM" /tmp/$VM.yaml
fi
NODE_IP=$(limactl shell "$VM" -- sh -c "ip -4 -br addr show lima0 | awk '{print \$3}' | cut -d/ -f1")
echo "IP LAN du nœud : $NODE_IP"

step "6. k3s ($ROLE)"
if [ "$ROLE" = server ]; then
	limactl shell "$VM" -- sudo sh -c "command -v k3s >/dev/null || curl -sfL https://get.k3s.io | INSTALL_K3S_EXEC='server --node-ip=$NODE_IP --flannel-iface=lima0 --tls-san=$NODE_IP --cluster-init --write-kubeconfig-mode=644' sh -"
	echo "Token de join (pour les agents) :"
	limactl shell "$VM" -- sudo cat /var/lib/rancher/k3s/server/node-token
else
	: "${SERVER_IP:?définir SERVER_IP pour un agent}"
	: "${TOKEN:?définir TOKEN pour un agent}"
	cat > "$HOME/k3s-agent.sh" <<EOF
#!/bin/sh
curl -sfL https://get.k3s.io | \\
  INSTALL_K3S_EXEC="agent --node-ip=$NODE_IP --flannel-iface=lima0" \\
  K3S_URL="https://$SERVER_IP:6443" K3S_TOKEN="$TOKEN" sh -
EOF
	limactl shell "$VM" -- sudo sh "/Users/$(whoami)/k3s-agent.sh"
fi

step "7. autostart au login"
limactl start-at-login "$VM"

echo ""
echo "Nœud $VM prêt ($NODE_IP). Sur le serveur, vérifier : kubectl get nodes."
echo "Image docverify : docker save | k3s ctr images import dans la VM, puis DaemonSet deploy/fleet/docverify-daemonset.yaml."
