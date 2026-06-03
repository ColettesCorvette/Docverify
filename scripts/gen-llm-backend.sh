#!/usr/bin/env bash
# gen-llm-backend.sh — génère un manifeste Kubernetes de backend LLM pour une
# machine Linux à GPU discret, derrière le même Service vllm-mlx que la VM macOS.
# docverify n'est pas modifié : il parle OpenAI à http://vllm-mlx:8000 quel que
# soit le moteur dessous.
#
# Détection : NVIDIA via nvidia-smi, AMD via rocminfo ou rocm-smi. Les GPU
# intégrés Intel sont ignorés volontairement : un iGPU ne tient pas un VLM 7B et
# n'expose pas de ressource Kubernetes exploitable. Sans GPU discret, le script
# refuse plutôt que de produire un manifeste qui ne démarrera jamais.
#
# Limite connue : un APU AMD intégré peut apparaître comme GPU sous rocminfo. Le
# script vise les cartes discrètes NVIDIA/AMD ; vérifier le node si la machine
# n'a qu'un APU.
#
# Usage :
#   ./scripts/gen-llm-backend.sh > deploy/k8s/vllm-linux.yaml
#   kubectl apply -f deploy/k8s/vllm-linux.yaml      # à la place de macos.yaml
#
# Env (défauts) :
#   MODEL(Qwen/Qwen2.5-VL-7B-Instruct) MAX_NUM_SEQS(5) GPU(auto|nvidia|amd)

set -euo pipefail

MODEL="${MODEL:-Qwen/Qwen2.5-VL-7B-Instruct}"
MAX_NUM_SEQS="${MAX_NUM_SEQS:-5}"
GPU="${GPU:-auto}"

# detect_gpu écrit nvidia|amd sur stdout, ou retourne 1 si aucun GPU discret.
detect_gpu() {
  if command -v nvidia-smi >/dev/null 2>&1 && nvidia-smi -L 2>/dev/null | grep -q '^GPU '; then
    echo nvidia; return 0
  fi
  if command -v rocminfo >/dev/null 2>&1 && rocminfo 2>/dev/null | grep -qiE 'Device Type:[[:space:]]+GPU'; then
    echo amd; return 0
  fi
  if command -v rocm-smi >/dev/null 2>&1 && rocm-smi --showid >/dev/null 2>&1; then
    echo amd; return 0
  fi
  return 1
}

if [ "$GPU" = auto ]; then
  if ! GPU="$(detect_gpu)"; then
    echo "Aucun GPU discret NVIDIA ou AMD detecte." >&2
    echo "Les iGPU Intel sont ignores : impossible d'y servir un VLM 7B." >&2
    echo "Forcer si besoin : GPU=nvidia $0   ou   GPU=amd $0" >&2
    exit 1
  fi
fi

case "$GPU" in
  nvidia) IMAGE="vllm/vllm-openai:latest"; GPU_RESOURCE="nvidia.com/gpu" ;;
  amd)    IMAGE="rocm/vllm:latest";        GPU_RESOURCE="amd.com/gpu" ;;
  *) echo "GPU inconnu : $GPU — attendu nvidia ou amd" >&2; exit 1 ;;
esac

echo "GPU=$GPU image=$IMAGE ressource=$GPU_RESOURCE modele=$MODEL" >&2

cat <<YAML
# Genere par scripts/gen-llm-backend.sh — backend LLM Linux pour GPU $GPU.
# A appliquer a la place de macos.yaml. docverify reste inchange : il vise le
# Service vllm-mlx. Le flag --no-enable-prefix-caching reprend la parade
# anti-contamination de la VM macOS ; l'ajuster selon la version de vLLM.
apiVersion: apps/v1
kind: Deployment
metadata:
  name: vllm-linux
  labels:
    app: vllm-mlx
spec:
  replicas: 1
  selector:
    matchLabels:
      app: vllm-mlx
  template:
    metadata:
      labels:
        app: vllm-mlx
    spec:
      containers:
      - name: vllm
        image: "$IMAGE"
        args:
        - "--model"
        - "$MODEL"
        - "--host"
        - "0.0.0.0"
        - "--port"
        - "8000"
        - "--max-num-seqs"
        - "$MAX_NUM_SEQS"
        - "--no-enable-prefix-caching"
        ports:
        - name: http
          containerPort: 8000
        resources:
          limits:
            $GPU_RESOURCE: "1"
---
apiVersion: v1
kind: Service
metadata:
  name: vllm-mlx
spec:
  type: ClusterIP
  selector:
    app: vllm-mlx
  ports:
  - port: 8000
    targetPort: 8000
YAML
