#!/usr/bin/env bash
# Build une image de backend LLM vLLM pour une plateforme donnée. Remplace la
# détection-à-l'exécution de gen-llm-backend.sh par des images préparées et
# versionnées : on construit, on tague, on pousse, le déploiement référence le tag.
#
# Usage :
#   PLATFORM=cuda ./deploy/llm/build.sh
#   PLATFORM=rocm VLLM_VERSION=latest REGISTRY=registry.int.orbinea.example ./deploy/llm/build.sh
#
# Variables :
#   PLATFORM       cuda | rocm | xpu | cpu   (obligatoire)
#   VLLM_VERSION   tag de base vLLM           (défaut par plateforme)
#   REGISTRY       préfixe de registre        (push si défini)
#   IMAGE          nom complet de l'image     (sinon dérivé)
#
# Important : ces images ciblent du linux/amd64 et un accélérateur précis. Les
# construire et les tester sur la machine cible ou un CI à l'architecture
# correspondante, pas sur un Mac Apple Silicon. macOS n'est pas concerné : la
# flotte Mac sert le LLM via Docker Model Runner, pas vLLM.

set -euo pipefail
cd "$(dirname "$0")"

PLATFORM="${PLATFORM:?définir PLATFORM=cuda|rocm|xpu|cpu}"
case "$PLATFORM" in
	cuda|rocm|xpu|cpu) ;;
	*) echo "PLATFORM invalide : $PLATFORM (attendu cuda|rocm|xpu|cpu)" >&2; exit 1 ;;
esac

# Version vLLM par défaut, surchargée par VLLM_VERSION.
default_version() {
	case "$1" in
		xpu) echo "latest" ;;   # images XPU Intel suivent leurs propres tags
		cpu) echo "v0.18.0" ;;  # build depuis les sources, tag git
		*)   echo "latest" ;;
	esac
}
VLLM_VERSION="${VLLM_VERSION:-$(default_version "$PLATFORM")}"

IMAGE="${IMAGE:-docverify-vllm-${PLATFORM}:${VLLM_VERSION}}"
[ -n "${REGISTRY:-}" ] && IMAGE="${REGISTRY%/}/${IMAGE}"

echo "==> build $IMAGE depuis Dockerfile.$PLATFORM (vLLM $VLLM_VERSION, linux/amd64)"
docker build --platform linux/amd64 \
	--build-arg "VLLM_VERSION=${VLLM_VERSION}" \
	-f "Dockerfile.${PLATFORM}" -t "$IMAGE" .

if [ -n "${REGISTRY:-}" ]; then
	echo "==> push $IMAGE"
	docker push "$IMAGE"
fi
echo "OK : $IMAGE"
