# API HTTP docverify

Le binaire `./docverify` lancé sans argument démarre un serveur HTTP sur `:8680`. En cluster k3d, il est exposé par un Service LoadBalancer sur `localhost:8680`.

## Instance de test sur le réseau interne

Une instance tourne en continu pour évaluer le service :

```
http://192.168.30.42:8680/api/v1/supporting-documents
```

Test rapide de disponibilité :

```bash
curl http://192.168.30.42:8680/health
```

L'IP est fixée par réservation DHCP. Il n'y a pas d'authentification en V1 : c'est un service interne, à ne jamais exposer ailleurs que sur le réseau interne.

## Variables d'environnement

| Variable | Défaut | Rôle |
|---|---|---|
| `LLM_URL` | `http://localhost:12434/engines` | URL de l'endpoint LLM OpenAI-compatible. En cluster K8s : `http://192.168.65.254:12434/engines`, la passerelle hôte fixe de Docker Desktop, qui évite les pannes DNS. |
| `LLM_MODEL` | `huggingface.co/unsloth/gemma-4-e4b-it-gguf:Q5_K_M` | Modèle utilisé quand la requête n'en précise pas. |
| `QUEUE_WORKERS` | `1` | Workers de la queue in-process, par pod. Un GPU Metal ne traite qu'une inférence à la fois, bench du 2026-06-25 à l'appui : un worker par GPU, la prod tourne à 1. Pour plus de débit, on ajoute des Macs, pas des workers. |
| `LLM_MAX_CONCURRENT` | `1` | Sémaphore d'appels LLM simultanés. À garder aligné sur `QUEUE_WORKERS` et la capacité du backend. |
| `DOCVERIFY_CALLBACK_ALLOWED_HOSTS` | vide | Liste blanche d'hôtes, séparés par des virgules, autorisés comme `callback_url` en mode asynchrone. Vide = pas de restriction par hôte. C'est le verrou anti-SSRF pour fermer les callbacks vers d'autres services internes. |
| `DOCVERIFY_DEBUG_OCR` | désactivé | À `1`, logge les 1500 premiers caractères du texte OCR, pour diagnostiquer un document mal classé. **Jamais en prod** : le texte extrait contient des données personnelles. |

> Il n'y a **pas** de Redis : la queue est in-process dans chaque pod, un channel Go bufferisé consommé par un pool de workers. Une variante Redis a existé puis a été abandonnée, c'était disproportionné pour un seul backend LLM. Elle reste archivée dans le tag `archive/redis-queue`.

## `GET /health/live`

Liveness : répond 200 tant que le process est vivant, sans jamais appeler le backend LLM. C'est la route de la **livenessProbe** K8s — un LLM down ne doit pas faire redémarrer le pod.

```bash
curl http://localhost:8680/health/live
```

```json
{ "status": "ok" }
```

## `GET /health`

Readiness, enrichi des métriques de la queue et d'infos de build. C'est la route de la **readinessProbe** K8s et du health-check Caddy : un LLM injoignable sort le nœud de la rotation, sans redémarrer le pod.

```bash
curl http://localhost:8680/health
```

```json
{
  "status": "ok",
  "version": "dev",
  "uptime_s": 42,
  "workers": 1,
  "queued": 0,
  "capacity": 100,
  "inflight": 0,
  "processed": 137,
  "llm": "ok"
}
```

Quand le backend LLM ne répond pas, `status` passe à `"degraded"`, `llm` à `"unreachable"` et le code HTTP à `503`.

## `POST /api/v1/supporting-documents`

La route principale, appelée par Regia en multipart/form-data. Regia envoie le fichier avec les métadonnées attendues — nom, prénom, code postal — qui viennent de sa BDD, pas du client final.

Le fichier est écrit dans `/tmp`, le job part dans la queue, un worker déroule le pipeline OCR + analyse, puis le fichier est supprimé. Si la première passe extrait trop peu, une seconde passe par segmentation est tentée. Le champ `raw_text` est vidé de la réponse pour rester conforme RGPD.

### Champs

| Champ         | Type   | Requis | Description                                                                       |
|---------------|--------|--------|-----------------------------------------------------------------------------------|
| `doc`         | file   | oui    | Image ou PDF du document, 30 Mo maximum                                            |
| `name`        | string | non    | Nom attendu                                                                       |
| `surname`     | string | non    | Prénom attendu                                                                    |
| `postal_code` | string | **oui** | Code postal attendu. Toujours transmis par Regia, y compris pour une CNI ou un passeport où il est ignoré à l'analyse, pas à la réception. |
| `valid_months`| int    | non    | Fenêtre de validité des factures et justificatifs de domicile, en mois. Défaut 3, vide ou ≤ 0 vaut 3. Ignoré sur CNI et passeport. |
| `model`       | string | non    | Override du modèle vision. Le défaut vient de `LLM_MODEL`, soit `huggingface.co/unsloth/gemma-4-e4b-it-gguf:Q5_K_M` en cluster.  |
| `llm_url`     | string | non    | Override de l'URL LLM. Défaut : `LLM_URL` ou `http://localhost:12434/engines`.    |
| `callback_url`| string | non    | Bascule en mode asynchrone par webhook, voir la section dédiée.                   |

### Exemple

```bash
curl -X POST http://localhost:8680/api/v1/supporting-documents \
  -F "doc=@samples/Facture_FreePro.pdf" \
  -F "name=ORBINEA" \
  -F "surname=CONCEPT" \
  -F "postal_code=99000"
```

### Réponse

```json
{
  "ok": true,
  "doc_type": "facture",
  "name_found": true,
  "postal_found": true,
  "postal_match": "99000",
  "date_found": true,
  "date_valid": true,
  "doc_date": "01/03/2026",
  "confidence": "haut",
  "signature_verified": true
}
```

| Champ                | Type   | Description                                                                                          |
|----------------------|--------|------------------------------------------------------------------------------------------------------|
| `ok`                 | bool   | **Le verdict global.** `true` si tous les contrôles passent : type reconnu, nom et code postal concordants, date dans la fenêtre de validité. C'est le champ que Regia lit en premier. |
| `doc_type`           | string | `facture` \| `carte_identite` \| `passeport` \| `courrier` \| `inconnu`.                              |
| `name_found`         | bool   | Nom attendu retrouvé.                                                                                 |
| `surname_found`      | bool   | Prénom retrouvé. Présent seulement si `surname` a été fourni.                                         |
| `postal_found`       | bool   | Code postal retrouvé. Ignoré sur CNI et passeport.                                                    |
| `postal_match`       | string | Code postal effectivement lu.                                                                         |
| `date_found`         | bool   | Une date exploitable a été extraite.                                                                  |
| `date_valid`         | bool   | **Validité temporelle.** Facture ou domicile : `doc_date` postérieure à aujourd'hui moins `valid_months`. CNI ou passeport : date d'expiration dans le futur. |
| `doc_date`           | string | Date du document, émission ou facturation.                                                            |
| `expiry_date`        | string | Date d'expiration. CNI et passeport uniquement.                                                       |
| `issue_date`         | string | Date de délivrance. CNI et passeport uniquement.                                                      |
| `birth_date`         | string | Date de naissance. CNI et passeport uniquement.                                                       |
| `confidence`         | string | Confiance dans l'extraction, voir la section dédiée.                                                  |
| `signature_verified` | bool   | Chemin 2D-DOC, voir la section dédiée.                                                                |
| `unreadable`         | bool   | Document non analysable, voir la section dédiée.                                                      |
| `errors`             | array  | Les motifs d'échec quand `ok` vaut `false`.                                                           |

**Fenêtre de validité.** Une facture ou un justificatif de domicile est considéré périmé au-delà de `valid_months` mois. La fenêtre se règle par requête via le champ multipart, par exemple `12` pour accepter un justificatif d'un an ; en son absence le défaut est 3 mois. L'entrée CLI et le batch JSON exposent le même champ. Les CNI et passeports l'ignorent : leur validité vient de la date d'expiration lue sur le document.

Toutes les dates sortent au format `DD/MM/YYYY`.

### Le champ `confidence`

Chaque verdict porte un niveau de confiance dans l'extraction, distinct de la validité métier. Quatre paliers :

| Palier | Quand | Décision Regia |
|---|---|---|
| `haut` | 2D-DOC à signature vérifiée, ou OCR net avec tous les champs concordants | automatisable |
| `moyen` | fallback LLM pleine page, ou 2D-DOC dont la signature n'a pas pu être vérifiée | confirmation manuelle |
| `bas` | rescousse par segmentation, ou champ clé manquant | confirmation manuelle |
| `nul` | document illisible ou timeout | rejet / review |

C'est le signal recommandé pour décider de l'automatisation côté Regia. La règle exacte entre palier et action reste à caler avec le métier.

### Le champ `signature_verified`

Présent uniquement quand le document porte un 2D-DOC. `true` signifie que la signature cryptographique du cachet a été validée contre les certificats des autorités ANTS embarqués dans le binaire. Une signature vérifiée justifie le palier `confidence: haut` ; un cachet lisible mais non authentifié reste à `moyen`. Si le champ est absent, le document n'a simplement pas de 2D-DOC.

Chaque réponse synchrone porte aussi un en-tête **`X-Request-Id`**, un UUID v4 qui identifie la requête et permet de la corréler avec les logs serveur. C'est le même identifiant qui sert de `job_id` en mode asynchrone.

### Le champ `unreadable`

Quand le document n'est pas analysable, le verdict ressemble à :

```json
{
  "ok": false,
  "doc_type": "inconnu",
  "unreadable": true,
  "errors": ["Document illisible — review manuelle requise"]
}
```

Trois situations le déclenchent :
- l'OCR sort moins de 30 caractères exploitables ;
- le type de document n'est pas détecté et aucun signal n'est trouvé, ni nom, ni code postal, ni date ;
- le pipeline a dépassé ses délais, `queueTimeout` de 45 s d'attente en file ou `execTimeout` de 120 s d'exécution — typiquement un document trop lourd ou un backend LLM saturé.

Dans tous ces cas, Regia doit router le dossier vers la validation manuelle, pas le rejeter.

### Codes HTTP

La règle : `200` dès que le pipeline a produit un verdict, **y compris** `unreadable: true`. Un document illisible n'est pas une erreur du service, c'est un verdict, et c'est à Regia de router vers la review manuelle. Les codes `4xx` et `5xx` sont réservés aux vraies erreurs structurelles.

| Statut HTTP | Cas                                                              |
|-------------|------------------------------------------------------------------|
| `202`       | Mode asynchrone : `callback_url` fourni, job accepté et mis en file              |
| `400`       | Multipart invalide, champ `doc` manquant, `postal_code` vide, contenu incohérent avec l'extension déclarée, `callback_url` invalide, upload de plus de 30 Mo |
| `404`       | Route inconnue                                                   |
| `405`       | Méthode non autorisée sur une route existante                    |
| `499`       | Client déconnecté avant la fin du pipeline, convention nginx, body vide |
| `500`       | Échec OCR amont, création du fichier temporaire impossible, erreur LLM non transitoire |
| `503`       | Queue saturée — Regia peut retenter plus tard                     |

Le `405` est émis automatiquement par le ServeMux quand la méthode ne correspond à aucun pattern enregistré. Le serveur fixe aussi `ReadHeaderTimeout=10s`, `ReadTimeout=60s`, `WriteTimeout=5min` et `IdleTimeout=120s` pour se protéger des attaques slowloris.

## Mode asynchrone par webhook

Par défaut la route est **synchrone** : le client garde la connexion ouverte jusqu'au verdict et reçoit l'en-tête `X-Request-Id`.

S'il fournit un champ multipart **`callback_url`** en http ou https, la route bascule en **asynchrone**. La réponse immédiate n'est plus le verdict mais un accusé de mise en file, avec le code **`202 Accepted`** :

```json
{ "job_id": "c7080471-48e4-45ab-9701-1c6197addbde", "status": "queued" }
```

Le job passe par la même queue et les mêmes workers que le mode synchrone. En fin de traitement, docverify **POST** le verdict à `callback_url`, sous l'une de ces deux formes :

```json
{ "job_id": "c7080471-…", "status": "done", "result": { "ok": true, "doc_type": "facture", "…": "…" } }
```

```json
{ "job_id": "c7080471-…", "status": "error", "error": "Erreur OCR: …" }
```

Le `result` est exactement l'`AnalysisResult` qu'aurait renvoyé l'appel synchrone, `raw_text` vidé. Le callback est retenté une fois en cas d'échec. Il n'y a **pas de polling** : docverify ne stocke pas le résultat, c'est le webhook qui le pousse. Aucune dépendance partagée, donc compatible multi-pod.

> Sécurité SSRF : au moment du dial, le callback **refuse** les IP loopback, link-local — dont l'endpoint metadata `169.254.169.254` —, non spécifiées et multicast, et **ne suit pas les redirections**. Vérifier au dial plutôt qu'à la validation d'URL ferme la fenêtre TOCTOU et le DNS-rebinding. Les IP privées restent autorisées puisque le callback Regia est interne ; pour fermer complètement le SSRF interne, définir **`DOCVERIFY_CALLBACK_ALLOWED_HOSTS`** avec la liste des hôtes Regia.

## Routes legacy

Les anciennes routes `POST /check` et `POST /batch` ont été supprimées le 2026-06-02, c'était du code mort qui ne compilait plus. Les modes mono-document et batch restent accessibles via le CLI, `./docverify <input.json>`. La seule route HTTP est `POST /api/v1/supporting-documents`.

## Erreurs

Toutes les erreurs structurelles sortent en JSON :

```json
{ "ok": false, "error": "Erreur OCR: ..." }
```

## Tests

```bash
./scripts/test-api.sh all       # tous les samples
./scripts/test-api.sh freepro   # un seul
./scripts/test-api.sh cni8493   # le passeport BEN AMAR
```

---

Dernière mise à jour : 2026-07-02
