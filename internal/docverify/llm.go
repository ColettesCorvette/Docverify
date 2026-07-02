package docverify

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const DefaultModel = "huggingface.co/unsloth/gemma-4-e4b-it-gguf:Q5_K_M"

// --- Format OpenAI-compatible, via /v1 (Docker Model Runner, vLLM, etc.) ---

type chatMessage struct {
	Role    string        `json:"role"`
	Content []contentPart `json:"content"`
}

type contentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *imageURL `json:"image_url,omitempty"`
}

type imageURL struct {
	URL string `json:"url"`
}

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	MaxTokens   int           `json:"max_tokens"`
	Temperature float64       `json:"temperature"`
	Stream      bool          `json:"stream"`
	// FrequencyPenalty pénalise un token proportionnellement au nombre de fois
	// où il est déjà apparu. Sans ça, Gemma 4 part en boucle sur le remplissage
	// `<` des MRZ et brûle tout le budget de tokens avant d'atteindre le verso
	// d'une pièce d'identité, où se trouve la date d'expiration. Observé sur le
	// sample solange. 0.3 suffit à casser la boucle sans dégrader les autres
	// transcriptions ; à 1.0 le modèle altère des chiffres de date. Paramètre
	// standard OpenAI, honoré par llama.cpp comme par vLLM.
	FrequencyPenalty float64 `json:"frequency_penalty,omitempty"`
	// ChatTemplateKwargs désactive le mode raisonnement des modèles qui en ont
	// un, comme Gemma 4. Sans ça ils consomment tous les tokens en réflexion et
	// renvoient un content vide. Ignoré sans effet par les modèles non concernés
	// comme Gemma 3. Tâche OCR : on veut la transcription, pas du raisonnement.
	ChatTemplateKwargs map[string]any `json:"chat_template_kwargs,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// llmHTTPClient est partagé entre tous les appels RunLLM pour réutiliser
// les connexions TCP vers le backend LLM (keepalive). Le timeout par requête
// est piloté par le contexte transmis, pas par le client.
var llmHTTPClient = &http.Client{
	Transport: &http.Transport{
		MaxIdleConns:        10,
		MaxIdleConnsPerHost: 5,
		IdleConnTimeout:     90 * time.Second,
	},
}

// llmSemaphore borne le nombre d'inférences LLM concurrentes. Sans cette
// limite, N workers x M étapes du pipeline peuvent submerger le backend DMR
// (llama.cpp GPU Metal), qui n'a pas de queue élastique interne. Initialisé
// par InitLLMSemaphore, appelé au démarrage du serveur.
var llmSemaphore chan struct{}

var (
	detectedLLMURL    string
	detectedModelName string
)

// InitLLMSemaphore crée le sémaphore qui borne les appels LLM concurrents.
// La capacité est lue depuis LLM_MAX_CONCURRENT (défaut 1, aligné sur la prod :
// un seul GPU Metal ne parallélise pas). Doit être appelé une fois, avant tout
// appel à RunLLM.
func InitLLMSemaphore() {
	n := 1
	if v := os.Getenv("LLM_MAX_CONCURRENT"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			n = parsed
		}
	}
	llmSemaphore = make(chan struct{}, n)
	log.Printf("Sémaphore LLM : max %d inférences concurrentes", n)
}

// InitLLM initialise le sémaphore LLM et auto-détecte le backend LLM si nécessaire.
func InitLLM() {
	InitLLMSemaphore()

	// Si LLM_URL et LLM_MODEL sont tous deux définis dans l'environnement, pas besoin d'auto-détection.
	if os.Getenv("LLM_URL") != "" && os.Getenv("LLM_MODEL") != "" {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	candidates := []string{
		"http://192.168.65.254:12434/engines",   // Docker Desktop host-gateway (Mac)
		"http://host.lima.internal:12434/engines", // VM Lima k3s (Mac VM)
		"http://localhost:12434/engines",        // DMR local (Mac host)
		"http://localhost:8000",                 // vLLM local (Linux host/hostNetwork)
		"http://vllm:8000",                      // Service vLLM K8s (Linux)
	}

	// Si LLM_URL est défini, c'est l'unique candidat à tester pour le modèle
	if url := os.Getenv("LLM_URL"); url != "" {
		candidates = []string{url}
	}

	client := &http.Client{Timeout: 500 * time.Millisecond}

	for _, baseURL := range candidates {
		url := strings.TrimRight(baseURL, "/") + "/v1/models"
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			detectedLLMURL = baseURL
			log.Printf("[llm-detect] Backend LLM détecté : %s", baseURL)

			var modelResp struct {
				Data []struct {
					ID string `json:"id"`
				} `json:"data"`
			}
			if bodyBytes, rerr := io.ReadAll(resp.Body); rerr == nil {
				if merr := json.Unmarshal(bodyBytes, &modelResp); merr == nil && len(modelResp.Data) > 0 {
					detectedModelName = modelResp.Data[0].ID
					log.Printf("[llm-detect] Modèle actif détecté : %s", detectedModelName)
				}
			}
			return
		}
	}

	log.Printf("[llm-detect] Aucun backend LLM actif détecté. Fallbacks par défaut utilisés.")
}

// Anti-contamination : aucun nonce dans le prompt. Le backend est désormais
// Docker Model Runner (llama.cpp), différent moteur que l'ancien vllm-mlx.
// Garde-fou conservé : `concurrency-test.sh` vérifie sous rafale qu'aucune
// réponse ne fuit le contenu d'une autre requête. Historique : sur vllm-mlx,
// le prefix-cache multimodal réutilisait le KV des placeholders image et
// faisait FUITER une image sur la suivante ; la parade était
// --disable-prefix-cache. Si une contamination réapparaît sous DMR, traiter
// le cache de prompt du backend de la même façon avant tout retour au nonce.

// RunLLM appelle l'endpoint OpenAI-compatible (Docker Model Runner, vLLM, etc.)
// pour transcrire l'image fournie. Le contexte propage l'annulation côté
// client HTTP et le timeout global du job.
func RunLLM(ctx context.Context, imagePath, model, llmURL string) (string, error) {
	if llmSemaphore != nil {
		select {
		case llmSemaphore <- struct{}{}:
			defer func() { <-llmSemaphore }()
		case <-ctx.Done():
			return "", fmt.Errorf("annulé en attente du sémaphore LLM: %w", ctx.Err())
		}
	}

	processedPath, cleanup, _ := preprocessForLLM(ctx, imagePath)
	defer cleanup()

	if os.Getenv("DOCVERIFY_DEBUG_OCR") == "1" {
		dst := filepath.Join("/tmp", fmt.Sprintf("docverify-sent-%d%s", time.Now().UnixNano(), filepath.Ext(processedPath)))
		if data, rerr := os.ReadFile(processedPath); rerr == nil && os.WriteFile(dst, data, 0o644) == nil {
			log.Printf("[DEBUG OCR] image envoyée au LLM: %s (source: %s)", dst, imagePath)
		}
	}

	imgB64, err := encodeImageBase64(processedPath)
	if err != nil {
		return "", fmt.Errorf("encodage image: %w", err)
	}
	log.Printf("   Image encodée : %.1f Ko", float64(len(imgB64))/1024)

	dataURL := fmt.Sprintf("data:%s;base64,%s", mimeFromExt(processedPath), imgB64)

	payload := chatRequest{
		Model: model,
		Messages: []chatMessage{{
			Role: "user",
			Content: []contentPart{
				{Type: "text", Text: "Transcribe every word visible in this document image. Output only the raw text, no commentary."},
				{Type: "image_url", ImageURL: &imageURL{URL: dataURL}},
			},
		}},
		MaxTokens:        700,
		Temperature:      0,
		Stream:           false,
		FrequencyPenalty: 0.3,
		ChatTemplateKwargs: map[string]any{"enable_thinking": false},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	reqCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()

	apiURL := strings.TrimRight(llmURL, "/") + "/v1/chat/completions"

	resp, err := postLLMWithRetry(reqCtx, apiURL, body)
	if err != nil {
		if reqCtx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("timeout dépassé (3 min)")
		}
		if ctx.Err() != nil {
			return "", fmt.Errorf("requête annulée: %w", ctx.Err())
		}
		return "", fmt.Errorf("HTTP POST: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("LLM HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var chat chatResponse
	if err := json.Unmarshal(raw, &chat); err != nil {
		return "", fmt.Errorf("decode réponse: %w (body: %s)", err, string(raw))
	}

	if chat.Error != nil {
		return "", fmt.Errorf("erreur LLM: %s", chat.Error.Message)
	}
	if len(chat.Choices) == 0 {
		return "", fmt.Errorf("réponse vide (0 choices)")
	}

	text := strings.TrimSpace(chat.Choices[0].Message.Content)
	if text == "" {
		return "", fmt.Errorf("contenu vide — modèle '%s' mal chargé ?", model)
	}

	log.Printf("   OCR terminé (%d caractères)", len(text))
	return text, nil
}

// postLLMWithRetry envoie une requête POST au LLM et retente une fois en cas
// d'erreur transient typique d'un keepalive cassé : EOF, connection reset,
// broken pipe. Le retry est ignoré si le contexte est déjà annulé ou si
// l'erreur n'est pas marquée transient. Le body est re-emballé dans un
// bytes.Reader frais à chaque tentative.
func postLLMWithRetry(ctx context.Context, apiURL string, body []byte) (*http.Response, error) {
	resp, err := doLLMPost(ctx, apiURL, body)
	if err == nil {
		return resp, nil
	}
	if ctx.Err() != nil || !isRetryableLLMErr(err) {
		return nil, err
	}
	log.Printf("LLM erreur transient (%v) — retry dans 500ms", err)
	select {
	case <-time.After(500 * time.Millisecond):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return doLLMPost(ctx, apiURL, body)
}

func doLLMPost(ctx context.Context, apiURL string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return llmHTTPClient.Do(req)
}

// isRetryableLLMErr matche les erreurs transient connues : keepalive cassé
// après crash du backend, reset TCP, broken pipe. Les timeouts contextuels ne
// sont pas retentés.
func isRetryableLLMErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "EOF") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "use of closed network connection")
}

// PingLLM vérifie que l'endpoint LLM répond sur /v1/models. Exporté pour
// être utilisé au démarrage du serveur et dans le health check enrichi.
// N'est plus appelé à chaque requête RunLLM pour éviter de surcharger le DMR.
func PingLLM(ctx context.Context, llmURL string) error {
	// 15 s (et non 5 s) pour tolérer un démarrage à froid du backend : juste
	// après un démarrage, le serveur met quelques secondes à répondre sur
	// /v1/models, et un ping trop court renvoyait un faux « endpoint injoignable ».
	pingCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	url := strings.TrimRight(llmURL, "/") + "/v1/models"
	req, err := http.NewRequestWithContext(pingCtx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := llmHTTPClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

func encodeImageBase64(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(data), nil
}

func mimeFromExt(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png":
		return "image/png"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	default:
		return "image/jpeg"
	}
}
