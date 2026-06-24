# Cluster docverify, phase 2 shared-nothing

Architecture de mise a l'echelle sur plusieurs Macs, actee apres le bench DMR
CPU vs Metal du 2026-06-05 : un pod DMR conteneurise tombe en CPU et depasse le
délai d'exécution sous rafale, donc le GPU Metal reste host-native.

## Principe

Chaque Mac est une unite autonome et identique :

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

- Aucun appel LLM ne traverse le LAN : chaque docverify parle a son DMR local
  via la passerelle hôte `192.168.65.254:12434`. DMR reste sur `127.0.0.1`, pas d'exposition.
- Le LB ne repartit que le trafic HTTP entrant sur les `:8680` publies par k3d.
- **Mise à l'échelle** : le `Caddyfile` statique liste le cluster (une ligne `to <IP>:8680` par Mac, IP DHCP-réservées). Ajout/retrait d'un nœud : éditer le `Caddyfile` + `caddy reload`.
- Tolérance de panne : le health-check actif `/health` éjecte un Mac dont docverify ou
  le DMR local est tombé (aucune action manuelle requise).

## Mettre en service un Mac

Sur chaque Mac, depuis la racine du repo :

```bash
./deploy/cluster/provision-mac-node.sh
```

Le script installe k3d si absent dans ~/.local/bin sans sudo, gate l'activation
TCP de DMR sur sa joignabilité réelle, précharge le modèle, crée le cluster k3d,
build et déploie docverify, applique le fix CoreDNS, puis vérifie `/health`.
Les conteneurs `docverify` lancés répondent au health-check actif du LB Caddy sur `/health`.

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

## Lancer le Load Balancer Caddy

Le `Caddyfile` est **statique** : une ligne `to <IP>:8680` par Mac du cluster
(IP DHCP-réservées). Aucun registre, aucune génération automatique.

1. **Éditer `deploy/cluster/Caddyfile`** : ajouter/retirer une ligne `to <IP>:8680`
   par nœud.

2. **Lancer Caddy** sur le nœud front (`:8080`) :

       caddy run --config deploy/cluster/Caddyfile

   ou en conteneur :

       docker run -d --name docverify-lb -p 8080:8080 \
         -v "$PWD/deploy/cluster/Caddyfile:/etc/caddy/Caddyfile" caddy:2

3. **Recharger à chaud** après édition :

       caddy reload --config deploy/cluster/Caddyfile

Un Mac down ou au DMR hangé est éjecté automatiquement de la rotation par le
health-check actif `/health` (aucune action manuelle requise).

## Reglage de la concurrence

Concurrence locale vers un DMR = `replicas x QUEUE_WORKERS` dans
`deploy/k8s/docverify-deployment.yaml`. A caler sur ce qu'un Mac encaisse en
Metal sans degrader la latence. A mesurer par Mac avant de pousser les valeurs.


