# Backends LLM vLLM par type de machine

Ce dossier contient des images vLLM prêtes à construire, une par accélérateur Linux, pour servir le modèle vision derrière une API OpenAI. J'ai préféré des images versionnées à un script d'installation : une image construite et taguée se reproduit et s'audite, un script dépend de l'état de la machine.

docverify n'a pas besoin d'être modifié : il parle OpenAI au Service `vllm`, donc `LLM_URL=http://vllm:8000/v1` et c'est tout.

## Plateformes

| Plateforme | Dockerfile | Base | Ressource K8s |
|---|---|---|---|
| NVIDIA CUDA | `Dockerfile.cuda` | `vllm/vllm-openai` | `nvidia.com/gpu` |
| AMD ROCm | `Dockerfile.rocm` | `vllm/vllm-openai-rocm` | `amd.com/gpu` |
| Intel Arc / XPU | `Dockerfile.xpu` | `intel/vllm` | `gpu.intel.com/i915` |
| CPU | `Dockerfile.cpu` | build depuis les sources vLLM | cpu / memory |

**macOS n'est pas dans la liste**, et c'est voulu. Sur les Macs du cluster, le LLM est servi par Docker Model Runner en process hôte, avec llama.cpp et un GGUF sur Metal. vLLM sur Metal ne gère pas la vision, donc une image macOS n'aurait aucun sens pour notre modèle.

## Construire

```bash
PLATFORM=cuda ./deploy/llm/build.sh
PLATFORM=rocm VLLM_VERSION=latest REGISTRY=registry.int.orbinea.example ./deploy/llm/build.sh
```

Il faut construire et tester **sur la machine cible**, ou sur un CI de la même architecture : ce sont des images linux/amd64 liées à un accélérateur précis, impossibles à valider sur un Mac Apple Silicon. La variante CPU marche mais reste lente par nature pour un VLM de cette taille, voir le bench DMR CPU contre Metal.

## Déployer

1. Construire et pousser l'image correspondant à la plateforme du nœud.
2. Dans `vllm-deployment.yaml`, mettre l'image, décommenter la bonne ressource GPU, et le `nodeSelector` si le cluster est hétérogène.
3. Vérifier que le device plugin de l'accélérateur est présent sur le cluster : NVIDIA device plugin, AMD GPU operator ou Intel device plugin.
4. `kubectl apply -f deploy/llm/vllm-deployment.yaml`, puis pointer `LLM_URL=http://vllm:8000/v1` côté docverify.

Le modèle `google/gemma-4-e4b-it` est téléchargé depuis Hugging Face au démarrage. C'est le même Gemma 4 E4B que sert DMR sur les Macs, mais en modèle HF complet plutôt qu'en GGUF Q5_K_M, parce que vLLM sert nativement les modèles HF, vision comprise. Attention, Gemma est gated : il faut accepter la licence sur Hugging Face puis fournir `HF_TOKEN` via un Secret. Prévoir aussi un volume persistant pour le cache, sinon chaque pod re-télécharge le modèle.

À VALIDER : le bench groundtruth a été mesuré sur le GGUF Q5_K_M sous DMR, pas sur ce modèle HF complet servi par vLLM. Il faudra le rejouer sur une machine GPU avant de mettre cette voie en production.

## Référence

La logique vLLM par plateforme vient de la doc officielle vLLM. Les images ROCm officielles `vllm/vllm-openai-rocm` existent depuis janvier 2026. La variante XPU a été validée sur Intel Arc Pro B.
