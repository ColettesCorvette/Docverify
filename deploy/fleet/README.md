# Flotte docverify, phase 2 shared-nothing

Architecture de mise a l'echelle sur plusieurs Macs, actee apres le bench DMR
CPU vs Metal du 2026-06-05 : un pod DMR conteneurise tombe en CPU et depasse le
délai d'exécution sous rafale, donc le GPU Metal reste host-native.

## Principe

Chaque Mac est une unite autonome et identique :

```
                          ┌─ Mac principal ─────────────────────┐
                          │ k3d : docverify x2  ─┐                │
                          │                     └─► DMR Metal     │
Regia ─► LB de flotte ─────┤                        127.0.0.1:12434│
        (Caddy :8680)     │ ... idem ...                          │
                          ├─ Mac worker #1 ──────────────────────┤
                          ├─ Mac worker #2 ──────────────────────┤
                          └─ Mac worker #3 ──────────────────────┘
```

- Aucun appel LLM ne traverse le LAN : chaque docverify parle a son DMR local
  via `host.k3d.internal:12434`. DMR reste sur `127.0.0.1`, pas d'exposition.
- Le LB ne repartit que le trafic HTTP entrant sur les `:8680` publies par k3d.
- **Mise a l'echelle dynamique** : Les nœuds (Macs et Linux) s'auto-enregistrent dynamiquement auprès du registre central (inclus dans `./docverify --registry`) qui gère et actualise à chaud les backends du `Caddyfile` de flotte sans modification manuelle.
- Tolerance de panne : le health-check `/health` ejecte un Mac dont docverify ou
  le DMR local est tombe. Le registre central nettoie périodiquement (toutes les 90s) les nœuds inactifs.

## Mettre en service un Mac

Sur chaque Mac, depuis la racine du repo :

```bash
./deploy/fleet/provision-mac-node.sh
```

Le script installe k3d si absent dans ~/.local/bin sans sudo, gate l'activation
TCP de DMR sur sa joignabilité réelle, précharge le modèle, crée le cluster k3d,
build et déploie docverify, applique le fix CoreDNS, puis vérifie `/health`.
Les conteneurs `docverify` lancés s'enregistrent automatiquement auprès du registre central configuré par la variable d'environnement `REGISTRY_URL`.

Outils requis sur le Mac : Docker Desktop seul suffit, il fournit docker et
kubectl. k3d est installe par le script. Go n'est pas requis, le binaire est
compile dans l'image multi-stage. Pas besoin de Homebrew. Le repo doit etre
present sur le Mac, par git clone ou rsync depuis un poste de reference.

Prerequis manuels, a faire une fois par Mac :

1. Docker Desktop, Settings > General : cocher "Start Docker Desktop when you
   sign in". DMR demarre avec Docker Desktop, le modele se charge a la premiere
   requete. C'est ce qui remplace tout LaunchAgent.

2. Auto-login de session, Reglages Systeme > Utilisateurs et groupes > "Ouvrir
   une session automatiquement". Indispensable sur un Mac sans clavier : le login
   item Docker Desktop ne se declenche qu'APRES ouverture de session. Sans
   auto-login, un Mac qui reboot reste sur l'ecran de connexion, DMR et k3d
   eteints.

   Attention FileVault : s'il est actif, l'auto-login est inoperant. Le disque
   chiffre exige le mot de passe au boot, et c'est ce deverrouillage qui ouvre la
   session. Pour un boot autonome apres coupure, soit FileVault off sur ces
   noeuds workers, soit on accepte une saisie manuelle au redemarrage.

## Survie au reboot

Verifie sur reboot reel de deux Macs le 2026-06-05 : auto-login + login item
Docker Desktop + restart policy des conteneurs k3d remontent la stack sans
intervention. Le piege etait le DNS du cluster. Au reboot, host.k3d.internal
disparait de CoreDNS, et selon les Macs CoreDNS lui-meme ne redemarre pas, sa
configmap perdant la cle NodeHosts, donc tout le DNS in-cluster tombe et chaque
analyse renvoyait 500.

Solution retenue : supprimer la dependance DNS du chemin LLM. Le deploiement
pointe LLM_URL sur l'IP host-gateway fixe de Docker Desktop,
http://192.168.65.254:12434/engines, au lieu de host.k3d.internal. docverify
n'a besoin d'aucun autre service in-cluster, donc il reste operationnel au reboot
meme si CoreDNS est mort, sans LaunchAgent. Valide bout en bout sur les deux Macs.

Note : CoreDNS peut rester en echec apres reboot sur certains clusters k3d, bug
connu de la cle NodeHosts. Ca n'affecte plus docverify, mais a surveiller si tu
ajoutes des composants qui dependent du DNS in-cluster.

Note mineure : un redemarrage en place de Docker Desktop, distinct d'un reboot
complet, a deja fait perdre la publication du port API 6443 a k3d. Un reboot
complet l'a republie correctement. Si kubectl devient injoignable hors reboot,
`k3d cluster start mini-cluster` republie le mapping.

## Lancer le Registre et le Load Balancer Caddy

Le Load Balancer Caddy est configuré dynamiquement par le registre de flotte.

1. **Démarrer le registre sécurisé** (sur le nœud principal / LB) :
   ```bash
   DOCVERIFY_REGISTRY_TOKEN="votre_secret_token" ./docverify --registry 8681
   ```

   Ce service écoute sur le port `:8681` et se charge de réécrire le fichier `deploy/fleet/Caddyfile` à partir du template `deploy/fleet/Caddyfile.template` à chaque auto-enregistrement de nœud.

2. **Démarrer Caddy** (sur le nœud principal / LB) :
   ```bash
   caddy run --config deploy/fleet/Caddyfile
   # ou via Docker :
   docker run -d --name docverify-lb --network host \
     -v "$PWD/deploy/fleet/Caddyfile:/etc/caddy/Caddyfile" caddy:2
   ```

   Caddy rechargera sa configuration automatiquement à chaque modification du fichier généré par le registre.

3. **Variables d'environnement du registre** :
   Les variables suivantes permettent de configurer le registre et le client d'enregistrement :
   - `REGISTRY_URL` : URL du registre à contacter par le client heartbeat (ex: `http://192.168.40.10:8681`).
   - `NODE_IP` : IP locale du nœud à déclarer au registre.
   - `DOCVERIFY_REGISTRY_TOKEN` : Clé secrète partagée optionnelle pour authentifier l'enregistrement (rejet HTTP 401 si invalide).
   - `DOCVERIFY_CADDY_TEMPLATE` : Chemin vers le template Caddyfile (défaut: `deploy/fleet/Caddyfile.template`).
   - `DOCVERIFY_CADDY_OUTPUT` : Chemin vers le Caddyfile de sortie (défaut: `deploy/fleet/Caddyfile`).

## Reglage de la concurrence

Concurrence locale vers un DMR = `replicas x QUEUE_WORKERS` dans
`deploy/k8s/docverify-deployment.yaml`. A caler sur ce qu'un Mac encaisse en
Metal sans degrader la latence. A mesurer par Mac avant de pousser les valeurs.

## Chemin A — vrai cluster K8s multi-noeuds (en cours d'industrialisation)

Approche retenue au-dela du shared-nothing : un seul cluster Kubernetes qui
s'etend sur les Macs, plutot que N clusters autonomes. Un noeud k3s par Mac dans
une VM Linux lima bridgee sur le LAN, auto-join par token, etcd. macOS ne fait
pas tourner k3s nativement, d'ou la VM ; le DMR Metal reste host-natif. k3d ne
convient pas en multi-hote, ses noeuds sont derriere le NAT Docker par Mac.

Provisionner un Mac-noeud (recette validee en spike) :

```bash
ROLE=server ./deploy/fleet/provision-mac-k3s.sh                       # 1er Mac
ROLE=agent SERVER_IP=192.168.30.25 TOKEN=K10... \                    # Macs suivants
  ./deploy/fleet/provision-mac-k3s.sh
```

Le script installe lima, configure le reseau bridge et k3s, et s'arrete en
indiquant la commande sudo exacte si un prerequis root manque, socket_vmnet ou
sudoers. Il active aussi l'autostart au login via `limactl start-at-login`.

docverify tourne en **DaemonSet hostNetwork** (`docverify-daemonset.yaml`) : un pod
par noeud, chacun parlant a son DMR Metal local via `host.lima.internal:12434`.
Un pod normal n'atteint pas le DMR de l'hote, hostNetwork si. L'image est
distribuee dans le containerd de chaque noeud, k3s n'a pas de `k3d image import` :

```bash
docker save docverify-docverify:latest -o ~/docverify.tar          # sur le Mac de build
scp ~/docverify.tar <mac>:~/docverify.tar                          # vers chaque worker
limactl shell <vm> -- sudo k3s ctr images import /Users/<user>/docverify.tar
```

Le kubeconfig vise l'IP LAN du serveur, pas le loopback hote, pour ne pas entrer
en collision avec k3d tant que les deux coexistent :

```bash
limactl shell k3s-spike -- sudo cat /etc/rancher/k3s/k3s.yaml \
  | sed 's#https://127.0.0.1:6443#https://192.168.30.25:6443#' > ~/.kube/lima-k3s.yaml
KUBECONFIG=~/.kube/lima-k3s.yaml kubectl apply -f deploy/fleet/docverify-daemonset.yaml
```

Chaque noeud sert l'API sur son IP LAN `:8680` ; le LB de flotte les cible. Une
machine Linux a GPU rejoint le meme cluster nativement, sans VM, et sert le LLM
via vLLM (`deploy/llm/`).

Etat : valide en spike sur 2 Macs. Reste a finir : reservations DHCP des IP de
VM, bascule depuis k3d puis decommission, et un sample 2D-DOC lisible pour
bencher la verification de signature de bout en bout.
