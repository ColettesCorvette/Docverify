# Backends LLM vLLM par type de machine

Images vLLM préparées et versionnées pour servir le VLM derrière une API
OpenAI, une par accélérateur Linux. Des images construites et
taguées sont plus reproductibles et auditables qu'un script.

docverify n'est pas modifié : il parle OpenAI au Service `vllm`, soit
`LLM_URL=http://vllm:8000/v1`.

## Plateformes

| Plateforme | Dockerfile | Base | Ressource K8s |
|---|---|---|---|
| NVIDIA CUDA | `Dockerfile.cuda` | `vllm/vllm-openai` | `nvidia.com/gpu` |
| AMD ROCm | `Dockerfile.rocm` | `vllm/vllm-openai-rocm` | `amd.com/gpu` |
| Intel Arc / XPU | `Dockerfile.xpu` | `intel/vllm` | `gpu.intel.com/i915` |
| CPU | `Dockerfile.cpu` | build sources vLLM | cpu / memory |

**macOS n'est pas ici.** Le cluster Mac sert le LLM via Docker Model Runner
host-native (Metal, llama.cpp GGUF) dans le cluster K8s multi-Mac. vLLM sur Metal
ne gère pas la vision, donc aucune image macOS n'a de sens pour notre VLM.

## Construire

```bash
PLATFORM=cuda ./deploy/llm/build.sh
PLATFORM=rocm VLLM_VERSION=latest REGISTRY=registry.int.orbinea.example ./deploy/llm/build.sh
```

À construire et tester **sur la machine cible** ou un CI à l'architecture
correspondante : ce sont des images linux/amd64 liées à un accélérateur précis,
non validables sur un Mac Apple Silicon. CPU = fallback fonctionnel, lent par
nature pour un VLM 7B, cf. le bench DMR CPU vs Metal.

## Déployer

1. Construire et pousser l'image de la plateforme du node.
2. Dans `vllm-deployment.yaml` : mettre l'image, décommenter la bonne ressource
   GPU, et le `nodeSelector` si le cluster est hétérogène.
3. Le device plugin de l'accélérateur doit être présent sur le cluster : NVIDIA
   device plugin, AMD GPU operator, ou Intel device plugin.
4. `kubectl apply -f deploy/llm/vllm-deployment.yaml`, puis pointer
   `LLM_URL=http://vllm:8000/v1` côté docverify.

Le modèle `google/gemma-4-e4b-it` est téléchargé depuis Hugging Face au
démarrage : c'est le Gemma 4 E4B servi par DMR sur les Macs, ici en modèle HF
complet plutôt qu'en GGUF Q8 (vLLM sert nativement les modèles HF, vision
incluse). Gemma est gated : fournir `HF_TOKEN` via un Secret après acceptation
de la licence sur Hugging Face. Prévoir un volume persistant pour le cache si
l'on ne veut pas re-télécharger à chaque pod.

À VALIDER : le bench groundtruth (100 % avec le GGUF Q8 sous DMR) n'a pas encore
été rejoué avec ce modèle HF complet servi par vLLM sur une machine GPU. Le
relancer avant de mettre cette voie en production.

## Référence

Logique vLLM par plateforme : doc officielle vLLM. Images ROCm officielles
`vllm/vllm-openai-rocm` depuis janvier 2026. XPU validé sur Intel Arc Pro B.
