# docverify

![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)
![Kubernetes](https://img.shields.io/badge/Kubernetes-k3d-326CE5?logo=kubernetes&logoColor=white)
![Docker](https://img.shields.io/badge/Docker-Model%20Runner-2496ED?logo=docker&logoColor=white)

`docverify` est un service Go qui décide si un justificatif administratif est valide : factures, cartes d'identité, passeports, justificatifs de domicile. Il combine OCR et LLM multimodal, tourne sur Kubernetes, et il est appelé par le back-office Regia qui lui transmet un document avec l'identité et l'adresse attendues.

Doc détaillée dans le [wiki](https://git.int.orbinea.example/tfuchs/docverify/wiki/Home). Ce README donne la vue d'ensemble et le nécessaire pour développer en local.

## Comment ça marche

Regia POSTe le document sur l'API. Le serveur le dépose dans une queue Go interne au pod, un pool de workers le traite, le fichier temporaire est supprimé, le verdict renvoyé. La queue borne le nombre d'appels LLM simultanés sans dépendre d'un Redis.

Le traitement est une cascade qui s'arrête dès qu'elle peut décider. Un cachet 2D-DOC, le Datamatrix signé des justificatifs FR, court-circuite tout : identité, adresse et date sortent des données signées, sans OCR ni LLM, et la signature est vérifiée contre les certificats ANTS embarqués dans le binaire. Sinon le texte est extrait par `pdftotext` pour un PDF natif, ou Tesseract pour une image à fond simple. Extraction trop pauvre ou fond complexe, docverify bascule sur le LLM multimodal. En dernier recours la page est découpée en zones traitées séparément. À chaque étape un résultat n'est gardé que s'il extrait plus que le précédent.

Le verdict porte un niveau de confiance à quatre paliers, de `nul` à `haut`, selon le chemin : 2D-DOC vérifié ou OCR net donnent `haut`, le LLM plafonne à `moyen`, la segmentation reste `bas`, illisible vaut `nul`. Regia automatise sur `haut`, demande confirmation sur `moyen`/`bas`, rejette `nul`.

Le LLM est servi par Docker Model Runner : modèle vision `Gemma 4 E4B` GGUF sur GPU Metal, en process hôte. Les pods l'atteignent via la passerelle hôte fixe `192.168.65.254:12434`, ce qui évite les pannes DNS au redémarrage de k3d. Le code ne connaît qu'une URL OpenAI-compatible et un nom de modèle : le backend se remplace sans toucher au service.

## Architecture

### Développement local, k3d mono-nœud
Les pods `docverify` tournent dans un cluster `k3d` local et atteignent le **Docker Model Runner** sur l'hôte pour le GPU Metal :

<img src="assets/pipeline_cascade.svg" width="650" alt="Architecture locale" />

Chemin d'une requête : **Regia** &rarr; **pod docverify** &rarr; **Docker Model Runner hôte** &rarr; **Gemma 4 E4B**.

### Production, cluster multi-nœuds Shared-Nothing
Le cluster regroupe plusieurs Mac Minis autonomes, Mac principal inclus qui sert aussi de worker. Chaque Mac a son cluster `k3d` local et son DMR. L'image docverify est buildée une fois et poussée sur le **registre interne** `alpha.srv.orbinea:5000` ; chaque k3d la pull. Un Load Balancer **Caddy** statique, une ligne `to <IP>:8680` par Mac, répartit les requêtes ; un nœud HS sort tout seul de la rotation via le **health-check** `/health` :

<img src="assets/infrastructure_production.svg" width="650" alt="Infrastructure du cluster" />

## Briques externes

| Projet | Rôle |
|---|---|
| [Docker Model Runner](https://docs.docker.com/ai/model-runner/) | Sert un GGUF en endpoint OpenAI-compatible sur GPU Metal, en process hôte |
| [Gemma 4 E4B](https://huggingface.co/unsloth/gemma-4-e4b-it-gguf) | Modèle vision-langage par défaut, `hf.co/unsloth/gemma-4-e4b-it-gguf:Q4_K_M` |
| [k3d](https://k3d.io) | k3s en conteneurs Docker pour le cluster local |

## Développer et tester en local

Projet Go pur, sans dépendance Go externe. Besoin d'outils système et d'un endpoint LLM joignable.

```bash
brew install go tesseract tesseract-lang poppler imagemagick dmtx-utils jq
go build -o docverify ./cmd/docverify
```

Le binaire a trois modes. Sans argument il démarre le serveur HTTP sur `:8680`, l'URL du LLM se règle par `LLM_URL`. Avec un fichier ou du JSON il analyse un document unique. Si l'entrée contient une liste `individuals`, il passe en mode batch.

```bash
LLM_URL=http://localhost:12434/engines ./docverify     # serveur HTTP
./docverify input.json                        # un seul document
./scripts/test-api.sh all                    # suite de tests sur les samples
```

Route, champs et mode webhook sur la page wiki [API HTTP](https://git.int.orbinea.example/tfuchs/docverify/wiki/API-HTTP).

## Déployer

Backend LLM = Docker Model Runner, activé dans Docker Desktop avec le TCP host-side. On pull le modèle une fois par Mac, puis deux chemins selon la cible.

```bash
docker model pull hf.co/unsloth/gemma-4-e4b-it-gguf:Q4_K_M   # backend LLM, une fois par Mac

./scripts/redeploy.sh         # DEV LOCAL : build + import k3d + override local, sans registre
./scripts/deploy-cluster.sh   # CLUSTER : build + push registre, puis rollout de chaque nœud
```

Le manifeste `deploy/k8s/docverify-deployment.yaml` cible l'image du registre interne en `imagePullPolicy: Always`. Un Mac neuf s'onboarde depuis le contrôle avec `deploy/cluster/bootstrap-node.sh <ip>`, qui pousse la config, installe les outils, provisionne le k3d et affiche la ligne à coller au `Caddyfile`. Pour servir le LLM depuis une machine Linux à GPU, `deploy/llm/` fournit des images vLLM par accélérateur. Voir `deploy/llm/README.md`.

## Documentation

| Page | Contenu |
|---|---|
| [API HTTP](https://git.int.orbinea.example/tfuchs/docverify/wiki/API-HTTP) | Route, champs multipart, réponses, webhook, codes d'erreur |
| [Scripts](https://git.int.orbinea.example/tfuchs/docverify/wiki/Scripts) | Outillage et configuration par `.env` |
| [Commandes utiles](https://git.int.orbinea.example/tfuchs/docverify/wiki/Commandes-utiles) | Logs, shell dans un pod, scale, backend LLM |
| [Dashboard](https://git.int.orbinea.example/tfuchs/docverify/wiki/Dashboard) | Dashboard Kubernetes, installation et accès |

## Scripts

Le dossier `scripts/` regroupe l'outillage de dev et d'exploitation. Les paramètres communs se centralisent dans un `.env` racine : copie `.env.example` en `.env` et ajuste.

| Script | Rôle |
|---|---|
| `test-api.sh` | Tests HTTP contre les samples, un cas ou tous |
| `concurrency-test.sh` | Rafale parallèle, garde anti-contamination |
| `bench-models.sh` + `score-bench.sh` | Bench multi-modèles et scoring |
| `redeploy.sh` | Dev local : build + import k3d + override local, sans registre |
| `deploy-cluster.sh` | Cluster : build + push registre, rollout de chaque nœud via SSH |
| `monitor-cluster.sh` | Dashboard temps réel du cluster, `/health` enrichi |
| `bootstrap-node.sh` | Onboarde un Mac neuf depuis le contrôle : config, outils, k3d |
| `provision-node-registry.sh` | Cœur registre : recrée le k3d d'un Mac pour pull du registre |
| `push-wiki.sh` | Pousse `wiki/` vers le wiki Gitea |

## Machines

Quatre Mac Mini M4 Pro 12 cœurs en shared-nothing, un nœud k3d autonome et un DMR local par machine. Le principal, 48 Go, sert de poste de dev, de front Load Balancer et de worker. Trois Mac de 24 Go complètent le cluster.

Pour tenir dans 24 Go, le modèle de production est quantisé en `Q4_K_M`, 4.62 GiB, ce qui libère 3 Go de VRAM par nœud à 100% de justesse au benchmark. Le découplage du cycle de vie des tâches, timeout d'attente court en file et timeout d'exécution garanti pour le worker, encaisse les rafales intenses sans crash ni timeout.
