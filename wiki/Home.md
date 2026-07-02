# Bienvenue sur le wiki docverify

**docverify** analyse des justificatifs administratifs — factures, cartes d'identité, passeports, justificatifs de domicile — en combinant OCR et LLM multimodal. C'est un service Go sur Kubernetes, avec une queue interne par pod, destiné au back-office Regia.

Le LLM est servi par Docker Model Runner : `Gemma 4 E4B` au format GGUF, sur GPU Metal, en process hôte. Les pods l'atteignent par la passerelle hôte fixe `192.168.65.254:12434`, ce qui évite les pannes DNS au redémarrage.

Deux points qui comptent pour l'intégration : docverify **vérifie la signature** des cachets 2D-DOC contre les certificats ANTS embarqués dans le binaire, et chaque verdict porte un **niveau de confiance** de `nul` à `haut` qui dit à Regia s'il peut automatiser ou s'il faut une validation humaine. Les détails sont sur [API HTTP](https://git.int.orbinea.example/tfuchs/docverify/wiki/API-HTTP).

La mise à l'échelle est en *shared-nothing* : chaque Mac, y compris le principal qui sert aussi de worker, fait tourner son cluster `k3d` et son DMR. L'image docverify est poussée sur un registre interne et chaque k3d la tire. Un Load Balancer Caddy statique répartit les requêtes, une ligne `to <IP>:8680` par Mac, et le health-check `/health` éjecte tout seul un nœud tombé.

## Pour utiliser le service

- [API HTTP](https://git.int.orbinea.example/tfuchs/docverify/wiki/API-HTTP) : la route `POST /api/v1/supporting-documents`, les champs, le webhook, les codes d'erreur.
- [Dashboard](https://git.int.orbinea.example/tfuchs/docverify/wiki/Dashboard) : installation et accès au tableau de bord Kubernetes.

## Pour exploiter

- [Commandes utiles](https://git.int.orbinea.example/tfuchs/docverify/wiki/Commandes-utiles) : logs, accès à un pod, scale, backend LLM.
- [Scripts](https://git.int.orbinea.example/tfuchs/docverify/wiki/Scripts) : l'outillage du dépôt et sa configuration par `.env`.

---

**Repo :** [tfuchs/docverify](https://git.int.orbinea.example/tfuchs/docverify)
**Stack :** Go 1.26, k3d sur Docker Desktop, queue Go interne, Docker Model Runner, Gemma 4 E4B
**Plateforme :** quatre Mac Mini M4 Pro ARM64 12 cœurs, le principal en 48 Go, trois workers en 24 Go
