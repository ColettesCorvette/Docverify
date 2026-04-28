# docverify

`docverify` est un programme qui valide des documents administratifs (factures, CNI, passeports, justificatifs de domicile) via OCR + LLM.

Documentation disponible sur le [wiki Gitea](https://git.int.orbinea.example/tfuchs/docverify/wiki/Home).

**Machines :** 

1x Mac Mini M4 Pro (ARM64) - 12 CPUs — 48 GB RAM

3x En attente 

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

### Création 

```bash
k3d cluster create mini-cluster -p "8680:8680@loadbalancer" -p "8001:8001@loadbalancer"
k3d image import docverify-docverify:latest -c mini-cluster
```
Les port mappings exposent directement `localhost:8680` (app) et `localhost:8001` (dashboard) sans nécessiter `kubectl port-forward` ni `kubectl proxy`.

### Vérifier

```bash
k3d cluster list
kubectl get nodes
kubectl cluster-info
```

### Lancer vllm-mlx sur le host 

```bash
source ~/vllm-mlx-env/bin/activate
vllm-mlx serve mlx-community/gemma-4-e4b-it-4bit \
  --mllm --host 0.0.0.0 --port 8000 \
  --continuous-batching --max-num-seqs 5
```
- `--mllm` : mode vision 
- `--host 0.0.0.0` : exposer hors loopback pour que les pods K8s puissent y accéder via `host.docker.internal`
- `--continuous-batching --max-num-seqs 5` : batching de 5 requêtes simultanées 

### Verifier que l'API repond

```bash
curl http://localhost:8000/v1/models | jq
```

### Appliquer les manifests

```bash
kubectl apply -f deploy/k8s/docverify-deployment.yaml
```

### Vérifier

```bash
kubectl get pods,svc,deploy
kubectl rollout status deployment/docverify
curl http://localhost:8680/health
```

### Tests

```bash
# Santé
curl http://localhost:8680/health

# Suite complète 
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
│  localhost:8001 ──┤                │   (Gemma-4 e4b, MLX)        │
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