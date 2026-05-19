# docverify

![Go](https://img.shields.io/badge/Go-1.25-00ADD8?logo=go&logoColor=white)
![Kubernetes](https://img.shields.io/badge/Kubernetes-k3d-326CE5?logo=kubernetes&logoColor=white)
![macOS](https://img.shields.io/badge/macOS-launchd-000000?logo=apple&logoColor=white)

`docverify` est un service Go d'analyse automatisée de justificatifs administratifs (factures, CNI, passeports, justificatifs de domicile) via OCR + LLM multimodal, déployé sur Kubernetes et destiné à être intégré au back-office Regia.

Documentation disponible sur le [wiki](https://git.int.orbinea.example/tfuchs/docverify/wiki/Home).

## Comment ça marche

Regia envoie le document en POST sur l'API. Le handler dépose le job dans une queue in-process Go : un channel bufferisé alimenté par les requêtes HTTP et consommé par un pool de workers. Chaque worker exécute le pipeline OCR + analyse : extraction par `pdftotext` ou Tesseract sur les documents simples, fallback vers un LLM multimodal (Qwen2.5-VL-32B servi par vllm-mlx) pour les images et fonds complexes.

Si la première passe d'analyse renvoie un score d'information faible ou un verdict `unreadable`, le pipeline tente un second passage par segmentation : ImageMagick détecte les zones de contenu rectangulaires distinctes sur la page (typique d'un recto + verso de CNI scannés ensemble sur une A4), crope chaque zone et la fait OCRiser séparément. Le résultat segmenté n'est retenu que s'il extrait strictement plus de champs que la première passe.

vllm-mlx tourne hors cluster, directement sur macOS, supervisé par launchd pour bénéficier d'un restart automatique en cas de crash. Les pods docverify l'atteignent via `host.docker.internal:8000`.

## Machines

- **Production / dev actuel** : 1x Mac Mini M4 Pro (ARM64, 12 CPU, 48 Go RAM unifiée)
- **Cibles futures** : 3 machines en attente d'attribution

## Prérequis

```bash
# Runtime Docker
brew install colima docker docker-compose

# Outils K8s + traitement docs
brew install k3d kubectl tesseract tesseract-lang poppler imagemagick jq

# Stack Go + Python pour vllm-mlx
brew install go uv python@3.13
```


## Setup vllm-mlx

```bash
# venv Python dédié
uv venv ~/vllm-mlx-env --python 3.13

# cloner vllm-mlx
git clone https://github.com/waybarrios/vllm-mlx.git ~/vllm-mlx
cd ~/vllm-mlx

# activer + installer avec le support vision
source ~/vllm-mlx-env/bin/activate
uv pip install -e ".[vision]"
```

## Build de l'image docverify

```bash
cd docverify
docker build -f deploy/Dockerfile -t docverify-docverify:latest .
```

## Cluster k3d

```bash
k3d cluster create mini-cluster \
  --api-port 6443 \
  -p "8680:8680@loadbalancer" \
  -p "8001:8001@loadbalancer"
k3d image import docverify-docverify:latest -c mini-cluster
```

Les port mappings exposent directement `localhost:8680` (API docverify) et `localhost:8001` ([dashboard K8s](https://git.int.orbinea.example/tfuchs/docverify/wiki/Dashboard)) sans nécessiter `kubectl port-forward` ni `kubectl proxy`.

Le flag `--api-port 6443` fixe le port du serveur API K8s sur le host. Sans lui, Docker attribue un port aléatoire à chaque démarrage du cluster, ce qui invalide le kubeconfig au moindre redémarrage du Mac et casse `kubectl`.

Vérification rapide :

```bash
k3d cluster list
kubectl get nodes
kubectl cluster-info
```

## Supervision vllm-mlx via launchd

vllm-mlx ne doit pas tourner dans un terminal en V1 : si le terminal ferme ou si le process crash, l'inférence s'arrête sans alerte. Un LaunchAgent macOS le supervise avec restart automatique sur crash.

```bash
mkdir -p ~/Library/Logs/vllm-mlx
cp deploy/launchd/com.orbinea.vllm-mlx.plist ~/Library/LaunchAgents/
launchctl load ~/Library/LaunchAgents/com.orbinea.vllm-mlx.plist
```

Vérifier :

```bash
launchctl list | grep vllm-mlx          # PID + dernier code de sortie
curl http://localhost:8000/v1/models | jq
tail -f ~/Library/Logs/vllm-mlx/stdout.log
```

Détails de la config et étapes suivantes (watchdog HTTP, supervision multi-Mac) dans `docs/setup/supervision.md`.

## Déployer dans le cluster

```bash
kubectl apply -f deploy/k8s/docverify-deployment.yaml
```

Un seul manifest : Deployment 2 replicas + Service LoadBalancer sur 8680.

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
┌─────────────────────────── Host Mac ────────────────────────────┐
│                                                                  │
│  ┌─── LaunchAgent com.orbinea.vllm-mlx ─────────────────────┐    │
│  │  vllm-mlx :8000  (Qwen2.5-VL-32B, MLX)                  │    │
│  │  restart auto si crash, logs ~/Library/Logs/vllm-mlx/   │    │
│  └─────────────────────────────────────────────────────────┘    │
│                                ▲                                 │
│  localhost:8680 ──┐            │                                 │
│  localhost:8001 ──┤            │                                 │
│  ┌────────────────▼────────────┴──────────────────────────────┐ │
│  │  Colima VM (k3d bridge 172.19.0.0/16)                      │ │
│  │  ┌──────── k3d cluster mini-cluster ─────────────────────┐ │ │
│  │  │  serverlb (k3d-mini-cluster-serverlb)                │ │ │
│  │  │    :8680 → Service docverify (LoadBalancer)           │ │ │
│  │  │    :8001 → Service kubernetes-dashboard-lb           │ │ │
│  │  │                                                      │ │ │
│  │  │  server-0 (k3d-mini-cluster-server-0)                │ │ │
│  │  │    ├ docverify-pod-1  ──┐                             │ │ │
│  │  │    │   queue in-process, 5 workers                   │ │ │
│  │  │    ├ docverify-pod-2  ──┼─→ host.docker.internal:8000 │ │ │
│  │  │    │   queue in-process, 5 workers                   │ │ │
│  │  │    └ kubernetes-dashboard                            │ │ │
│  │  └──────────────────────────────────────────────────────┘ │ │
│  └────────────────────────────────────────────────────────────┘ │
└──────────────────────────────────────────────────────────────────┘
```

### Stack vllm-mlx côté host

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

## Pour aller plus loin

| Sujet | Doc |
|---|---|
| API HTTP, champs, codes erreur | [docs/api.md](docs/api.md) |
| Queue Go + fallback segmentation | [docs/queue-pipeline.md](docs/queue-pipeline.md) |
| Supervision vllm-mlx, étapes 1-3 | [docs/setup/supervision.md](docs/setup/supervision.md) |
| Déploiement k3d détaillé | [docs/deploiement-k8s.md](docs/deploiement-k8s.md) |
| vllm-mlx, flags et benchs | [docs/vllm-mlx.md](docs/vllm-mlx.md) |
| Architecture cible V2 | [docs/architectureV2.md](docs/architectureV2.md) |
