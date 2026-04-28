package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// ── Individu ──────────────────────────────────────────────────────────────────

type Individual struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Surname     string   `json:"surname"`      // optionnel
	BirthDate   string   `json:"birth_date"`   // optionnel, ex: "15/03/2010"
	PostalCodes []string `json:"postal_codes"` // optionnel
	Cities      []string `json:"cities"`       // optionnel
	Referent    bool     `json:"referent"`     // true = référent du dossier
}

// ── Document (nouveau format) ─────────────────────────────────────────────────

type Document struct {
	ID          string   `json:"id"`
	Path        string   `json:"path"`
	Individual  string   `json:"individual"`   // ID de l'individu rattaché
	Checks      []string `json:"checks"`       // champs à vérifier
	Label       string   `json:"label"`        // description libre (optionnel)
	ValidMonths int      `json:"valid_months"` // défaut: 3
}

// ── Format batch ──────────────────────────────────────────────────────────────

type BatchInput struct {
	Individuals []Individual `json:"individuals"`
	Documents   []Document   `json:"documents"`
	Model       string       `json:"model"`
	OllamaURL   string       `json:"ollama_url"`
}

// ── Ancien format (rétrocompatibilité) ────────────────────────────────────────

type Input struct {
	Name         string   `json:"name"`
	Surname      string   `json:"surname"`
	ValidMonths  int      `json:"valid_months"`
	PostalCodes  []string `json:"postal_codes"`
	DocumentPath string   `json:"document_path"`
	Model        string   `json:"model"`
	OllamaURL    string   `json:"ollama_url"`
}

// ── Résultat d'un document ────────────────────────────────────────────────────

// ReferentAddress stocke l'adresse validée du référent pour la propager aux autres
type ReferentAddress struct {
	PostalMatch string // code postal trouvé
	CityMatch   string // ville trouvée
}

type DocResult struct {
	DocumentID          string         `json:"document_id"`
	Label               string         `json:"label,omitempty"`
	IndividualID        string         `json:"individual_id"`
	Checks              []string       `json:"checks"`
	OK                  bool           `json:"ok"`
	AddressFromReferent bool           `json:"address_from_referent,omitempty"` // adresse héritée du référent
	Errors              []string       `json:"errors,omitempty"`
	Analysis            AnalysisResult `json:"analysis"`
}

// ── Point d'entrée ────────────────────────────────────────────────────────────

func main() {
	if len(os.Args) < 2 {
		// Pas d'argument → mode serveur API
		startServer()
		return
	}

	data, err := readArg(os.Args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "Erreur lecture input: %v\n", err)
		os.Exit(1)
	}

	if isBatchFormat(data) {
		runBatch(data)
	} else {
		runLegacy(data)
	}
}

func isBatchFormat(data []byte) bool {
	var probe struct {
		Individuals []json.RawMessage `json:"individuals"`
	}
	return json.Unmarshal(data, &probe) == nil && len(probe.Individuals) > 0
}

// ── Mode batch ────────────────────────────────────────────────────────────────

func runBatch(data []byte) {
	var batch BatchInput
	if err := json.Unmarshal(data, &batch); err != nil {
		fmt.Fprintf(os.Stderr, "Erreur parsing JSON batch: %v\n", err)
		os.Exit(1)
	}

	// Valeurs par défaut
	if batch.Model == "" {
		batch.Model = defaultModel
	}
	if batch.OllamaURL == "" {
		batch.OllamaURL = "http://localhost:11434"
	}

	// Index individus
	indivIdx := map[string]Individual{}
	for _, ind := range batch.Individuals {
		indivIdx[ind.ID] = ind
	}

	// Validation
	if err := validateBatch(batch, indivIdx); err != nil {
		fmt.Fprintf(os.Stderr, "Input invalide: %v\n", err)
		os.Exit(1)
	}

	// Identifier le référent
	var referentID string
	for _, ind := range batch.Individuals {
		if ind.Referent {
			referentID = ind.ID
			break
		}
	}

	fmt.Printf("📋 Analyse batch : %d individu(s), %d document(s)\n",
		len(batch.Individuals), len(batch.Documents))
	fmt.Printf("   Modèle : %s\n", batch.Model)
	if referentID != "" {
		fmt.Printf("   Référent : %s\n", referentID)
	}
	fmt.Println()

	var results []DocResult
	allOK := true
	var referentAddr *ReferentAddress // adresse validée du référent, nil jusqu'à validation

	for _, doc := range batch.Documents {
		if doc.ValidMonths <= 0 {
			doc.ValidMonths = 3
		}
		ind := indivIdx[doc.Individual]

		// En-tête du document
		label := doc.Label
		if label == "" {
			label = doc.ID
		}
		fmt.Printf("┌─ %s\n", label)
		fmt.Printf("│  Fichier     : %s\n", doc.Path)
		indivLabel := ind.Name
		if ind.Surname != "" {
			indivLabel += " " + ind.Surname
		}
		if ind.Referent {
			indivLabel += " ⭐ référent"
		}
		fmt.Printf("│  Individu    : %s [%s]\n", indivLabel, ind.ID)
		fmt.Printf("│  Vérifs      : %s\n", strings.Join(doc.Checks, ", "))

		// Détecter si l'adresse peut être héritée du référent
		checksSet := map[string]bool{}
		for _, c := range doc.Checks {
			checksSet[c] = true
		}
		addressFromReferent := false
		if !ind.Referent && referentAddr != nil && (checksSet["postal"] || checksSet["city"]) {
			fmt.Printf("│  📍 Adresse héritée du référent (%s)\n", referentAddr.PostalMatch)
			addressFromReferent = true
		}
		fmt.Println("│")

		// OCR
		var rawText string
		var extractErr error
		if IsPDF(doc.Path) {
			fmt.Println("│  📄 Traitement PDF...")
			rawText, extractErr = ExtractPDF(doc.Path, batch.Model, batch.OllamaURL)
		} else {
			fmt.Println("│  🖼️  Traitement image...")
			rawText, extractErr = ExtractText(doc.Path, batch.Model, batch.OllamaURL)
		}

		if extractErr != nil {
			res := DocResult{
				DocumentID: doc.ID, Label: doc.Label,
				IndividualID: doc.Individual, Checks: doc.Checks,
				OK:     false,
				Errors: []string{"Erreur OCR: " + extractErr.Error()},
			}
			results = append(results, res)
			allOK = false
			fmt.Printf("│  ❌ Erreur OCR: %v\n", extractErr)
			fmt.Println("└─────────────────────────────────────────")
			continue
		}

		// Construire l'Input
		input := buildInput(ind, doc, batch)

		// Analyse complète
		analysis := AnalyzeText(rawText, input)

		// Si adresse héritée du référent : validation automatique
		if addressFromReferent && referentAddr != nil {
			analysis.PostalFound = true
			analysis.PostalMatch = referentAddr.PostalMatch + " (référent)"
		}

		// Évaluer les checks demandés
		res := evaluateChecks(analysis, doc, ind)
		res.AddressFromReferent = addressFromReferent
		results = append(results, res)
		if !res.OK {
			allOK = false
		}

		// Si c'est le référent avec adresse validée → mémoriser pour les autres
		if ind.Referent && (checksSet["postal"] || checksSet["city"]) {
			if analysis.PostalFound && analysis.PostalMatch != "" {
				referentAddr = &ReferentAddress{PostalMatch: analysis.PostalMatch}
				fmt.Printf("│  📍 Adresse référent mémorisée (%s)\n", referentAddr.PostalMatch)
			}
		}

		printDocResult(res)
		fmt.Println("└─────────────────────────────────────────")
	}

	// Résumé
	printBatchSummary(results, batch.Individuals, allOK)

	// JSON global
	out, _ := json.MarshalIndent(map[string]any{
		"ok":      allOK,
		"results": results,
	}, "", "  ")
	fmt.Printf("\nJSON:\n%s\n", string(out))

	if !allOK {
		os.Exit(1)
	}
}

// buildInput construit un Input adapté à l'individu et aux checks demandés
func buildInput(ind Individual, doc Document, batch BatchInput) Input {
	checksSet := map[string]bool{}
	for _, c := range doc.Checks {
		checksSet[c] = true
	}
	input := Input{
		Name:         ind.Name,
		Surname:      ind.Surname,
		DocumentPath: doc.Path,
		ValidMonths:  doc.ValidMonths,
		Model:        batch.Model,
		OllamaURL:    batch.OllamaURL,
	}
	if checksSet["postal"] {
		input.PostalCodes = ind.PostalCodes
	}
	return input
}

// evaluateChecks évalue uniquement les checks demandés sur l'analyse,
// en tenant compte du type de document détecté pour ignorer les checks non pertinents.
func evaluateChecks(analysis AnalysisResult, doc Document, ind Individual) DocResult {
	checksSet := map[string]bool{}
	for _, c := range doc.Checks {
		checksSet[c] = true
	}

	isIdentityDoc := analysis.DocType == DocTypePassport || analysis.DocType == DocTypeID
	isAddressDoc := analysis.DocType == DocTypeInvoice || analysis.DocType == DocTypeLetter

	ok := true
	var errors []string

	if checksSet["name"] && !analysis.NameFound {
		ok = false
		errors = append(errors, fmt.Sprintf("Nom '%s' non trouvé", ind.Name))
	}
	if checksSet["surname"] && !analysis.SurnameFound {
		ok = false
		errors = append(errors, fmt.Sprintf("Prénom '%s' non trouvé", ind.Surname))
	}
	if checksSet["postal"] {
		if isIdentityDoc {
			// Code postal non pertinent sur CNI/passeport → ignoré silencieusement
			analysis.PostalFound = true
		} else if !analysis.PostalFound {
			ok = false
			errors = append(errors, fmt.Sprintf("Code postal %v non trouvé", ind.PostalCodes))
		}
	}
	if checksSet["city"] {
		if isIdentityDoc {
			// Ville non pertinente sur CNI/passeport → ignorée silencieusement
		} else {
			cityFound := false
			for _, city := range ind.Cities {
				if strings.Contains(strings.ToUpper(analysis.RawText), strings.ToUpper(city)) {
					cityFound = true
					break
				}
			}
			if !cityFound {
				ok = false
				errors = append(errors, fmt.Sprintf("Ville %v non trouvée", ind.Cities))
			}
		}
	}
	if checksSet["date"] {
		if isIdentityDoc {
			// Sur CNI/passeport : "date valide" = non expiré (déjà évalué dans DateValid)
			if !analysis.DateValid {
				ok = false
				if analysis.ExpiryDate != "" {
					errors = append(errors, fmt.Sprintf("Document expiré (expiration : %s)", analysis.ExpiryDate))
				} else {
					errors = append(errors, "Document expiré ou date d'expiration non trouvée")
				}
			}
		} else {
			// Sur facture/courrier : date récente obligatoire
			if !analysis.DateFound || !analysis.DateValid {
				ok = false
				if !analysis.DateFound {
					errors = append(errors, "Date du document non trouvée")
				} else {
					errors = append(errors, "Document trop ancien")
				}
			}
		}
	}
	if checksSet["birth_date"] {
		if isAddressDoc {
			// Date de naissance inexistante sur une facture → ignorée silencieusement
		} else if ind.BirthDate == "" {
			// Pas de date attendue : juste vérifier qu'on en a trouvé une
			if analysis.BirthDate == "" {
				ok = false
				errors = append(errors, "Date de naissance non trouvée dans le document")
			}
		} else {
			// Comparer avec la date attendue
			birthOK := analysis.BirthDate != "" &&
				normalizeDate(analysis.BirthDate) == normalizeDate(ind.BirthDate)
			if !birthOK {
				ok = false
				errors = append(errors, fmt.Sprintf(
					"Date de naissance attendue '%s', trouvée '%s'",
					ind.BirthDate, analysis.BirthDate))
			}
		}
	}

	analysis.Errors = errors
	analysis.OK = ok

	return DocResult{
		DocumentID:   doc.ID,
		Label:        doc.Label,
		IndividualID: ind.ID,
		Checks:       doc.Checks,
		OK:           ok,
		Errors:       errors,
		Analysis:     analysis,
	}
}

func normalizeDate(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, " ", "/")
	s = strings.ReplaceAll(s, "-", "/")
	return strings.ToLower(s)
}

func printDocResult(r DocResult) {
	isIdentityDoc := r.Analysis.DocType == DocTypePassport || r.Analysis.DocType == DocTypeID
	isAddressDoc := r.Analysis.DocType == DocTypeInvoice || r.Analysis.DocType == DocTypeLetter

	fmt.Printf("│  Type détecté : %s\n", docTypeLabel(r.Analysis.DocType))
	fmt.Println("│")

	for _, check := range r.Checks {
		switch check {
		case "name":
			fmt.Printf("│  %-26s %s\n", "Nom :", boolEmoji(r.Analysis.NameFound))
		case "surname":
			fmt.Printf("│  %-26s %s\n", "Prénom :", boolEmoji(r.Analysis.SurnameFound))
		case "postal":
			if isIdentityDoc {
				fmt.Printf("│  %-26s %s\n", "Code postal :", "➖ ignoré (identité)")
			} else {
				line := boolEmoji(r.Analysis.PostalFound)
				if r.Analysis.PostalMatch != "" {
					line += "  (" + r.Analysis.PostalMatch + ")"
				}
				if r.AddressFromReferent {
					line += "  📍 hérité du référent"
				}
				fmt.Printf("│  %-26s %s\n", "Code postal :", line)
			}
		case "city":
			if isIdentityDoc {
				fmt.Printf("│  %-26s %s\n", "Ville :", "➖ ignoré (identité)")
			} else {
				line := boolEmoji(r.Analysis.PostalFound)
				if r.AddressFromReferent {
					line += "  📍 hérité du référent"
				}
				fmt.Printf("│  %-26s %s\n", "Ville :", line)
			}
		case "date":
			if isIdentityDoc {
				line := boolEmoji(r.Analysis.DateValid)
				if r.Analysis.ExpiryDate != "" {
					line += "  (expiration : " + r.Analysis.ExpiryDate + ")"
				}
				fmt.Printf("│  %-26s %s\n", "Non expiré :", line)
			} else {
				fmt.Printf("│  %-26s %s\n", "Date valide :", boolEmoji(r.Analysis.DateValid))
				if r.Analysis.DocDate != "" {
					fmt.Printf("│  %-26s %s\n", "  └ Date :", r.Analysis.DocDate)
				}
			}
		case "birth_date":
			if isAddressDoc {
				fmt.Printf("│  %-26s %s\n", "Date naissance :", "➖ ignoré (facture)")
			} else {
				line := boolEmoji(r.Analysis.BirthDate != "")
				if r.Analysis.BirthDate != "" {
					line += "  (" + r.Analysis.BirthDate + ")"
				}
				fmt.Printf("│  %-26s %s\n", "Date naissance :", line)
			}
		}
	}

	fmt.Println("│")
	if r.OK {
		fmt.Println("│  ✅  DOCUMENT VALIDE")
	} else {
		fmt.Println("│  ❌  DOCUMENT INVALIDE")
		for _, e := range r.Errors {
			fmt.Printf("│      → %s\n", e)
		}
	}
}

func printBatchSummary(results []DocResult, individuals []Individual, allOK bool) {
	// Index individus pour affichage
	indivNames := map[string]string{}
	for _, ind := range individuals {
		name := ind.Name
		if ind.Surname != "" {
			name += " " + ind.Surname
		}
		indivNames[ind.ID] = name
	}

	fmt.Println("\n╔════════════════════════════════════════════════════╗")
	fmt.Println("║                  RÉSUMÉ BATCH                     ║")
	fmt.Println("╠════════════════════════════════════════════════════╣")

	for _, r := range results {
		status := "✅"
		if !r.OK {
			status = "❌"
		}
		label := r.Label
		if label == "" {
			label = r.DocumentID
		}
		indivName := indivNames[r.IndividualID]
		fmt.Printf("║  %s  %-20s → %-16s ║\n",
			status, truncate(label, 20), truncate(indivName, 16))
	}

	fmt.Println("╠════════════════════════════════════════════════════╣")
	if allOK {
		fmt.Println("║  ✅  TOUS LES DOCUMENTS VALIDES                    ║")
	} else {
		fmt.Println("║  ❌  CERTAINS DOCUMENTS INVALIDES                  ║")
	}
	fmt.Println("╚════════════════════════════════════════════════════╝")
}

func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s + strings.Repeat(" ", n-len(runes))
	}
	return string(runes[:n-1]) + "…"
}

func validateBatch(batch BatchInput, indivIdx map[string]Individual) error {
	if len(batch.Individuals) == 0 {
		return fmt.Errorf("'individuals' ne peut pas être vide")
	}
	if len(batch.Documents) == 0 {
		return fmt.Errorf("'documents' ne peut pas être vide")
	}
	for _, doc := range batch.Documents {
		if doc.Path == "" {
			return fmt.Errorf("document '%s' : champ 'path' requis", doc.ID)
		}
		if doc.Individual == "" {
			return fmt.Errorf("document '%s' : champ 'individual' requis", doc.ID)
		}
		if _, ok := indivIdx[doc.Individual]; !ok {
			return fmt.Errorf("document '%s' référence individu inconnu: '%s'", doc.ID, doc.Individual)
		}
		if len(doc.Checks) == 0 {
			return fmt.Errorf("document '%s' : champ 'checks' requis", doc.ID)
		}
		if _, err := os.Stat(doc.Path); err != nil {
			return fmt.Errorf("fichier introuvable: %s", doc.Path)
		}
	}
	return nil
}

// ── Mode legacy ───────────────────────────────────────────────────────────────

func runLegacy(data []byte) {
	var input Input
	if err := json.Unmarshal(data, &input); err != nil {
		fmt.Fprintf(os.Stderr, "Erreur parsing JSON: %v\n", err)
		os.Exit(1)
	}
	if input.ValidMonths <= 0 {
		input.ValidMonths = 3
	}
	if input.Model == "" {
		input.Model = defaultModel
	}
	if input.OllamaURL == "" {
		input.OllamaURL = "http://localhost:11434"
	}
	if err := validateInput(input); err != nil {
		fmt.Fprintf(os.Stderr, "Input invalide: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("🔍 Analyse de : %s\n", input.DocumentPath)
	if input.Surname != "" {
		fmt.Printf("   Nom attendu    : %s %s\n", input.Name, input.Surname)
	} else {
		fmt.Printf("   Nom attendu    : %s\n", input.Name)
	}
	fmt.Printf("   Codes postaux  : %v\n", input.PostalCodes)
	fmt.Printf("   Validité       : %d mois\n", input.ValidMonths)
	fmt.Printf("   Modèle Ollama  : %s\n\n", input.Model)

	var rawText string
	var extractErr error
	if IsPDF(input.DocumentPath) {
		fmt.Println("📄 Traitement PDF...")
		rawText, extractErr = ExtractPDF(input.DocumentPath, input.Model, input.OllamaURL)
	} else {
		fmt.Println("🖼️  Traitement image...")
		rawText, extractErr = ExtractText(input.DocumentPath, input.Model, input.OllamaURL)
	}
	if extractErr != nil {
		fmt.Fprintf(os.Stderr, "Erreur extraction: %v\n", extractErr)
		os.Exit(1)
	}

	fmt.Println("─── Texte OCR extrait ──────────────────")
	fmt.Println(rawText)
	fmt.Println("────────────────────────────────────────\n")

	result := AnalyzeText(rawText, input)
	printResult(result)

	if !result.OK {
		os.Exit(1)
	}
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func readArg(arg string) ([]byte, error) {
	if _, err := os.Stat(arg); err == nil {
		return os.ReadFile(arg)
	}
	return []byte(arg), nil
}

func validateInput(input Input) error {
	if input.Name == "" {
		return fmt.Errorf("champ 'name' requis")
	}
	if input.DocumentPath == "" {
		return fmt.Errorf("champ 'document_path' requis")
	}
	if _, err := os.Stat(input.DocumentPath); err != nil {
		return fmt.Errorf("fichier document introuvable : %s", input.DocumentPath)
	}
	return nil
}

func docTypeLabel(dt DocType) string {
	labels := map[DocType]string{
		DocTypePassport: "📘 Passeport",
		DocTypeID:       "🪪 Carte Nationale d'Identité",
		DocTypeInvoice:  "🧾 Facture",
		DocTypeLetter:   "📄 Courrier",
		DocTypeUnknown:  "❓ Inconnu",
	}
	if l, ok := labels[dt]; ok {
		return l
	}
	return string(dt)
}

func printResult(r AnalysisResult) {
	fmt.Println("═══════════════════════════════════════")
	fmt.Println("           RÉSULTAT D'ANALYSE          ")
	fmt.Println("═══════════════════════════════════════")
	fmt.Printf("  %-22s %s\n", "Type document :", docTypeLabel(r.DocType))
	fmt.Println("───────────────────────────────────────")
	fmt.Printf("  %-22s %s\n", "Nom trouvé :", boolEmoji(r.NameFound))
	if r.HasSurname {
		fmt.Printf("  %-22s %s\n", "Prénom trouvé :", boolEmoji(r.SurnameFound))
	}
	if r.DocType != DocTypePassport && r.DocType != DocTypeID {
		fmt.Printf("  %-22s %s", "Code postal trouvé :", boolEmoji(r.PostalFound))
		if r.PostalMatch != "" {
			fmt.Printf("  (%s)", r.PostalMatch)
		}
		fmt.Println()
	}
	fmt.Printf("  %-22s %s\n", "Date trouvée :", boolEmoji(r.DateFound))
	if r.BirthDate != "" {
		fmt.Printf("  %-22s %s\n", "Date de naissance :", r.BirthDate)
	}
	if r.IssueDate != "" {
		fmt.Printf("  %-22s %s\n", "Date émission :", r.IssueDate)
	} else if r.DocDate != "" {
		fmt.Printf("  %-22s %s\n", "Date document :", r.DocDate)
	}
	if r.ExpiryDate != "" {
		fmt.Printf("  %-22s %s\n", "Date expiration :", r.ExpiryDate)
	}
	fmt.Printf("  %-22s %s\n", "Date valide :", boolEmoji(r.DateValid))
	fmt.Println("───────────────────────────────────────")
	if r.OK {
		fmt.Println("  ✅  DOCUMENT VALIDE")
	} else {
		fmt.Println("  ❌  DOCUMENT INVALIDE")
		for _, e := range r.Errors {
			fmt.Printf("      → %s\n", e)
		}
	}
	fmt.Println("═══════════════════════════════════════")
	jsonOut, _ := json.MarshalIndent(r, "", "  ")
	fmt.Printf("\nJSON:\n%s\n", string(jsonOut))
}

func boolEmoji(b bool) string {
	if b {
		return "✅"
	}
	return "❌"
}

func printUsage() {
	fmt.Fprintln(os.Stderr, "Usage: docverify <input.json>")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Checks disponibles : name, surname, postal, city, date, birth_date")
	fmt.Fprintln(os.Stderr, "Les checks non pertinents sont ignorés selon le type de document détecté :")
	fmt.Fprintln(os.Stderr, "  Facture   : postal + date requis, birth_date ignoré")
	fmt.Fprintln(os.Stderr, "  CNI/Passeport : birth_date requis, postal ignoré, date = non expiré")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Exemple (dossier famille avec référent) :")
	fmt.Fprintln(os.Stderr, `  {
    "individuals": [
      {
        "id": "1",
        "name": "ORBINEA", "surname": "CONCEPT",
        "postal_codes": ["99000", "99100"],
        "cities": ["VILLEFRANCHE"],
        "referent": true
      },
      {
        "id": "2",
        "name": "DELAUNAY", "surname": "CAMILLE",
        "birth_date": "19/11/2018"
      },
      {
        "id": "3",
        "name": "DELAUNAY", "surname": "LUCIE",
        "birth_date": "07/06/2013"
      }
    ],
    "documents": [
      {
        "id": "1",
        "path": "./facture.pdf",
        "individual": "1",
        "checks": ["name", "surname", "postal", "date", "birth_date"],
        "label": "Justificatif de domicile référent",
        "valid_months": 3
      },
      {
        "id": "2",
        "path": "./cni_camille.jpg",
        "individual": "2",
        "checks": ["name", "surname", "postal", "date", "birth_date"],
        "label": "CNI Camille"
      },
      {
        "id": "3",
        "path": "./cni_lucie.jpg",
        "individual": "3",
        "checks": ["name", "surname", "postal", "date", "birth_date"],
        "label": "CNI Lucie"
      }
    ],
    "model": "llama3.2-vision",
    "ollama_url": "http://localhost:11434"
  }`)
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Résultat attendu :")
	fmt.Fprintln(os.Stderr, "  Facture  → name ✅  surname ✅  postal ✅  date ✅  birth_date ignoré")
	fmt.Fprintln(os.Stderr, "  CNI      → name ✅  surname ✅  postal ignoré  date=non expiré ✅  birth_date ✅")
}
