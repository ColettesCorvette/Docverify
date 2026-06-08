# docverify

![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)
![Kubernetes](https://img.shields.io/badge/Kubernetes-k3d-326CE5?logo=kubernetes&logoColor=white)
![Docker](https://img.shields.io/badge/Docker-Model%20Runner-2496ED?logo=docker&logoColor=white)

`docverify` est un service Go qui décide si un justificatif administratif est valide : factures, cartes d'identité, passeports, justificatifs de domicile. Il combine de l'OCR et un LLM multimodal, tourne sur Kubernetes, et il est appelé par le back-office Regia, qui lui transmet un document accompagné de l'identité et de l'adresse attendues.

La documentation détaillée vit dans le [wiki](https://git.int.orbinea.example/tfuchs/docverify/wiki/Home). Ce README donne la vue d'ensemble et le strict nécessaire pour développer en local.

## Comment ça marche

Regia envoie le document en POST sur l'API. Le serveur le dépose dans une queue interne, un pool de workers le traite, puis le fichier temporaire est supprimé et le verdict est renvoyé. La queue vit dans le processus de chaque pod : un simple channel Go alimenté par les requêtes et consommé par les workers. Elle borne ainsi le nombre d'appels simultanés au LLM sans dépendre d'un Redis.

Le traitement suit une cascade qui s'arrête dès qu'elle a de quoi décider. Si le document porte un cachet 2D-DOC, le Datamatrix signé des justificatifs de domicile français, docverify le lit directement et court-circuite tout le reste : l'identité, l'adresse et la date sortent des données signées, sans OCR ni LLM. Sa signature cryptographique est en plus vérifiée contre la liste des certificats des autorités ANTS embarquée dans le binaire, ce qui authentifie le cachet. Sinon le texte est extrait par `pdftotext` pour un PDF natif, ou par Tesseract pour une image à fond simple. Quand cette extraction est trop pauvre, ou que le fond est complexe comme sur un passeport, docverify bascule sur le LLM multimodal. En dernier recours, si la page semble contenir plusieurs documents, par exemple un recto et un verso de carte d'identité scannés ensemble, elle est découpée en zones traitées séparément. À chaque étape, un nouveau résultat n'est conservé que s'il extrait plus d'informations que le précédent.

Le verdict porte un niveau de confiance à quatre paliers, de `nul` à `haut`, qui reflète le chemin ayant produit le résultat : un 2D-DOC à signature vérifiée ou un OCR net donnent `haut`, le LLM plafonne à `moyen`, une rescousse par segmentation reste `bas`, un document illisible est `nul`. Regia s'en sert pour décider de l'automatisation : `haut` passe seul, `moyen` ou `bas` demandent une confirmation manuelle, `nul` est rejeté.

Le LLM est servi par Docker Model Runner, qui exécute le modèle vision `Gemma 4 E4B` au format GGUF sur le GPU Metal du Mac, en process hôte. Les pods l'atteignent par son endpoint OpenAI-compatible via `host.k3d.internal`. Côté code, docverify ne connaît qu'une URL OpenAI-compatible et un nom de modèle : on peut donc remplacer le backend sans toucher au service.

## Architecture

```
┌─────────────────────────── Host Mac ───────────────────────────────┐
│  Docker Desktop                                                     │
│    ├─ k3d "mini-cluster"                                            │
│    │    docverify-pod-1 ┐                                            │
│    │    docverify-pod-2 ┼──→  host.k3d.internal:12434/engines        │
│    │                                                  │             │
│    └─ Docker Model Runner  ◄──────────────────────────┘             │
│              │ llama.cpp · GPU Metal · process hôte                 │
│              ▼                                                      │
│         Gemma 4 E4B (GGUF)                                          │
└─────────────────────────────────────────────────────────────────────┘
```

Une requête suit le chemin : Regia → pod docverify → Docker Model Runner sur l'hôte → Gemma 4 E4B.

## Briques externes

| Projet | Rôle |
|---|---|
| [Docker Model Runner](https://docs.docker.com/ai/model-runner/) | Sert un modèle GGUF en endpoint OpenAI-compatible sur GPU Metal, en process hôte |
| [Gemma 4 E4B](https://huggingface.co/unsloth/gemma-4-e4b-it-gguf) | Modèle vision-langage servi par défaut, empaqueté `hf.co/unsloth/gemma-4-e4b-it-gguf:Q8_0` |
| [k3d](https://k3d.io) | k3s en conteneurs Docker pour le cluster local |

## Développer et tester en local

docverify est un projet Go pur, sans dépendance Go externe. Il a en revanche besoin de quelques outils système et d'un endpoint LLM joignable.

```bash
brew install go tesseract tesseract-lang poppler imagemagick dmtx-utils jq
go build -o docverify ./cmd/docverify
```

Le même binaire a trois modes. Sans argument, il démarre le serveur HTTP sur `:8680`, et l'URL du LLM se règle par la variable `LLM_URL`. Avec un fichier ou du JSON en argument, il analyse un document unique et l'affiche en tableau. Si l'entrée contient une liste `individuals`, il bascule en mode batch sur plusieurs personnes.

```bash
LLM_URL=http://localhost:12434/engines ./docverify     # serveur HTTP
./docverify input.json                        # un seul document
./scripts/test-api.sh all                    # suite de tests sur les samples
```

La route, ses champs et le mode webhook sont décrits dans le wiki, page [API HTTP](https://git.int.orbinea.example/tfuchs/docverify/wiki/API-HTTP).

## Déployer

Le backend LLM est Docker Model Runner, activé dans Docker Desktop avec le support TCP host-side. On récupère le modèle une fois, puis le déploiement tient en un manifeste et un script.

```bash
docker model pull hf.co/unsloth/gemma-4-e4b-it-gguf:Q8_0   # backend LLM, une fois
kubectl apply -f deploy/k8s/docverify-deployment.yaml   # service docverify et ses replicas
./scripts/redeploy.sh                                  # rebuild, import et rollout après une modif Go
```

Les pods atteignent le runner via `host.k3d.internal:12434`. Pour servir le LLM depuis une machine Linux à GPU plutôt que depuis le runner hôte, `deploy/llm/` fournit des images vLLM préparées par accélérateur, CUDA, ROCm, Intel Arc ou CPU, avec leur script de build et un manifeste de déploiement. Voir `deploy/llm/README.md`.

## Documentation

| Page | Contenu |
|---|---|
| [API HTTP](https://git.int.orbinea.example/tfuchs/docverify/wiki/API-HTTP) | Route, champs multipart, réponses, mode webhook, codes d'erreur |
| [Scripts](https://git.int.orbinea.example/tfuchs/docverify/wiki/Scripts) | Détail de l'outillage et de la configuration par `.env` |
| [Commandes utiles](https://git.int.orbinea.example/tfuchs/docverify/wiki/Commandes-utiles) | Logs, shell dans un pod, scale, gestion du backend LLM |
| [Dashboard](https://git.int.orbinea.example/tfuchs/docverify/wiki/Dashboard) | Dashboard Kubernetes, installation et accès |

## Scripts

Le dossier `scripts/` regroupe l'outillage de développement et d'exploitation. Les paramètres communs comme l'URL de l'API, le modèle, le chemin des samples ou le cluster se centralisent dans un `.env` à la racine : copie `.env.example` en `.env` et ajuste. Chaque script charge ce `.env` s'il existe, sinon il retombe sur ses défauts.

| Script | Rôle |
|---|---|
| `test-api.sh` | Suite de tests HTTP contre les samples, un cas ou tous |
| `concurrency-test.sh` | Rafale parallèle, garde anti-régression de la contamination |
| `bench-models.sh` + `score-bench.sh` | Bench multi-modèles et scoring contre le groundtruth |
| `redeploy.sh` | Rebuild image, import k3d et rollout docverify |
| `push-wiki.sh` | Pousse `wiki/` vers le wiki Gitea |

Pour servir le LLM hors macOS, les images vLLM par accélérateur vivent dans `deploy/llm/` avec leur `build.sh` ; elles remplacent l'ancien `gen-llm-backend.sh`, conservé mais obsolète.

Le détail de chaque script et de sa configuration est sur la page [Scripts](https://git.int.orbinea.example/tfuchs/docverify/wiki/Scripts) du wiki.

## Machines

Le développement se fait sur un Mac Mini M4 Pro, 12 cœurs et 48 Go de mémoire unifiée. Trois Mac Mini supplémentaires, mêmes M4 Pro 12 cœurs avec 24 Go chacun, répartissent la charge du LLM sur plusieurs backends.

La mise à l'échelle multi-Mac évolue vers un vrai cluster Kubernetes à plusieurs nœuds : chaque Mac héberge un nœud k3s dans une VM Linux légère bridgée sur le LAN, les nœuds rejoignent automatiquement le serveur par token, et le DMR Metal reste host-natif sur chaque Mac. Cette approche a été validée en spike sur deux Macs ; son industrialisation est en cours. Une machine Linux à GPU peut rejoindre le même cluster nativement, sans VM, et y servir le LLM via vLLM. Le déploiement de production reste pour l'instant un k3d autonome par Mac.
