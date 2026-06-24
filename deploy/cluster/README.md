# Cluster docverify, shared-nothing

Mise à l'échelle sur plusieurs Macs. Bench DMR du 2026-06-05 : un pod DMR conteneurisé tombe en CPU et dépasse le délai sous rafale, donc le GPU Metal reste host-native.

## Principe

Chaque Mac est une unité autonome et identique :

```
                          ┌─ Mac principal ─────────────────────┐
                          │ k3d : docverify x2  ─┐                │
                          │                     └─► DMR Metal     │
Regia ─► LB de cluster ─────┤                        127.0.0.1:12434│
        (Caddy :8080)     │ ... idem ...                          │
                          ├─ Mac worker #1 ──────────────────────┤
                          ├─ Mac worker #2 ──────────────────────┤
                          └─ Mac worker #3 ──────────────────────┘
```

- Aucun appel LLM ne traverse le LAN : chaque docverify parle à son DMR local via la passerelle hôte `192.168.65.254:12434`. DMR reste sur `127.0.0.1`, pas d'exposition.
- Le LB ne répartit que le trafic HTTP entrant sur les `:8680` publiés par k3d.
- Le `Caddyfile` est statique : une ligne `to <IP>:8680` par Mac, IP DHCP-réservées. Ajout/retrait d'un nœud : éditer le `Caddyfile` puis `caddy reload`.
- Tolérance de panne : le health-check actif `/health` éjecte tout seul un Mac dont docverify ou le DMR est tombé.

## Mettre en service un Mac

L'image docverify vit sur le registre interne `alpha.srv.orbinea:5000`. Provisionner un Mac recrée son k3d avec la config registre puis applique le manifeste :

```bash
./deploy/cluster/provision-node-registry.sh
```

Outils requis : Docker Desktop, qui fournit docker et kubectl, plus k3d. Go n'est pas requis, le binaire est compilé dans l'image et tiré du registre.

Prérequis manuels, une fois par Mac :

1. Docker Desktop, Settings > General : cocher "Start Docker Desktop when you sign in". DMR démarre avec Docker Desktop, le modèle se charge à la première requête.
2. Auto-login de session, Réglages > Utilisateurs et groupes. Indispensable sur un Mac sans clavier : sans auto-login, un reboot reste sur l'écran de connexion, DMR et k3d éteints. Si FileVault est actif, l'auto-login est inopérant, le disque chiffré exige le mot de passe au boot.

## Survie au reboot

Vérifié sur reboot réel le 2026-06-05 : auto-login, login item Docker Desktop et restart policy des conteneurs k3d remontent la stack sans intervention. Le piège était le DNS : au reboot, `host.k3d.internal` disparaît de CoreDNS. La parade est de supprimer toute dépendance DNS du chemin LLM : `LLM_URL` pointe l'IP host-gateway fixe `http://192.168.65.254:12434/engines`. docverify n'a besoin d'aucun service in-cluster, il survit même si CoreDNS est mort.

Note : un redémarrage en place de Docker Desktop, distinct d'un reboot complet, peut faire perdre à k3d la publication du port API 6443. `k3d cluster start mini-cluster` republie le mapping.

## Lancer le Load Balancer Caddy

Front sur `:8080`, depuis la racine du repo :

```bash
caddy run --config deploy/cluster/Caddyfile
# ou en conteneur
docker run -d --name docverify-lb -p 8080:8080 \
  -v "$PWD/deploy/cluster/Caddyfile:/etc/caddy/Caddyfile" caddy:2
# recharger à chaud après édition
caddy reload --config deploy/cluster/Caddyfile
```

## Réglage de la concurrence

Concurrence locale vers un DMR = `replicas x QUEUE_WORKERS` dans `deploy/k8s/docverify-deployment.yaml`. À caler sur ce qu'un Mac encaisse en Metal sans dégrader la latence. À mesurer par Mac.
