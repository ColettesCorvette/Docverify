# Note d'anonymisation

Ce dépôt est la publication d'un travail réalisé pendant un stage de fin de
deuxième année de BUT Informatique (avril – juillet 2026). Le code et la
documentation sont authentiques, **mais tous les noms propres ont été
remplacés** avant publication, y compris dans l'historique git.

Si vous lisez ce dépôt en pensant y reconnaître une entreprise, un client ou
une personne : ce n'est pas le cas. Rien de ce qui suit ne désigne une entité
réelle.

## Ce qui a été remplacé

| Dans ce dépôt | Nature |
|---|---|
| `Orbinea` | l'entreprise d'accueil |
| `Regia` | le back-office métier qui appelle ce service |
| `docverify` | le service lui-même |
| `orbinea.example`, `*.int.orbinea.example`, `alpha.srv.orbinea` | domaines et hôtes internes |
| les plages `10.30.x`, `10.40.x`, `10.50.x`, `192.168.10/20/30/40.x` | adressage privé du cluster |

Sont en revanche **authentiques et volontairement conservés** : les noms de
technologies (Go, Kubernetes, k3d, Caddy, Tesseract, Docker Model Runner,
Gemma), et les références publiques du 2D-DOC — autorités de certification
ANTS, Certigna/Dhimyotis, SunnyStamp, IDnow — dont dépend la vérification de
signature. Les altérer aurait privé le code de son sens.

## Données personnelles

Le service a été développé et évalué contre un jeu de justificatifs réels
(cartes d'identité, passeports, factures, avis d'imposition) fournis par
l'entreprise. **Aucun de ces documents n'est distribué ici**, ni dans l'arbre
de travail, ni dans l'historique : ils ont été retirés de l'intégralité des
commits.

Les identités qui subsistent dans le code, les tests et les jeux d'essai
(`DURAND`, `BEN AMAR`, `SOPHIE BERTRAND`, `LEROY PIERRE`, `LUCIE`, `CAMILLE`,
`SOLANGE`…), ainsi que les adresses, dates de naissance, numéros de facture et
chaînes MRZ ou 2D-DOC associées, sont **entièrement fictifs**. Ils ont été
construits pour préserver la structure des données — longueur des champs MRZ,
absence de frontière de mot dans un champ composite — afin que les tests
gardent leur sens.

Deux exceptions, toutes deux publiques : le spécimen officiel 2D-DOC
(`MME/SPECIMEN/NATACHA`, `145 AVENUE DES SPECIMENS`) et le spécimen MRZ ICAO
(`P<UTOERIKSSON<<ANNA<MARIA`, pays fictif « UTO »).

## Conséquences pratiques

- La suite de bench (`scripts/bench-models.sh`, `scripts/score-bench.sh`) et
  `scripts/test-api.sh` référencent des fichiers d'échantillon absents : ils ne
  peuvent pas s'exécuter tels quels. `samples/groundtruth.json` est conservé
  pour montrer la forme de la vérité de terrain.
- Les tests unitaires (`go test ./...`), eux, ne dépendent d'aucun échantillon
  et restent exécutables.
- Les chaînes 2D-DOC des tests portent encore une signature cryptographique,
  désormais sans rapport avec leur contenu modifié. Les tests concernés ne
  vérifient que le *parsing*, pas la signature ; la vérification de signature
  est testée séparément, sur les spécimens publics.

## Droits

Ce code a été écrit dans le cadre d'un stage salarié. Il est publié à titre de
portfolio, sans licence d'utilisation : aucun droit de réutilisation n'est
concédé. Le prototype initial dont est issu ce service avait été écrit par le
tuteur de stage.
