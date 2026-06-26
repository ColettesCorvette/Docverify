package docverify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

// statusClientClosed est le code non-standard utilisé pour signaler qu'un
// client a fermé sa connexion avant la fin du traitement (convention nginx).
const statusClientClosed = 499

// Version du binaire, surchargeable au build via
// -ldflags "-X github.com/.../docverify.Version=...". Défaut en dev.
var Version = "dev"

// startTime sert à calculer l'uptime exposé par /health.
var startTime = time.Now()

// maxUploadBytes borne la taille acceptée d'un upload multipart. Au-delà,
// http.MaxBytesReader court-circuite la requête sans charger l'intégralité
// du body en mémoire.
const maxUploadBytes = 30 << 20

// DefaultLLMURL retourne l'URL de l'endpoint LLM OpenAI-compatible par défaut.
// Variable d'env : LLM_URL. Fallback : Docker Model Runner local.
// Utilisé à la fois par le serveur HTTP et le CLI.
func DefaultLLMURL() string {
	if url := os.Getenv("LLM_URL"); url != "" {
		return url
	}
	if detectedLLMURL != "" {
		return detectedLLMURL
	}
	return "http://localhost:12434/engines"
}

// DefaultModelName retourne le modèle LLM par défaut.
// Variable d'env : LLM_MODEL. Fallback : constante DefaultModel.
// Utilisé quand la requête ne précise pas de modèle, par le serveur et le CLI.
func DefaultModelName() string {
	if m := os.Getenv("LLM_MODEL"); m != "" {
		return m
	}
	if detectedModelName != "" {
		return detectedModelName
	}
	return DefaultModel
}

// StartServer démarre le serveur HTTP docverify sur :8680.
//
// Utilise le ServeMux et ses patterns méthode "METHOD /path/{var}", introduits en Go 1.22.
// V1 sans AuthMiddleware ni RBAC : docverify est un service interne
// appelé uniquement par Regia (réseau K8s). À ajouter pour la V2.
func StartServer() {
	InitLLM()

	llmURL := DefaultLLMURL()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	if err := PingLLM(ctx, llmURL); err != nil {
		log.Printf("Avertissement : LLM non joignable au demarrage (%s): %v", llmURL, err)
	}
	cancel()

	queue := NewQueue(DefaultWorkers())

	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", makeHealthHandler(queue))
	mux.HandleFunc("POST /api/v1/supporting-documents", makeSupportingDocumentHandler(queue))

	srv := &http.Server{
		Addr:              ":8680",
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      5 * time.Minute,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
		sig := <-sigCh
		log.Printf("Signal %v recu, arret gracieux en cours...", sig)
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer shutdownCancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("Erreur arret gracieux: %v", err)
		}
	}()

	log.Printf("Serveur docverify sur :8680 (LLM=%s)", llmURL)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatal(err)
	}
	log.Println("Serveur arrete proprement")
}

// --- Health check ---------------------------------------------------------

// makeHealthHandler renvoie le handler de /health, enrichi des métriques de la
// queue (workers, profondeur, in-flight) et d'infos de build, pour alimenter le
// dashboard de monitoring du cluster. Le code HTTP reste 200 (ok) / 503
// (degraded, LLM injoignable) — la readinessProbe k8s s'en sert.
func makeHealthHandler(q *Queue) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		st := q.Stats()
		body := map[string]any{
			"status":    "ok",
			"version":   Version,
			"uptime_s":  int(time.Since(startTime).Seconds()),
			"workers":   st.Workers,
			"queued":    st.Queued,
			"capacity":  st.Capacity,
			"inflight":  st.InFlight,
			"processed": st.Processed,
		}

		ctx, cancel := context.WithTimeout(r.Context(), 1500*time.Millisecond)
		defer cancel()

		w.Header().Set("Content-Type", "application/json")
		if err := PingLLM(ctx, DefaultLLMURL()); err != nil {
			body["status"] = "degraded"
			body["llm"] = "unreachable"
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(body)
			return
		}
		body["llm"] = "ok"
		json.NewEncoder(w).Encode(body)
	}
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
//   - callback_url : active le mode asynchrone (optionnel) — voir ci-dessous
//
// Par défaut (pas de callback_url), le handler dépose la requête dans la queue
// in-process et attend le verdict d'un worker (synchrone). Si callback_url est
// fourni, le handler répond immédiatement 202 + job_id et le verdict est POSTé
// à cette URL en fin de traitement (asynchrone par webhook).
func makeSupportingDocumentHandler(q *Queue) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
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

		dst, err := os.CreateTemp("", "docverify-*"+filepath.Ext(header.Filename))
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "Erreur création fichier temporaire")
			return
		}
		destinationPath := dst.Name()
		if _, err := io.Copy(dst, file); err != nil {
			dst.Close()
			os.Remove(destinationPath)
			writeJSONError(w, http.StatusInternalServerError, "Erreur sauvegarde fichier")
			return
		}
		dst.Close()

		model := r.FormValue("model")
		if model == "" {
			model = DefaultModelName()
		}
		llmURL := r.FormValue("llm_url")
		if llmURL == "" {
			llmURL = DefaultLLMURL()
		}

		job := Job{
			FilePath: destinationPath,
			Input: Input{
				Name:        r.FormValue("name"),
				Surname:     r.FormValue("surname"),
				PostalCodes: []string{r.FormValue("postal_code")},
				ValidMonths: 3,
				Model:       model,
				LLMURL:      llmURL,
			},
			Model:  model,
			LLMURL: llmURL,
		}

		jobID := newRequestID()

		// Mode asynchrone : si le client fournit un callback_url, on accepte tout
		// de suite (202 + job_id) et le verdict lui est POSTé en fin de traitement.
		// Le POST synchrone reste le défaut quand callback_url est absent.
		if callbackURL := r.FormValue("callback_url"); callbackURL != "" {
			if !validCallbackURL(callbackURL) {
				os.Remove(destinationPath)
				writeJSONError(w, http.StatusBadRequest, "callback_url invalide (http/https attendu)")
				return
			}
			go processAsync(q, job, jobID, callbackURL)
			log.Printf("[async %s] accepté — callback vers %s", jobID, callbackURL)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusAccepted)
			json.NewEncoder(w).Encode(map[string]string{"job_id": jobID, "status": "queued"})
			return
		}

		w.Header().Set("X-Request-Id", jobID)
		res := q.Submit(r.Context(), job)
		if res.Err != nil {
			handlePipelineError(w, res.Err)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(res.Analysis)
	}
}

// handlePipelineError traduit les erreurs queue en réponses HTTP utiles pour
// Regia. Le timeout pipeline devient un verdict unreadable HTTP 200 plutôt
// qu'un 500 : le doc fait crasher / saturer le LLM, mais le service répond
// correctement et Regia route vers la review manuelle.
func handlePipelineError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errClientGone):
		log.Printf("Client déconnecté avant la fin du pipeline: %v", err)
		w.WriteHeader(statusClientClosed)
	case errors.Is(err, errQueueFull):
		writeJSONError(w, http.StatusServiceUnavailable, err.Error())
	case errors.Is(err, errTimeout) || errors.Is(err, errQueueTimeout):
		log.Printf("Pipeline timeout (%v) — verdict unreadable renvoyé", err)
		unreadable := AnalysisResult{
			DocType:    DocTypeUnknown,
			Unreadable: true,
			Errors:     []string{"Document non analysable dans le délai imparti — review manuelle requise"},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(unreadable)
	default:
		writeJSONError(w, http.StatusInternalServerError, "Erreur pipeline: "+err.Error())
	}
}
