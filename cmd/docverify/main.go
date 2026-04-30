package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	dc "docverify/internal/docverify"
)

// ── Point d'entrée ────────────────────────────────────────────────────────────

func main() {
	if len(os.Args) < 2 {
		// Pas d'argument → mode serveur API
		dc.StartServer()
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
	var batch dc.BatchInput
	if err := json.Unmarshal(data, &batch); err != nil {
		fmt.Fprintf(os.Stderr, "Erreur parsing JSON batch: %v\n", err)
		os.Exit(1)
	}

	if batch.Model == "" {
		batch.Model = dc.DefaultModel
	}
	if batch.LLMURL == "" {
		batch.LLMURL = dc.DefaultLLMURL()
	}

	indivIdx := map[string]dc.Individual{}
	for _, ind := range batch.Individuals {
		indivIdx[ind.ID] = ind
	}

	if err := validateBatch(batch, indivIdx); err != nil {
		fmt.Fprintf(os.Stderr, "Input invalide: %v\n", err)
		os.Exit(1)
	}

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

	var results []dc.DocResult
	allOK := true
	var referentAddr *dc.ReferentAddress

	for _, doc := range batch.Documents {
		if doc.ValidMonths <= 0 {
			doc.ValidMonths = 3
		}
		ind := indivIdx[doc.Individual]

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

		var rawText string
		var extractErr error
		if dc.IsPDF(doc.Path) {
			fmt.Println("│  📄 Traitement PDF...")
			rawText, extractErr = dc.ExtractPDF(doc.Path, batch.Model, batch.LLMURL)
		} else {
			fmt.Println("│  🖼️  Traitement image...")
			rawText, extractErr = dc.ExtractText(doc.Path, batch.Model, batch.LLMURL)
		}

		if extractErr != nil {
			res := dc.DocResult{
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

		input := dc.BuildInput(ind, doc, batch)
		analysis := dc.AnalyzeText(rawText, input)

		if addressFromReferent && referentAddr != nil {
			analysis.PostalFound = true
			analysis.PostalMatch = referentAddr.PostalMatch + " (référent)"
		}

		res := dc.EvaluateChecks(analysis, doc, ind)
		res.AddressFromReferent = addressFromReferent
		results = append(results, res)
		if !res.OK {
			allOK = false
		}

		if ind.Referent && (checksSet["postal"] || checksSet["city"]) {
			if analysis.PostalFound && analysis.PostalMatch != "" {
				referentAddr = &dc.ReferentAddress{PostalMatch: analysis.PostalMatch}
				fmt.Printf("│  📍 Adresse référent mémorisée (%s)\n", referentAddr.PostalMatch)
			}
		}

		printDocResult(res)
		fmt.Println("└─────────────────────────────────────────")
	}

	printBatchSummary(results, batch.Individuals, allOK)

	out, _ := json.MarshalIndent(map[string]any{
		"ok":      allOK,
		"results": results,
	}, "", "  ")
	fmt.Printf("\nJSON:\n%s\n", string(out))

	if !allOK {
		os.Exit(1)
	}
}

// ── Mode legacy ───────────────────────────────────────────────────────────────

func runLegacy(data []byte) {
	var input dc.Input
	if err := json.Unmarshal(data, &input); err != nil {
		fmt.Fprintf(os.Stderr, "Erreur parsing JSON: %v\n", err)
		os.Exit(1)
	}
	if input.ValidMonths <= 0 {
		input.ValidMonths = 3
	}
	if input.Model == "" {
		input.Model = dc.DefaultModel
	}
	if input.LLMURL == "" {
		input.LLMURL = dc.DefaultLLMURL()
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
	fmt.Printf("   Modèle LLM     : %s\n\n", input.Model)

	var rawText string
	var extractErr error
	if dc.IsPDF(input.DocumentPath) {
		fmt.Println("📄 Traitement PDF...")
		rawText, extractErr = dc.ExtractPDF(input.DocumentPath, input.Model, input.LLMURL)
	} else {
		fmt.Println("🖼️  Traitement image...")
		rawText, extractErr = dc.ExtractText(input.DocumentPath, input.Model, input.LLMURL)
	}
	if extractErr != nil {
		fmt.Fprintf(os.Stderr, "Erreur extraction: %v\n", extractErr)
		os.Exit(1)
	}

	fmt.Println("─── Texte OCR extrait ──────────────────")
	fmt.Println(rawText)
	fmt.Println("────────────────────────────────────────")
	fmt.Println()

	result := dc.AnalyzeText(rawText, input)
	printResult(result)

	if !result.OK {
		os.Exit(1)
	}
}

// ── Helpers CLI ───────────────────────────────────────────────────────────────

func readArg(arg string) ([]byte, error) {
	if _, err := os.Stat(arg); err == nil {
		return os.ReadFile(arg)
	}
	return []byte(arg), nil
}

func validateInput(input dc.Input) error {
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

func validateBatch(batch dc.BatchInput, indivIdx map[string]dc.Individual) error {
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

func docTypeLabel(dt dc.DocType) string {
	labels := map[dc.DocType]string{
		dc.DocTypePassport: "📘 Passeport",
		dc.DocTypeID:       "🪪 Carte Nationale d'Identité",
		dc.DocTypeInvoice:  "🧾 Facture",
		dc.DocTypeLetter:   "📄 Courrier",
		dc.DocTypeUnknown:  "❓ Inconnu",
	}
	if l, ok := labels[dt]; ok {
		return l
	}
	return string(dt)
}

func boolEmoji(b bool) string {
	if b {
		return "✅"
	}
	return "❌"
}

func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s + strings.Repeat(" ", n-len(runes))
	}
	return string(runes[:n-1]) + "…"
}

// ── Affichage ─────────────────────────────────────────────────────────────────

func printDocResult(r dc.DocResult) {
	isIdentityDoc := r.Analysis.DocType == dc.DocTypePassport || r.Analysis.DocType == dc.DocTypeID
	isAddressDoc := r.Analysis.DocType == dc.DocTypeInvoice || r.Analysis.DocType == dc.DocTypeLetter

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

func printBatchSummary(results []dc.DocResult, individuals []dc.Individual, allOK bool) {
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

func printResult(r dc.AnalysisResult) {
	fmt.Println("═══════════════════════════════════════")
	fmt.Println("           RÉSULTAT D'ANALYSE          ")
	fmt.Println("═══════════════════════════════════════")
	fmt.Printf("  %-22s %s\n", "Type document :", docTypeLabel(r.DocType))
	fmt.Println("───────────────────────────────────────")
	fmt.Printf("  %-22s %s\n", "Nom trouvé :", boolEmoji(r.NameFound))
	if r.HasSurname {
		fmt.Printf("  %-22s %s\n", "Prénom trouvé :", boolEmoji(r.SurnameFound))
	}
	if r.DocType != dc.DocTypePassport && r.DocType != dc.DocTypeID {
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
