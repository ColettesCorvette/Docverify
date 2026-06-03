# docverify

![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)
![Kubernetes](https://img.shields.io/badge/Kubernetes-k3d-326CE5?logo=kubernetes&logoColor=white)
![macOS](https://img.shields.io/badge/macOS-launchd-000000?logo=apple&logoColor=white)

`docverify` est un service Go qui décide si un justificatif administratif est valide : factures, cartes d'identité, passeports, justificatifs de domicile. Il combine de l'OCR et un LLM multimodal, tourne sur Kubernetes, et il est appelé par le back-office Regia, qui lui transmet un document accompagné de l'identité et de l'adresse attendues.

La documentation détaillée vit dans le [wiki](https://git.int.orbinea.example/tfuchs/docverify/wiki/Home). Ce README donne la vue d'ensemble et le strict nécessaire pour développer en local.

## Comment ça marche

Regia envoie le document en POST sur l'API. Le serveur le dépose dans une queue interne, un pool de workers le traite, puis le fichier temporaire est supprimé et le verdict est renvoyé. La queue vit dans le processus de chaque pod : un simple channel Go alimenté par les requêtes et consommé par les workers. Elle borne ainsi le nombre d'appels simultanés au LLM sans dépendre d'un Redis.

Le traitement suit une cascade qui s'arrête dès qu'elle a de quoi décider. Si le document porte un cachet 2D-DOC, le Datamatrix signé des justificatifs de domicile français, docverify le lit directement et court-circuite tout le reste : l'identité, l'adresse et la date sortent des données signées, sans OCR ni LLM. Sinon le texte est extrait par `pdftotext` pour un PDF natif, ou par Tesseract pour une image à fond simple. Quand cette extraction est trop pauvre, ou que le fond est complexe comme sur un passeport, docverify bascule sur le LLM multimodal. En dernier recours, si la page semble contenir plusieurs documents, par exemple un recto et un verso de carte d'identité scannés ensemble, elle est découpée en zones traitées séparément. À chaque étape, un nouveau résultat n'est conservé que s'il extrait plus d'informations que le précédent.

Le LLM est `vllm-mlx`, qui sert le modèle `Qwen2.5-VL-7B-Instruct-4bit`. Il ne tourne pas à côté du service mais dans une VM macOS pilotée par Kubernetes. Le Mac s'enregistre dans le cluster comme un node `darwin/arm64` grâce à [macOS-vz-kubelet](https://github.com/agoda-com/macOS-vz-kubelet), Kubernetes y démarre la VM à partir d'une image OCI, et un service interne `vllm-mlx` l'expose aux pods docverify. Côté code, docverify ne connaît qu'une URL OpenAI-compatible : on peut donc remplacer le backend macOS par un backend Linux à GPU sans toucher au service.

## Architecture

```
┌─────────────────────────── Host Mac ───────────────────────────────┐
│  macos-vz-kubelet  →  enregistre le node virtuel "macos-vz-mini"    │
│                                                                     │
│  Colima → k3d "mini-cluster"                                        │
│    ├─ node Linux                                                    │
│    │    docverify-pod-1 ┐                                            │
│    │    docverify-pod-2 ┼──→  Service vllm-mlx:8000                  │
│    ├─ k3d-registry  OCI :5000                                       │
│    └─ node Darwin "macos-vz-mini"                                   │
│         pod vllm-mlx  ◄─┘                                           │
│              │ Virtualization.framework                             │
│              ▼                                                      │
│  VM macOS → vllm-mlx:8000  ·  Qwen2.5-VL-7B MLX  ·  LaunchDaemon    │
└─────────────────────────────────────────────────────────────────────┘
```

Une requête suit le chemin : Regia → pod docverify → service vllm-mlx → pod sur le Mac → VM → vllm-mlx.

## Briques externes

| Projet | Rôle |
|---|---|
| [macOS-vz-kubelet](https://github.com/agoda-com/macOS-vz-kubelet) | Pilote `Virtualization.framework` pour faire tourner des VM macOS comme des pods |
| [oras-macos-vz](https://github.com/agoda-com/oras-macos-vz) | Empaquette l'image VM au format OCI poussé dans le registry |
| [macosvm](https://github.com/s-u/macosvm) | Crée et démarre la VM source pendant la préparation de l'image |
| [vllm-mlx](https://github.com/waybarrios/vllm-mlx) | Serveur OpenAI-compatible sur la stack MLX d'Apple Silicon |
| [Qwen2.5-VL-7B-Instruct-4bit](https://huggingface.co/mlx-community/Qwen2.5-VL-7B-Instruct-4bit) | Modèle vision-langage servi par défaut |

## Développer et tester en local

docverify est un projet Go pur, sans dépendance Go externe. Il a en revanche besoin de quelques outils système et d'un endpoint LLM joignable.

```bash
brew install go tesseract tesseract-lang poppler imagemagick dmtx-utils jq
go build -o docverify ./cmd/docverify
```

Le même binaire a trois modes. Sans argument, il démarre le serveur HTTP sur `:8680`, et l'URL du LLM se règle par la variable `LLM_URL`. Avec un fichier ou du JSON en argument, il analyse un document unique et l'affiche en tableau. Si l'entrée contient une liste `individuals`, il bascule en mode batch sur plusieurs personnes.

```bash
LLM_URL=http://localhost:8000 ./docverify     # serveur HTTP
./docverify input.json                        # un seul document
./scripts/test-api.sh all                    # suite de tests sur les samples
```

La route, ses champs et le mode webhook sont décrits dans le wiki, page [API HTTP](https://git.int.orbinea.example/tfuchs/docverify/wiki/API-HTTP).

## Déployer

La mise en place complète, de la préparation de l'image VM jusqu'à la mise en service du backend, est documentée pas à pas dans le wiki. Une fois l'image VM prête et le virtual-kubelet lancé, le déploiement courant tient en deux manifestes et un script.

```bash
kubectl apply -f deploy/k8s/docverify-deployment.yaml   # service docverify et ses replicas
kubectl apply -f deploy/k8s/macos.yaml                 # backend vllm-mlx, un par Mac via DaemonSet
./scripts/redeploy.sh                                  # rebuild, import et rollout après une modif Go
```

Pour servir le LLM depuis une machine Linux à GPU plutôt que depuis la VM macOS, `scripts/gen-llm-backend.sh` détecte le GPU discret et génère le manifeste vLLM correspondant, derrière le même service `vllm-mlx`.

## Documentation

| Page | Contenu |
|---|---|
| [API HTTP](https://git.int.orbinea.example/tfuchs/docverify/wiki/API-HTTP) | Route, champs multipart, réponses, mode webhook, codes d'erreur |
| [Préparation image VM](https://git.int.orbinea.example/tfuchs/docverify/wiki/Preparation-image-VM) | Création de la VM macOS, installation de vllm-mlx, push OCI |
| [Déploiement macOS-vz](https://git.int.orbinea.example/tfuchs/docverify/wiki/Deploiement-macOS-vz) | virtual-kubelet, certs TLS, mise en service du backend |
| [Supervision vllm-mlx](https://git.int.orbinea.example/tfuchs/docverify/wiki/Supervision-vllm-mlx) | LaunchDaemon et watchdog à l'intérieur de la VM |
| [Commandes utiles](https://git.int.orbinea.example/tfuchs/docverify/wiki/Commandes-utiles) | Logs, shell dans un pod, scale, recapture |
| [Dashboard](https://git.int.orbinea.example/tfuchs/docverify/wiki/Dashboard) | Dashboard Kubernetes, installation et accès |

## Scripts

Le dossier `scripts/` regroupe l'outillage de développement et d'exploitation. Les paramètres communs comme l'URL de l'API, le modèle, le chemin des samples ou le cluster se centralisent dans un `.env` à la racine : copie `.env.example` en `.env` et ajuste. Chaque script charge ce `.env` s'il existe, sinon il retombe sur ses défauts.

| Script | Rôle |
|---|---|
| `test-api.sh` | Suite de tests HTTP contre les samples, un cas ou tous |
| `concurrency-test.sh` | Rafale parallèle, garde anti-régression de la contamination |
| `bench-models.sh` + `score-bench.sh` | Bench multi-modèles et scoring contre le groundtruth |
| `redeploy.sh` | Rebuild image, import k3d et rollout docverify |
| `recapture-vm.sh` | Recapture de l'image VM de bout en bout |
| `gen-llm-backend.sh` | Génère un backend LLM Linux pour GPU discret |
| `push-wiki.sh` | Pousse `wiki/` vers le wiki Gitea |

Le détail de chaque script et de sa configuration est sur la page [Scripts](https://git.int.orbinea.example/tfuchs/docverify/wiki/Scripts) du wiki.

## Machines

Le développement se fait aujourd'hui sur un Mac Mini M4 Pro, 12 cœurs et 48 Go de mémoire unifiée. Trois Mac Mini supplémentaires arrivent pour répartir la charge du LLM sur plusieurs backends : mêmes M4 Pro 12 cœurs, mais avec 24 Go de mémoire chacun.
