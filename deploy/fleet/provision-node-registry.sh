#!/usr/bin/env bash
# Provisionne (ou re-provisionne) le k3d d'un Mac de la flotte pour pull docverify
# depuis le registre interne rhea. Idempotent : supprime le cluster existant et
# le recrée propre avec la config registre. Prérequis dans ~ : rhea-ca.crt,
# rhea-registries.yaml, docverify-deployment.yaml.
set -euo pipefail
export PATH="$HOME/.local/bin:/usr/local/bin:/opt/homebrew/bin:$PATH"
CLUSTER=mini-cluster

for b in k3d docker kubectl; do command -v "$b" >/dev/null || { echo "MANQUE: $b"; exit 1; }; done
for f in rhea-ca.crt rhea-registries.yaml docverify-deployment.yaml; do
  [ -f "$HOME/$f" ] || { echo "MANQUE fichier: ~/$f"; exit 1; }
done

echo "== suppression cluster existant (si présent) =="
k3d cluster delete "$CLUSTER" 2>/dev/null || true
echo "== nettoyage résidus réseau/volume =="
docker network rm "k3d-${CLUSTER}" 2>/dev/null || true
docker volume rm "k3d-${CLUSTER}-images" 2>/dev/null || true

echo "== création cluster (config registre rhea) =="
k3d cluster create "$CLUSTER" --api-port 6443 \
  -p "8680:8680@loadbalancer" -p "8001:8001@loadbalancer" \
  --registry-config "$HOME/rhea-registries.yaml" \
  --volume "$HOME/rhea-ca.crt:/etc/ssl/certs/rhea-ca.crt@server:0" \
  --host-alias 10.50.0.1:alpha.srv.orbinea

echo "== attente API ready =="
for i in $(seq 1 30); do kubectl get --raw=/readyz >/dev/null 2>&1 && break; sleep 2; done

echo "== apply manifest =="
kubectl apply -f "$HOME/docverify-deployment.yaml"

echo "== attente pod Running =="
for i in $(seq 1 40); do
  p=$(kubectl get pod -l app=docverify -o jsonpath='{.items[0].status.phase}' 2>/dev/null || true)
  r=$(kubectl get pod -l app=docverify -o jsonpath='{.items[0].status.containerStatuses[0].ready}' 2>/dev/null || true)
  [ "$p" = Running ] && [ "$r" = true ] && break
  sleep 3
done

echo "== résultat =="
kubectl get pods -o wide
echo -n "image: "; kubectl get pod -l app=docverify -o jsonpath='{.items[0].status.containerStatuses[0].image}{"\n"}'
echo -n "health: "; curl -fsS --max-time 5 http://localhost:8680/health && echo
