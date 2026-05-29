# Provisioning de la VM macOS (vllm-mlx)

Tout le « strict nécessaire » pour configurer la VM source et la (re)capturer en image OCI servie par macOS-vz-kubelet. À pousser dans la VM via `scp deploy/vm/* test@<vm-ip>:~/`.

## Fichiers

| Fichier | Rôle |
|---|---|
| `setup-vm.sh` | Installe vllm-mlx + venv, **pin `transformers==5.5.0`** (sinon vision en charabia), pré-DL Qwen-7B, et bootstrap les deux LaunchDaemons. Idempotent (`reload_daemon` gère un service déjà actif). Refuse de tourner hors user `test`. |
| `cleanup-vm.sh` | Ménage caches + zero-fill APFS **avant capture** (réduit l'image OCI). Garde `whoami==test` (jamais sur le host). |
| `vllm-watchdog.sh` | Supervision : poll d'une **vraie inférence** ; après 3 échecs consécutifs → `launchctl kickstart -k` du serveur. Couvre le GPU hang Metal que K8s ne voit pas. |
| `com.orbinea.vllm-mlx.plist` | LaunchDaemon serveur vllm-mlx (user `test`, KeepAlive). |
| `com.orbinea.vllm-watchdog.plist` | LaunchDaemon watchdog (root, `StartInterval 30s`). |

> Le LaunchAgent **host** `com.orbinea.macos-vz-kubelet.plist` reste dans `deploy/launchd/` : il pilote le virtual-kubelet sur le Mac, ce n'est pas du provisioning *dans* la VM.

## Artefacts host (hors repo)

Regroupés dans `~/docverify-vm/` : `disk.img`, `aux.img`, `vm.json`, `macosvm`. Boot de la VM source : `cd ~/docverify-vm && ./macosvm -g vm.json`. Outils de rebuild (clones git) : `~/oras-macos-vz` (binaire `bin/darwin/arm64/oras`) et `~/macos-vz-kubelet`.

Strict nécessaire pour **re-pousser** une image : `disk.img` + `aux.img` + `vm.json` + le binaire `oras`.

## Recapture d'une image (procédure validée 2026-05-29)

```bash
# 1. Libérer la VM source (le pod occupe l'IP + la RAM)
kubectl delete pod macos

# 2. Booter la VM source (terminal séparé) — IP statique 192.168.20.10
cd ~/docverify-vm && ./macosvm -g vm.json

# 3. Provisionner
scp deploy/vm/* test@192.168.20.10:~/
ssh -t test@192.168.20.10 'bash ~/setup-vm.sh'

# 4. Vérifier (vllm répond + watchdog tourne)
curl -s http://192.168.20.10:8000/v1/models
ssh test@192.168.20.10 'tail ~/Library/Logs/vllm-mlx/watchdog.log'

# 5. Cleanup + shutdown
ssh -t test@192.168.20.10 'bash ~/cleanup-vm.sh'
ssh -t test@192.168.20.10 'sudo shutdown -h now'

# 6. Pousser (VM arrêtée obligatoire ; vérifier l'espace registry d'abord)
~/oras-macos-vz/bin/darwin/arm64/oras push --plain-http \
  --disk-path ~/docverify-vm/disk.img --aux-path ~/docverify-vm/aux.img \
  --hardware-model "$(jq -r .hardwareModel ~/docverify-vm/vm.json)" \
  --machine-id "$(jq -r .machineId ~/docverify-vm/vm.json)" \
  localhost:5000/macos-vm:<tag>

# 7. Déployer : bumper l'image dans deploy/k8s/macos.yaml, puis
kubectl apply -f deploy/k8s/macos.yaml
```

Détails et pièges : `docs/setup/macos-vz.md`, `docs/macos-vm-setup-guide.md`, `docs/setup/supervision.md`.
