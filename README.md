# docverify

![Go](https://img.shields.io/badge/Go-1.25-00ADD8?logo=go&logoColor=white)
![Kubernetes](https://img.shields.io/badge/Kubernetes-k3d-326CE5?logo=kubernetes&logoColor=white)
![macOS](https://img.shields.io/badge/macOS-launchd-000000?logo=apple&logoColor=white)

`docverify` est un service Go d'analyse automatisée de justificatifs administratifs (factures, CNI, passeports, justificatifs de domicile) via OCR + LLM multimodal, déployé sur Kubernetes et destiné à être intégré au back-office Regia.

Documentation disponible sur le [wiki](https://git.int.orbinea.example/tfuchs/docverify/wiki/Home).

> **Branche `feat/macos-img`** : vllm-mlx ne tourne plus sur le host mais **dans une VM macOS pilotée par Kubernetes** via [macOS-vz-kubelet](https://github.com/agoda-com/macOS-vz-kubelet). L'archi côté code Go est identique à `main` : seuls les artefacts de déploiement diffèrent (`deploy/k8s/macos.yaml`, `deploy/launchd/com.orbinea.vllm-mlx.plist`, `LLM_URL` dans `docverify-deployment.yaml`). Voir [Déploiement macOS-vz](https://git.int.orbinea.example/tfuchs/docverify/wiki/Déploiement-macOS-vz) sur le wiki pour la procédure complète.

## Comment ça marche

Regia envoie le document en POST sur l'API. Le handler dépose le job dans une queue in-process Go : un channel bufferisé alimenté par les requêtes HTTP et consommé par un pool de workers. Chaque worker exécute le pipeline OCR + analyse : extraction par `pdftotext` ou Tesseract sur les documents simples, fallback vers un LLM multimodal (Qwen2.5-VL-32B servi par vllm-mlx) pour les images et fonds complexes.

Si la première passe d'analyse renvoie un score d'information faible ou un verdict `unreadable`, le pipeline tente un second passage par segmentation : ImageMagick détecte les zones de contenu rectangulaires distinctes sur la page (typique d'un recto + verso de CNI scannés ensemble sur une A4), crope chaque zone et la fait OCRiser séparément. Le résultat segmenté n'est retenu que s'il extrait strictement plus de champs que la première passe.

vllm-mlx tourne dans une VM macOS dans le cluster. Le host Mac s'enregistre comme node `darwin/arm64` via [macOS-vz-kubelet](https://github.com/agoda-com/macOS-vz-kubelet), Kubernetes ordonnance dessus le pod `macos`, le virtual-kubelet pull l'image OCI de la VM depuis le registry k3d et la démarre via `Virtualization.framework`. Dans la VM, un `LaunchDaemon` lance et supervise `vllm-mlx`. Les pods docverify l'atteignent via le Service Kubernetes `vllm-mlx:8000`.

## Projets externes utilisés

| Projet | URL | Rôle |
|---|---|---|
| `agoda-com/macOS-vz-kubelet` | https://github.com/agoda-com/macOS-vz-kubelet | Virtual Kubelet provider qui pilote `Virtualization.framework` pour démarrer des VMs macOS comme pods |
| `agoda-com/oras-macos-vz` | https://github.com/agoda-com/oras-macos-vz | Fork d'ORAS qui packe les images VM (disk + aux + hardware-model + machine-id) au format OCI compatible |
| `s-u/macosvm` | https://github.com/s-u/macosvm | Wrapper `Virtualization.framework` pour créer et booter la VM source pendant la préparation de l'image |
| `waybarrios/vllm-mlx` | https://github.com/waybarrios/vllm-mlx | Serveur compatible OpenAI sur stack MLX (Apple Silicon), installé dans la VM avant capture |
| `mlx-community/Qwen2.5-VL-32B-Instruct-4bit` | https://huggingface.co/mlx-community/Qwen2.5-VL-32B-Instruct-4bit | Modèle vision-language utilisé par vllm-mlx |
| `virtual-kubelet/virtual-kubelet` | https://github.com/virtual-kubelet/virtual-kubelet | Library Kubernetes sous-jacente à `macOS-vz-kubelet` |

## Machines

- **Production / dev actuel** : 1x Mac Mini M4 Pro (ARM64, 12 CPU, 48 Go RAM unifiée)
- **Cibles futures** : 3 machines en attente d'attribution

## Prérequis

```bash
# Runtime Docker
brew install colima docker docker-compose

# Outils K8s + traitement docs
brew install k3d kubectl tesseract tesseract-lang poppler imagemagick jq

# Stack Go + Python
brew install go uv python@3.13

# Repos macOS-vz à cloner à côté de docverify
git clone https://github.com/agoda-com/macOS-vz-kubelet ~/macos-vz-kubelet
git clone https://github.com/agoda-com/oras-macos-vz ~/oras-macos-vz
```

## Préparer la VM macOS source

C'est l'étape la plus longue (création d'image, install vllm-mlx, télécharger Qwen). Procédure détaillée sur le wiki : [Préparation image VM](https://git.int.orbinea.example/tfuchs/docverify/wiki/Préparation-image-VM). À la fin :

- `~/disk.img` + `~/aux.img` + `~/vm.json` (avec `hardwareModel` et `machineId` base64)
- Dans la VM (user `test`) : `vllm-mlx` installé, modèle Qwen pré-téléchargé, plist en `LaunchDaemon` dans `/Library/LaunchDaemons/`, SSH activé.

Deux scripts versionnés automatisent la majorité du setup :

```bash
# Setup vllm-mlx + LaunchDaemon dans une VM fraîche (boot manuel obligatoire avant)
scp scripts/setup-vm.sh deploy/launchd/com.orbinea.vllm-mlx.plist test@<vm-ip>:~/
ssh test@<vm-ip> 'bash ~/setup-vm.sh'

# Ménage caches + zero-fill APFS juste avant capture OCI (gain ~10-20% sur compression)
ssh test@<vm-ip> 'bash -s' < scripts/cleanup-vm.sh
ssh test@<vm-ip> 'sudo shutdown -h now'
```

## Build des outils macOS-vz

```bash
cd ~/macos-vz-kubelet
mkdir -p bin
go build -o bin/macos-vz-kubelet ./cmd/virtual-kubelet
codesign --entitlements resources/vz.entitlements -s - bin/macos-vz-kubelet
# binaire oras-macos-vz : déjà fourni dans bin/darwin/arm64/oras du repo
```

## Build de l'image docverify

```bash
cd docverify
docker build -f deploy/Dockerfile -t docverify-docverify:latest .
```

## Cluster k3d avec registry OCI

```bash
k3d cluster create mini-cluster \
  --api-port 6443 \
  -p "8680:8680@loadbalancer" \
  -p "8001:8001@loadbalancer" \
  --registry-create k3d-registry:0.0.0.0:5000

k3d image import docverify-docverify:latest -c mini-cluster
```

Le `--registry-create` ajoute un registre OCI accessible sur `localhost:5000` côté host et `k3d-registry:5000` intra-cluster — indispensable pour pousser l'image VM via `oras-macos-vz`.

Le flag `--api-port 6443` fixe le port du serveur API K8s. Sans lui, Docker attribue un port aléatoire à chaque démarrage et le kubeconfig devient invalide.

Vérification :

```bash
k3d cluster list
kubectl get nodes
curl -s http://localhost:5000/v2/ && echo "registry OK"
```

## Push de l'image VM dans le registry

**VM arrêtée obligatoire** sinon corruption du `disk.img` pendant le calcul des digests.

```bash
HW_MODEL=$(jq -r .hardwareModel ~/vm.json)
MACHINE_ID=$(jq -r .machineId ~/vm.json)

~/oras-macos-vz/bin/darwin/arm64/oras push \
  --plain-http \
  --disk-path ~/disk.img \
  --aux-path ~/aux.img \
  --hardware-model "$HW_MODEL" \
  --machine-id "$MACHINE_ID" \
  localhost:5000/macos-vm:26.5
```

Le `--plain-http` est crucial (registry HTTP, pas TLS). Push de 5-15 min pour un disque de 100 Go (compression ~30-50 Go).

## Lancer le virtual-kubelet

Extraction des certs TLS pour le virtual-kubelet (à refaire à chaque recréation du cluster) :

```bash
mkdir -p ~/.kube/k3d-tls
kubectl config view --raw --minify -o json | jq -r '.users[0].user["client-certificate-data"]' | base64 -d > ~/.kube/k3d-tls/tls.crt
kubectl config view --raw --minify -o json | jq -r '.users[0].user["client-key-data"]' | base64 -d > ~/.kube/k3d-tls/tls.key
kubectl config view --raw --minify -o json | jq -r '.clusters[0].cluster["certificate-authority-data"]' | base64 -d > ~/.kube/k3d-tls/ca.crt
chmod 600 ~/.kube/k3d-tls/tls.key
```

Lancement en foreground (pour debug ; à convertir en LaunchAgent host à terme) :

```bash
export APISERVER_CERT_LOCATION=$HOME/.kube/k3d-tls/tls.crt
export APISERVER_KEY_LOCATION=$HOME/.kube/k3d-tls/tls.key
export APISERVER_CA_CERT_LOCATION=$HOME/.kube/k3d-tls/ca.crt
export KUBECONFIG=$HOME/.kube/config
export DOCKER_HOST=unix:///Users/$USER/.colima/default/docker.sock
export VZ_SSH_USER=test
export VZ_SSH_PASSWORD=<mdp du user test dans la VM>

~/macos-vz-kubelet/bin/macos-vz-kubelet \
  --nodename macos-vz-mini \
  --provider-id "provider://$(uuidgen)" \
  --log-level debug
```

`kubectl get nodes` doit maintenant lister `macos-vz-mini` en plus du node Linux.

## Déployer dans le cluster

```bash
kubectl apply -f deploy/k8s/docverify-deployment.yaml
kubectl apply -f deploy/k8s/macos.yaml
kubectl get pods -w
```

Deux manifests :

- `docverify-deployment.yaml` : Deployment 2 replicas + Service LoadBalancer :8680. `LLM_URL = http://vllm-mlx:8000` (DNS interne du Service K8s, plus de `host.docker.internal`).
- `macos.yaml` : Pod unique sur le node `macos-vz-mini` (toleration `virtual-kubelet.io/provider=macos-vz`, nodeSelector `kubernetes.io/os=darwin`) + Service ClusterIP `vllm-mlx:8000`.

Le pod `macos` enchaîne `Pending → Downloading → ContainerCreating → Running` en 5-15 min selon le pull de l'image OCI.

Vérification :

```bash
kubectl get pods,svc,deploy
kubectl rollout status deployment/docverify
curl http://localhost:8680/health
```

## Tests

```bash
# Santé
curl http://localhost:8680/health

# Suite complète sur tous les samples
./scripts/test-api.sh all

# Un seul sample
./scripts/test-api.sh edf
./scripts/test-api.sh freepro
./scripts/test-api.sh cni8493
```

Test du parallélisme côté queue, 8 samples en parallèle dans 8 fichiers de log distincts :

```fish
mkdir -p /tmp/docverify-test
for fn in edf freepro sfr lucie cni8493 cni8498 justif-jpg justif-pdf
  ./scripts/test-api.sh $fn > /tmp/docverify-test/$fn.log 2>&1 &
end
wait
```

Les 5 premiers workers traitent en parallèle, les 3 suivants attendent qu'un slot se libère.

## Architecture

```
┌──────────────────────────── Host Mac ──────────────────────────────┐
│                                                                     │
│  macos-vz-kubelet (binaire codesigné)                               │
│      │ enregistre node virtuel "macos-vz-mini" (darwin/arm64)       │
│      │ pull image OCI depuis k3d-registry                           │
│      │ démarre VM via Virtualization.framework                      │
│      ▼                                                              │
│  ┌──────────────────────────────────────────────────────────────┐   │
│  │  Colima VM (k3d bridge)                                       │   │
│  │  ┌──── k3d cluster mini-cluster ─────────────────────────┐   │   │
│  │  │  serverlb                                              │   │   │
│  │  │    :8680 → Service docverify (LoadBalancer)             │   │   │
│  │  │    :8001 → Service kubernetes-dashboard-lb             │   │   │
│  │  │                                                        │   │   │
│  │  │  k3d-registry (registre OCI :5000)                     │   │   │
│  │  │                                                        │   │   │
│  │  │  k3d-mini-cluster-server-0  (Linux)                    │   │   │
│  │  │    ├ docverify-pod-1  ──┐                               │   │   │
│  │  │    ├ docverify-pod-2  ──┼─→ Service vllm-mlx :8000      │   │   │
│  │  │    └ kubernetes-dashboard                              │   │   │
│  │  │                          │                              │   │   │
│  │  │  macos-vz-mini  (Darwin, virtuel)                      │   │   │
│  │  │    └ pod "macos"  ◄──────┘                              │   │   │
│  │  └──────────────────────────────────────────────────────┘   │   │
│  └──────────────────────────────────────────────────────────────┘   │
│      ▲ k3d bridge                                                   │
│      │                                                              │
│  ┌──────────────────────────────────────────────────────────────┐   │
│  │ VM macOS clonée à partir de macos-vm:26.5                     │   │
│  │   user "test"                                                  │   │
│  │   /Library/LaunchDaemons/com.orbinea.vllm-mlx.plist             │   │
│  │   vllm-mlx :8000 (Qwen2.5-VL-32B, MLX)                         │   │
│  │   restart auto sur crash, logs /Users/test/Library/Logs/...   │   │
│  └──────────────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────────────┘
```

### Stack vllm-mlx dans la VM

```
┌─────────────────────────────────────────────────────────────────────────┐
│                           vLLM API Layer                                │
│                    (OpenAI-compatible interface)                         │
└─────────────────────────────────────────────────────────────────────────┘
                                   │
                                   ▼
┌─────────────────────────────────────────────────────────────────────────┐
│                            MLXPlatform                                  │
│               (vLLM platform plugin for Apple Silicon)                  │
└─────────────────────────────────────────────────────────────────────────┘
                                   │
        ┌─────────────┬────────────┴────────────┬─────────────┐
        ▼             ▼                         ▼             ▼
┌───────────────┐ ┌───────────────┐ ┌───────────────┐ ┌───────────────┐
│    mlx-lm     │ │   mlx-vlm     │ │   mlx-audio   │ │mlx-embeddings │
│ LLM inference│ │ Vision+LLM    │ │  TTS + STT    │ │ Embeddings    │
└───────────────┘ └───────────────┘ └───────────────┘ └───────────────┘
        │             │                         │             │
        └─────────────┴─────────────────────────┴─────────────┘
                                   │
                                   ▼
┌─────────────────────────────────────────────────────────────────────────┐
│                              MLX                                        │
│                Apple ML Framework, Metal kernels                         │
└─────────────────────────────────────────────────────────────────────────┘
```


