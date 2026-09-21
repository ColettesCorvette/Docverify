# samples/

Ce dossier contenait les dix justificatifs qui servaient de jeu d'évaluation :
factures, cartes d'identité, passeport, avis d'imposition.

**Ces documents ne sont pas distribués.** Il s'agissait de pièces réelles
appartenant à des personnes identifiables ; elles ont été retirées de l'arbre
de travail et de tous les commits de l'historique. Voir
[ANONYMISATION.md](../ANONYMISATION.md).

Reste ici `groundtruth.json`, la vérité de terrain du benchmark, conservée pour
documenter la méthode d'évaluation : un cas par document, avec l'entrée
soumise au service et la sortie attendue (type de document, champs trouvés,
verdict). Les identités et les dates qu'il contient sont fictives.

Pour rejouer le benchmark il faudrait reconstituer un jeu d'essai : dix
documents couvrant facture PDF native, facture scannée, CNI recto/verso,
passeport à MRZ, et justificatif porteur d'un cachet 2D-DOC — le tout avec
des identités inventées.
