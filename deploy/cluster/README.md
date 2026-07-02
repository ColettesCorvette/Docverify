# Cluster docverify, shared-nothing

Cette partie couvre la mise à l'échelle sur plusieurs Macs. Le choix de garder le GPU Metal en process hôte vient du bench DMR du 2026-06-05 : un DMR conteneurisé retombe en CPU et dépasse les délais sous rafale, donc pas de LLM dans les pods.

## Principe

Chaque Mac est une unité autonome et identique :

```
                          ┌─ Mac principal ─────────────────────┐
                          │ k3d : docverify  ─┐                   │
                          │                  └─► DMR Metal        │
Regia ─► LB de cluster ─────┤                     127.0.0.1:12434  │
        (Caddy :8080)     │ ... idem ...                          │
                          ├─ Mac worker #1 ──────────────────────┤
                          ├─ Mac worker #2 ──────────────────────┤
                          └─ Mac worker #3 ──────────────────────┘
```

- Aucun appel LLM ne traverse le réseau : chaque docverify parle à son DMR local via la passerelle hôte `192.168.65.254:12434`. Le DMR reste sur `127.0.0.1`, il n'est jamais exposé.
- Le LB ne répartit que le trafic HTTP entrant, vers les ports `:8680` publiés par chaque k3d.
- Le `Caddyfile` est statique : une ligne `to <IP>:8680` par Mac, avec des IP réservées en DHCP. Pour ajouter ou retirer un nœud, on édite le `Caddyfile` puis `caddy reload`.
- La tolérance de panne est gratuite : le health-check actif sur `/health` éjecte tout seul un Mac dont docverify ou le DMR est tombé, et le réintègre quand il revient.

## Mettre en service un Mac

L'image docverify vit sur le registre interne `alpha.srv.orbinea:5000`. Un Mac neuf s'intègre depuis le Mac de contrôle :

```bash
./deploy/cluster/bootstrap-node.sh <ip-du-noeud>
```

Le script pousse la config registre dans le `~/.config/docverify` du nœud, installe k3d et kubectl, précharge le modèle DMR, puis lance `provision-node-registry.sh` qui recrée le k3d avec la config registre et applique le manifeste. Il termine en affichant la ligne `to <ip>:8680` à coller dans le `Caddyfile`. Tous les Macs du cluster utilisent le même compte macOS, nommé `cluster` : c'est lui qui fait tourner Docker Desktop, et c'est donc lui qu'on utilise en SSH.

Côté outils, il faut Docker Desktop, qui apporte docker et kubectl, plus k3d. Go n'est pas nécessaire sur les nœuds : le binaire est compilé dans l'image et tiré du registre.

Certains prérequis ne sont pas scriptables et se font à la main, une fois par Mac, avant de lancer `bootstrap-node.sh` :

1. Compte macOS primaire nommé `cluster`, avec l'auto-login activé. Sans auto-login, un Mac sans clavier reste bloqué sur l'écran de connexion après un reboot, avec DMR et k3d éteints. Attention aussi à FileVault : il faut le désactiver, sinon le disque chiffré demande le mot de passe au boot et l'auto-login ne sert à rien.
2. Docker Desktop installé, avec « Start Docker Desktop when you sign in » coché dans Settings > General. Le DMR démarre avec Docker Desktop et le modèle se charge à la première requête.
3. Partage > Connexion à distance activé, pour la clé SSH du Mac de contrôle, et IP réservée en DHCP côté routeur.
4. WireGuard : `brew install wireguard-tools`, puis le fichier `/opt/homebrew/etc/wireguard/wg0.conf` provisionné avec les clés du nœud. Le script rend ensuite le tunnel persistant au reboot.

Pour un nœud déjà en service qui n'a pas encore l'autostart WireGuard, `install-wg-autostart.sh` installe juste cette partie.

## Survie au reboot

Vérifié sur un reboot réel le 2026-06-05 : l'auto-login, le login item Docker Desktop et la restart policy des conteneurs k3d suffisent à remonter toute la stack sans intervention. Le piège était le DNS : après un reboot, `host.k3d.internal` disparaît de CoreDNS. La parade a été de supprimer toute dépendance DNS du chemin LLM : `LLM_URL` pointe l'IP host-gateway fixe `http://192.168.65.254:12434/engines`. Comme docverify n'a besoin d'aucun service in-cluster, il survit même si CoreDNS est mort.

À savoir : un redémarrage de Docker Desktop seul, sans reboot complet, peut faire perdre à k3d la publication du port API 6443. Dans ce cas, `k3d cluster start mini-cluster` republie le mapping.

Le tunnel WireGuard, lui, ne remonte pas tout seul avec un simple `wg-quick` lancé à la main. `bootstrap-node.sh` installe un LaunchDaemon qui s'en charge au boot, avec trois pièges déjà réglés : le PATH minimal de launchd qui ne voit pas `/opt/homebrew/bin`, AMFI qui bloque les binaires Homebrew non signés, et la course entre le boot et la résolution DNS de l'endpoint.

## Lancer le Load Balancer Caddy

Le front écoute sur `:8080`. Depuis la racine du repo :

```bash
caddy run --config deploy/cluster/Caddyfile
# ou en conteneur, avec redémarrage automatique au reboot
docker run -d --name docverify-lb --restart unless-stopped -p 8080:8080 \
  -v "$PWD/deploy/cluster/Caddyfile:/etc/caddy/Caddyfile" caddy:2
# recharger à chaud après édition
caddy reload --config deploy/cluster/Caddyfile
```

Le `--restart unless-stopped` compte : avec la policy par défaut, le conteneur LB ne survit pas au reboot du Mac principal.

## Réglage de la concurrence

Réglé une fois pour toutes par le bench du 2026-06-25 : un GPU Metal ne traite qu'une inférence à la fois. Une facture image prend 9 s seule, mais trois requêtes simultanées avec trois workers prennent 137 s chacune, soit quinze fois pire, parce que le GPU thrash. Avec un seul worker les trois mêmes requêtes sortent en 9, 19 et 28 s, proprement sérialisées.

D'où la règle : `QUEUE_WORKERS=1` et `LLM_MAX_CONCURRENT=1` par Mac, valeurs par défaut du manifeste et du code. Pour plus de débit, on ajoute des Macs, pas des workers sur le même GPU.
