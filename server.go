package main

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
)

// defaultOllamaURL retourne l'URL de l'endpoint LLM (OpenAI-compatible) par défaut.
// Variable d'env : LLM_URL. Fallback : vllm-mlx local sur :8000.
func defaultOllamaURL() string {
	if url := os.Getenv("LLM_URL"); url != "" {
		return url
	}
	return "http://localhost:8000"
}

func startServer() {
	http.HandleFunc("/health", handleHealth)
	http.HandleFunc("/check", handleCheck)
	http.HandleFunc("/batch", handleBatch)
	log.Printf("Serveur docverify sur :8680 (LLM=%s)", defaultOllamaURL())
	log.Fatal(http.ListenAndServe(":8680", nil))
}

// --- Health check (GET /health) -------------------------------------------

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// writeJSONError renvoie une erreur au format JSON (compatible jq)
func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{
		"ok":    false,
		"error": msg,
	})
}

// --- Check single document (POST /check) ----------------------------------

func handleCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "Méthode non autorisée")
		return
	}

	// Parser le formulaire (max 10 Mo)
	err := r.ParseMultipartForm(10 << 20)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "Erreur lors de l'analyse du formulaire")
		return
	}

	// Récupérer le fichier
	file, header, err := r.FormFile("doc")
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "Erreur lors de la récupération du fichier")
		return
	}
	defer file.Close()

	// Sauvegarder dans /tmp
	destinationPath := filepath.Join("/tmp", header.Filename)
	dst, err := os.Create(destinationPath)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "Erreur lors de la création du fichier sur le serveur")
		return
	}
	defer dst.Close()

	_, err = io.Copy(dst, file)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "Erreur lors de la sauvegarde du fichier")
		return
	}
	dst.Close()

	// Modèle et URL Ollama (optionnels, avec défauts)
	model := r.FormValue("model")
	if model == "" {
		model = defaultModel
	}
	ollamaURL := r.FormValue("ollama_url")
	if ollamaURL == "" {
		ollamaURL = defaultOllamaURL()
	}

	// --- Pipeline OCR ---
	var rawText string
	var extractErr error

	if IsPDF(destinationPath) {
		rawText, extractErr = ExtractPDF(destinationPath, model, ollamaURL)
	} else {
		rawText, extractErr = ExtractText(destinationPath, model, ollamaURL)
	}

	// Nettoyage du fichier temporaire
	os.Remove(destinationPath)

	if extractErr != nil {
		writeJSONError(w, http.StatusInternalServerError, "Erreur OCR: "+extractErr.Error())
		return
	}

	// --- Analyse ---
	input := Input{
		Name:        r.FormValue("name"),
		Surname:     r.FormValue("surname"),
		PostalCodes: []string{r.FormValue("postal_code")},
		ValidMonths: 3,
		Model:       model,
		OllamaURL:   ollamaURL,
	}

	result := AnalyzeText(rawText, input)

	// Supprimer le texte OCR brut de la réponse (RGPD)
	result.RawText = ""

	// --- Réponse JSON ---
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// --- Batch check (POST /batch) --------------------------------------------

func handleBatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "Méthode non autorisée")
		return
	}

	// Parser le formulaire (max 50 Mo pour plusieurs fichiers)
	err := r.ParseMultipartForm(50 << 20)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "Erreur lors de l'analyse du formulaire")
		return
	}

	// Récupérer le JSON de configuration batch
	configJSON := r.FormValue("config")
	if configJSON == "" {
		writeJSONError(w, http.StatusBadRequest, "Champ 'config' requis (JSON batch)")
		return
	}

	var batch BatchInput
	if err := json.Unmarshal([]byte(configJSON), &batch); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Erreur parsing JSON config: "+err.Error())
		return
	}

	if batch.Model == "" {
		batch.Model = defaultModel
	}
	if batch.OllamaURL == "" {
		batch.OllamaURL = defaultOllamaURL()
	}

	// Index individus
	indivIdx := map[string]Individual{}
	for _, ind := range batch.Individuals {
		indivIdx[ind.ID] = ind
	}

	// Récupérer les fichiers uploadés et les sauvegarder dans /tmp
	savedFiles := map[string]string{} // docID → chemin temporaire
	for _, doc := range batch.Documents {
		file, header, err := r.FormFile("doc_" + doc.ID)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "Fichier manquant pour document "+doc.ID+" (champ 'doc_"+doc.ID+"')")
			return
		}
		defer file.Close()

		tmpPath := filepath.Join("/tmp", header.Filename)
		dst, err := os.Create(tmpPath)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "Erreur création fichier temporaire")
			return
		}

		_, err = io.Copy(dst, file)
		dst.Close()
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "Erreur sauvegarde fichier")
			return
		}

		savedFiles[doc.ID] = tmpPath
	}

	// --- Pipeline pour chaque document ---
	var results []DocResult
	allOK := true

	for _, doc := range batch.Documents {
		if doc.ValidMonths <= 0 {
			doc.ValidMonths = 3
		}
		ind := indivIdx[doc.Individual]
		docPath := savedFiles[doc.ID]

		// OCR
		var rawText string
		var extractErr error
		if IsPDF(docPath) {
			rawText, extractErr = ExtractPDF(docPath, batch.Model, batch.OllamaURL)
		} else {
			rawText, extractErr = ExtractText(docPath, batch.Model, batch.OllamaURL)
		}

		// Nettoyage du fichier temporaire
		os.Remove(docPath)

		if extractErr != nil {
			res := DocResult{
				DocumentID:   doc.ID,
				Label:        doc.Label,
				IndividualID: doc.Individual,
				Checks:       doc.Checks,
				OK:           false,
				Errors:       []string{"Erreur OCR: " + extractErr.Error()},
			}
			results = append(results, res)
			allOK = false
			continue
		}

		// Analyse
		input := buildInput(ind, doc, batch)
		analysis := AnalyzeText(rawText, input)

		// Supprimer le texte OCR brut (RGPD)
		analysis.RawText = ""

		// Évaluer les checks
		res := evaluateChecks(analysis, doc, ind)
		results = append(results, res)
		if !res.OK {
			allOK = false
		}
	}

	// Nettoyage des fichiers restants
	for _, path := range savedFiles {
		os.Remove(path)
	}

	// --- Réponse JSON ---
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"ok":      allOK,
		"results": results,
	})
}
