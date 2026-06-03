# Provisioning de la VM macOS (vllm-mlx)

Ce dossier rassemble tout ce qu'on dépose **à l'intérieur** de la VM macOS pour y faire tourner et superviser `vllm-mlx`. On le pousse dans la VM avec `scp deploy/vm/* test@<vm-ip>:~/`, puis `setup-vm.sh` se charge du reste.

## Fichiers

| Fichier | Rôle |
|---|---|
| `setup-vm.sh` | Installe vllm-mlx et son venv, épingle `transformers==5.5.0` qui sinon casse la vision, pré-télécharge Qwen-7B, et installe les deux LaunchDaemons. Idempotent, et refuse de tourner hors du user `test`. |
| `cleanup-vm.sh` | Fait le ménage des caches et un zero-fill APFS avant la capture, pour réduire la taille de l'image OCI. |
| `vllm-watchdog.sh` | Envoie périodiquement une vraie inférence et, après trois échecs consécutifs, relance le serveur par `launchctl kickstart -k`. Couvre le GPU hang que Kubernetes ne voit pas. |
| `com.orbinea.vllm-mlx.plist` | LaunchDaemon du serveur vllm-mlx, user `test`. |
| `com.orbinea.vllm-watchdog.plist` | LaunchDaemon du watchdog, root, déclenché toutes les 30 secondes. |

Le LaunchAgent **host** `com.orbinea.macos-vz-kubelet.plist` ne se trouve pas ici mais dans `deploy/launchd/` : il pilote le virtual-kubelet sur le Mac, ce n'est pas du provisioning à l'intérieur de la VM.

## Artefacts host

Les fichiers de la VM source restent hors du repo, regroupés dans `~/docverify-vm/` : `disk.img`, `aux.img`, `vm.json` et le binaire `macosvm`. Pour re-pousser une image il faut le disque, l'auxiliaire, le `vm.json`, et le binaire `oras` de `~/oras-macos-vz`.

## Recapture

La recapture est entièrement automatisée par un script, qui réutilise `setup-vm.sh` et `cleanup-vm.sh` dans la VM.

```bash
./scripts/recapture-vm.sh              # bump du tag, boot, provisioning, cleanup, push, redeploy
./scripts/recapture-vm.sh --push-only  # reprise si la VM est déjà arrêtée et nettoyée
```

La procédure manuelle équivalente, étape par étape, est détaillée sur le wiki, pages [Préparation image VM](https://git.int.orbinea.example/tfuchs/docverify/wiki/Préparation-image-VM) et [Déploiement macOS-vz](https://git.int.orbinea.example/tfuchs/docverify/wiki/Déploiement-macOS-vz).
