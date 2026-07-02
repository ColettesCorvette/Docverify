# Scripts

Le dossier `scripts/` rassemble l'outillage de dev et d'exploitation. Cette page décrit chaque script.

## Configuration partagée, le .env

Les scripts partagent leurs paramètres via un `.env` à la racine :

```bash
cp .env.example .env
```

Les scripts qui en dépendent chargent ce `.env` au démarrage s'il existe, sinon ils retombent sur leurs défauts. Trois scripts autonomes ne le lisent pas : `monitor-cluster.sh`, `score-bench.sh` et `fetch-2ddoc-leaves.sh`. Le `.env` est ignoré par git, seul `.env.example` est versionné. On peut aussi surcharger une variable le temps d'une commande :

```bash
API=http://autre-hote:8680 ./scripts/test-api.sh all
```

## Tests et qualité

### test-api.sh

La suite de tests HTTP contre les samples, tous les cas ou un seul :

```bash
./scripts/test-api.sh all        # tous les cas
./scripts/test-api.sh freepro    # un seul sample
./scripts/test-api.sh async      # contrat webhook : 202 + job_id
```

Chaque appel synchrone affiche l'en-tête `X-Request-Id`, le verdict JSON, puis un ✓ ou ✗ selon le code HTTP. Le script sort en erreur si au moins un cas n'a pas répondu 200, ce qui le rend utilisable en CI. Le cas `async` envoie un `callback_url` et vérifie la réponse `202` avec `job_id` ; la livraison du callback, elle, est couverte côté Go par `TestPostCallback`.

### concurrency-test.sh

Envoie une rafale de requêtes simultanées portant chacune une signature unique, puis vérifie qu'aucune réponse ne contient la signature d'une autre. C'est le garde-fou anti-contamination entre requêtes, et la base du test de charge.

```bash
./scripts/concurrency-test.sh 3   # 3 tours, soit 12 requêtes simultanées
```

### bench-models.sh et score-bench.sh

`bench-models.sh` interroge docverify sur chaque sample en mesurant la latence et produit un JSONL. `score-bench.sh` compare ce JSONL à `samples/groundtruth.json` et sort la justesse champ par champ, plus les latences p50 et p95.

```bash
MODEL=huggingface.co/unsloth/gemma-4-e4b-it-gguf:Q5_K_M ./scripts/bench-models.sh > /tmp/bench.jsonl
./scripts/score-bench.sh /tmp/bench.jsonl
```

## Déploiement et exploitation

### redeploy.sh

La boucle de dev local. Reconstruit l'image, l'importe dans le k3d local et patche le Deployment vers cette image en `imagePullPolicy: Never`, sans passer par le registre. La boucle reste rapide et isolée, sans polluer le tag `:latest` que le cluster tire.

### monitor-cluster.sh

Le dashboard temps réel du cluster. Il extrait les nœuds du `Caddyfile` et interroge le `/health` enrichi de chaque nœud : status, LLM, workers, profondeur de queue, in-flight, documents traités, version, uptime. Il affiche aussi un agrégat et la répartition des requêtes par backend vue du Load Balancer. Rafraîchit en place toutes les 0.5 s.

```bash
./scripts/monitor-cluster.sh            # dashboard live
./scripts/monitor-cluster.sh 2          # refresh toutes les 2s
./scripts/monitor-cluster.sh --once     # snapshot + code retour, pour un CI
```

### deploy-cluster.sh

Le déploiement sur tout le cluster, via le registre interne. Build et push une seule fois, puis le script extrait les hôtes du `Caddyfile` et fait un `kubectl rollout restart` en SSH sur chaque nœud, qui re-tire l'image. Aucun `git pull` ni build côté nœud.

```bash
./scripts/deploy-cluster.sh
```

### bootstrap-node.sh

Intègre un Mac neuf dans le cluster, depuis le Mac de contrôle. Le script pousse la config registre dans le `~/.config/docverify` du nœud, installe k3d et kubectl, précharge le modèle DMR, lance `provision-node-registry.sh`, puis installe l'autostart WireGuard. Il termine en affichant la ligne `to <ip>:8680` à coller dans le `Caddyfile`. Tout le cluster partage le compte macOS `cluster`.

Les prérequis GUI non scriptables restent à faire à la main avant de le lancer : compte `cluster` avec auto-login, Docker Desktop en démarrage automatique, Connexion à distance, réservation DHCP, clés WireGuard dans `wg0.conf`. Le script vit dans `deploy/cluster/`.

```bash
./deploy/cluster/bootstrap-node.sh 10.40.1.13
```

### provision-node-registry.sh

Le cœur registre, appelé par `bootstrap-node.sh` mais utilisable seul. Il recrée le cluster k3d d'un Mac avec la config registre — CA, `registries.yaml`, `host-alias` — et applique le manifeste. À relancer sur un nœud dont le k3d a été créé avant la config actuelle. Vit dans `deploy/cluster/`.

### install-wg-autostart.sh

Installe uniquement l'autostart WireGuard sur un nœud déjà en service, pour ceux qui ont été intégrés avant que `bootstrap-node.sh` ne le fasse. Règle les trois pièges du boot : PATH launchd, signature AMFI, course DNS. Vit dans `deploy/cluster/`.

### fetch-2ddoc-leaves.sh

Rafraîchit les certificats feuilles 2D-DOC embarqués dans le binaire. Les cachets sont signés par les certificats feuilles des autorités, pas par les certificats d'AC de la TSL : le script lit les URLs d'annuaire dans la TSL embarquée, télécharge les feuilles de chaque autorité et les dépose dans `internal/docverify/assets/leaves/`, d'où le `go:embed` les prend au build suivant. La vérification de signature reste ainsi 100 % offline.

### push-wiki.sh

Synchronise les pages de `wiki/` vers le dépôt wiki Gitea, qui est un repo séparé du principal. Respecte le nommage des pages en ligne.

## Backend LLM côté hôte

Depuis le 2026-06-04, le backend LLM est Docker Model Runner, qui sert le modèle vision en process hôte sur GPU Metal. Le TCP host-side activé dans Docker Desktop le rend joignable depuis les pods via la passerelle hôte fixe `192.168.65.254:12434`.
