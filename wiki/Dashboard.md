# Dashboard Kubernetes

## Installation, une fois par cluster

```bash
# Composants officiels du dashboard
kubectl apply -f https://raw.githubusercontent.com/kubernetes/dashboard/v2.7.0/aio/deploy/recommended.yaml

# Compte admin, liaison de rôle, secret de token et service d'accès sur le port 8001
kubectl apply -f deploy/k8s/dashboard-token.yaml
```

Le fichier `dashboard-token.yaml` regroupe quatre ressources : un `ServiceAccount` nommé `admin-user` dans le namespace `kubernetes-dashboard`, un `ClusterRoleBinding` qui lui donne le rôle `cluster-admin`, un `Secret` qui porte un token persistant généré automatiquement, et un `Service` LoadBalancer qui expose le dashboard sur le port 8001.

## Récupérer le token

```bash
# Token temporaire, valable une heure
kubectl -n kubernetes-dashboard create token admin-user

# Token persistant, lu depuis le Secret
kubectl -n kubernetes-dashboard get secret admin-user-token \
  -o jsonpath='{.data.token}' | base64 -d && echo
```

## Accès

Ouvrir `https://localhost:8001`, accepter le certificat auto-signé, puis coller le token dans le formulaire de connexion.

## Dépannage

Si `https://localhost:8001` ne répond pas, vérifier le service :

```bash
kubectl -n kubernetes-dashboard get svc kubernetes-dashboard-lb
```

Son `EXTERNAL-IP` doit afficher une IP du bridge k3d, par exemple `172.19.0.2`. Un état `<pending>` signifie que le port 8001 n'a pas été mappé à la création du cluster ; il faut alors le recréer en exposant ce port :

```bash
k3d cluster create mini-cluster \
  --api-port 6443 \
  -p "8680:8680@loadbalancer" \
  -p "8001:8001@loadbalancer"
```
