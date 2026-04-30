package docverify

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
)

// DefaultLLMURL retourne l'URL de l'endpoint LLM OpenAI-compatible par défaut.
// Variable d'env : LLM_URL. Fallback : vllm-mlx local sur :8000.
// Utilisé à la fois par le serveur HTTP et le CLI.
func DefaultLLMURL() string {
	if url := os.Getenv("LLM_URL"); url != "" {
		return url
	}
	return "http://localhost:8000"
}

// StartServer démarre le serveur HTTP docverify sur :8680.
//
// Utilise le ServeMux Go 1.22 (patterns "METHOD /path/{var}").
// V1 sans AuthMiddleware ni RBAC : docverify est un service interne
// appelé uniquement par Regia (réseau K8s). À ajouter pour la V2.
func StartServer() {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", handleHealth)
	mux.HandleFunc("POST /api/v1/supporting-documents", handlePostSupportingDocument)

	// --- Routes legacy (dev / testclient / bench) -------------------------
	// Décommenter pour réactiver les anciens points d'entrée multipart.
	// mux.HandleFunc("POST /check", handleCheck)
	// mux.HandleFunc("POST /batch", handleBatch)

	log.Printf("Serveur docverify sur :8680 (LLM=%s)", DefaultLLMURL())
	log.Fatal(http.ListenAndServe(":8680", mux))
}

// --- Health check ---------------------------------------------------------

func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// writeJSONError renvoie une erreur au format JSON.
func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{
		"ok":    false,
		"error": msg,
	})
}

// --- API v1 : Supporting Documents ----------------------------------------

// POST /api/v1/supporting-documents — upload + analyse d'un justificatif.
//
// Route principale appelée par Regia. Reçoit un multipart, lance le pipeline
// OCR, renvoie le verdict.
//
// Champs multipart attendus :
//   - doc          : le fichier (PDF ou image)
//   - name         : nom attendu (fourni par Regia depuis sa BDD)
//   - surname      : prénom attendu (idem)
//   - postal_code  : code postal attendu (idem)
//   - model        : override du modèle LLM (optionnel)
//   - llm_url      : override de l'endpoint LLM (optionnel)
func handlePostSupportingDocument(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Erreur lors de l'analyse du formulaire")
		return
	}

	file, header, err := r.FormFile("doc")
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "Champ 'doc' manquant")
		return
	}
	defer file.Close()

	destinationPath := filepath.Join("/tmp", header.Filename)
	dst, err := os.Create(destinationPath)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "Erreur création fichier temporaire")
		return
	}
	if _, err := io.Copy(dst, file); err != nil {
		dst.Close()
		os.Remove(destinationPath)
		writeJSONError(w, http.StatusInternalServerError, "Erreur sauvegarde fichier")
		return
	}
	dst.Close()
	defer os.Remove(destinationPath)

	model := r.FormValue("model")
	if model == "" {
		model = DefaultModel
	}
	llmURL := r.FormValue("llm_url")
	if llmURL == "" {
		llmURL = DefaultLLMURL()
	}

	var rawText string
	var extractErr error
	if IsPDF(destinationPath) {
		rawText, extractErr = ExtractPDF(destinationPath, model, llmURL)
	} else {
		rawText, extractErr = ExtractText(destinationPath, model, llmURL)
	}
	if extractErr != nil {
		writeJSONError(w, http.StatusInternalServerError, "Erreur OCR: "+extractErr.Error())
		return
	}

	input := Input{
		Name:        r.FormValue("name"),
		Surname:     r.FormValue("surname"),
		PostalCodes: []string{r.FormValue("postal_code")},
		ValidMonths: 3,
		Model:       model,
		LLMURL:      llmURL,
	}
	result := AnalyzeText(rawText, input)
	result.RawText = "" // RGPD

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// --- Routes legacy (dev) --------------------------------------------------
//
// handleCheck et handleBatch sont conservés pour le dev local (testclient,
// bench-batch). Désenregistrés du mux par défaut — voir StartServer.
//
/*
func handleCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "Méthode non autorisée")
		return
	}

	err := r.ParseMultipartForm(10 << 20)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "Erreur lors de l'analyse du formulaire")
		return
	}

	file, header, err := r.FormFile("doc")
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "Erreur lors de la récupération du fichier")
		return
	}
	defer file.Close()

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

	model := r.FormValue("model")
	if model == "" {
		model = DefaultModel
	}
	llmURL := r.FormValue("llm_url")
	if llmURL == "" {
		llmURL = DefaultLLMURL()
	}

	var rawText string
	var extractErr error

	if IsPDF(destinationPath) {
		rawText, extractErr = ExtractPDF(destinationPath, model, llmURL)
	} else {
		rawText, extractErr = ExtractText(destinationPath, model, llmURL)
	}

	os.Remove(destinationPath)

	if extractErr != nil {
		writeJSONError(w, http.StatusInternalServerError, "Erreur OCR: "+extractErr.Error())
		return
	}

	input := Input{
		Name:        r.FormValue("name"),
		Surname:     r.FormValue("surname"),
		PostalCodes: []string{r.FormValue("postal_code")},
		ValidMonths: 3,
		Model:       model,
		LLMURL:      llmURL,
	}

	result := AnalyzeText(rawText, input)
	result.RawText = ""

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func handleBatch(w http.ResponseWriter, r *http.Request) {
	err := r.ParseMultipartForm(50 << 20)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "Erreur lors de l'analyse du formulaire")
		return
	}

	configJSON := r.FormValue("config")
	if configJSON == "" {
		writeJSONError(w, http.StatusBadRequest, "Champ 'config' requis JSON batch")
		return
	}

	var batch BatchInput
	if err := json.Unmarshal([]byte(configJSON), &batch); err != nil {
		writeJSONError(w, http.StatusBadRequest, "Erreur parsing JSON config: "+err.Error())
		return
	}

	if batch.Model == "" {
		batch.Model = DefaultModel
	}
	if batch.LLMURL == "" {
		batch.LLMURL = DefaultLLMURL()
	}

	indivIdx := map[string]Individual{}
	for _, ind := range batch.Individuals {
		indivIdx[ind.ID] = ind
	}

	savedFiles := map[string]string{}
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

	var results []DocResult
	allOK := true

	for _, doc := range batch.Documents {
		if doc.ValidMonths <= 0 {
			doc.ValidMonths = 3
		}
		ind := indivIdx[doc.Individual]
		docPath := savedFiles[doc.ID]

		var rawText string
		var extractErr error
		if IsPDF(docPath) {
			rawText, extractErr = ExtractPDF(docPath, batch.Model, batch.LLMURL)
		} else {
			rawText, extractErr = ExtractText(docPath, batch.Model, batch.LLMURL)
		}

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

		input := BuildInput(ind, doc, batch)
		analysis := AnalyzeText(rawText, input)
		analysis.RawText = ""

		res := EvaluateChecks(analysis, doc, ind)
		results = append(results, res)
		if !res.OK {
			allOK = false
		}
	}

	for _, path := range savedFiles {
		os.Remove(path)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"ok":      allOK,
		"results": results,
	})
}
*/
