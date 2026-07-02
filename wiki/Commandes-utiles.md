# Commandes utiles

## Logs

```bash
# Suivre les logs du déploiement
kubectl logs -f deploy/docverify

# Logs de tous les pods docverify, préfixés par nom de pod
kubectl logs -l app=docverify --prefix=true -f

# État du backend LLM Docker Model Runner, côté hôte
docker model status
docker model ls

# Logs du conteneur précédent, utile après un CrashLoopBackOff
kubectl logs -l app=docverify --previous
```

## Shell dans un pod

```bash
# Ouvrir un shell dans un pod docverify
kubectl exec -it deploy/docverify -- sh

# Vérifier que le backend LLM répond depuis le réseau du cluster
kubectl run dmrtest --rm -i --restart=Never --image=busybox:1.36 -- \
  wget -qO- http://192.168.65.254:12434/engines/v1/models
```

## Scale horizontal

```bash
kubectl scale deploy/docverify --replicas=3
kubectl get pods -w
```

Attention, le backend LLM n'est pas dans le cluster : Docker Model Runner tourne en process hôte et les pods l'atteignent par la passerelle fixe `192.168.65.254:12434`. Scaler docverify multiplie les workers de queue, pas la capacité GPU. Comme un GPU Metal ne traite qu'une inférence à la fois, garder `replicas × QUEUE_WORKERS` à 1 par Mac ; le débit supplémentaire vient de l'ajout de Macs.

## Rebuild après une modification Go

Le plus simple est `./scripts/redeploy.sh`, qui enchaîne le build, l'import et le rollout. La suite manuelle équivalente :

```bash
docker build -f deploy/Dockerfile -t docverify-docverify:latest .
k3d image import docverify-docverify:latest -c mini-cluster
kubectl rollout restart deploy/docverify
```

## Backend LLM Docker Model Runner

```bash
# Récupérer ou mettre à jour le modèle vision
docker model pull huggingface.co/unsloth/gemma-4-e4b-it-gguf:Q5_K_M

# Tester l'endpoint OpenAI-compatible côté hôte
curl -s http://localhost:12434/engines/v1/models

# État du runner
docker model status
```

## Test de charge et de contamination

```bash
# Rafale de requêtes simultanées, qui détecte toute fuite d'une requête à l'autre
./scripts/concurrency-test.sh 2
```

## Ménage

```bash
# Supprimer les ressources docverify
kubectl delete -f deploy/k8s/docverify-deployment.yaml

# Repartir de zéro en supprimant tout le cluster
k3d cluster delete mini-cluster
```
