# docverify

![Go](https://img.shields.io/badge/Go-1.25-00ADD8?logo=go&logoColor=white)
![Kubernetes](https://img.shields.io/badge/Kubernetes-k3d-326CE5?logo=kubernetes&logoColor=white)
![Redis](https://img.shields.io/badge/Redis-7-DC382D?logo=redis&logoColor=white)

`docverify` est un service Go d'analyse automatisée de justificatifs administratifs (factures, CNI, passeports, justificatifs de domicile) via OCR + LLM multimodal, déployé sur Kubernetes et destiné à être intégré au back-office Regia.

Documentation disponible sur le [wiki](https://git.int.orbinea.example/tfuchs/docverify/wiki/Home).

## Comment ça marche

Regia envoie le document en POST sur l'API. Le handler dépose le job dans une queue Redis. Un pool de workers consomme la queue et exécute le pipeline pour chaque job : extraction par Tesseract sur les documents simples, fallback vers un LLM multimodal (Qwen2.5-VL-32B servi par vllm-mlx) pour les fonds complexes. Le résultat repart au handler qui répond à Regia avec un verdict JSON.

La queue Redis est partagée entre les pods docverify du cluster : un job reçu par n'importe quel pod peut être traité par n'importe quel worker, ce qui équilibre la charge sans coordination explicite.

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


## Setup vLLM-mlx

```bash
# vllm-mlx dans un venv Python dédié
uv venv ~/vllm-mlx-env --python 3.13

#cloner vllm-mlx
git clone https://github.com/waybarrios/vllm-mlx.git ~/vllm-mlx                                                       
cd ~/vllm-mlx 

#Activer + installer avec le support vision
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

## Lancer vllm-mlx sur le host

vllm-mlx tourne **hors cluster**, directement sur macOS, pour profiter du GPU Apple Silicon via Metal. Les pods docverify l'atteignent via `host.docker.internal:8000`.

```bash
source ~/vllm-mlx-env/bin/activate
vllm-mlx serve mlx-community/Qwen2.5-VL-32B-Instruct-4bit \
  --mllm --host 0.0.0.0 --port 8000 \
  --continuous-batching --max-num-seqs 5
```

- `--mllm` : mode vision
- `--host 0.0.0.0` : exposer hors loopback pour rendre vllm-mlx joignable depuis les pods
- `--continuous-batching --max-num-seqs 5` : batching de 5 requêtes simultanées, aligné sur le nombre de workers par pod docverify

Vérifier que l'API répond :

```bash
curl http://localhost:8000/v1/models | jq
```

## Déployer dans le cluster

```bash
kubectl apply -f deploy/k8s/redis.yaml
kubectl apply -f deploy/k8s/docverify-deployment.yaml
```

Deux manifests dans cet ordre :

- `redis.yaml` : Deployment + Service ClusterIP `redis:6379`. Sert de broker pour la queue partagée entre les pods docverify.
- `docverify-deployment.yaml` : Deployment 2 replicas + Service LoadBalancer sur 8680.

Vérification :

```bash
kubectl get pods,svc,deploy
kubectl rollout status deployment/docverify
kubectl rollout status deployment/redis
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

## Architecture

```
┌─────────────────────────── Host Mac ────────────────────────────┐
│                                                                  │
│  localhost:8680 ──┐                ┌── vllm-mlx :8000            │
│  localhost:8001 ──┤                │   (Qwen2.5-VL-32B, MLX)     │
│                   │                │                             │
│  ┌────────────────▼────────────────▼──────────────────────────┐ │
│  │  Colima VM (k3d bridge 172.19.0.0/16)                      │ │
│  │  ┌──────── k3d cluster mini-cluster ─────────────────────┐ │ │
│  │  │  serverlb (k3d-mini-cluster-serverlb)                │ │ │
│  │  │    :8680 → Service docverify (LoadBalancer)           │ │ │
│  │  │    :8001 → Service kubernetes-dashboard-lb           │ │ │
│  │  │                                                      │ │ │
│  │  │  server-0 (k3d-mini-cluster-server-0)                │ │ │
│  │  │    ├ docverify-pod-1  ──┐                             │ │ │
│  │  │    ├ docverify-pod-2  ──┼─→ host.docker.internal:8000 │ │ │
│  │  │    │      ↑↓ jobs via redis:6379                     │ │ │
│  │  │    ├ redis-pod (broker queue)                        │ │ │
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
│(LLM inference)│ │ (Vision+LLM)  │ │  (TTS + STT)  │ │ (Embeddings)  │
└───────────────┘ └───────────────┘ └───────────────┘ └───────────────┘
        │             │                         │             │
        └─────────────┴─────────────────────────┴─────────────┘
                                   │
                                   ▼
┌─────────────────────────────────────────────────────────────────────────┐
│                              MLX                                        │
│                (Apple ML Framework - Metal kernels)                      │
└─────────────────────────────────────────────────────────────────────────┘
```


