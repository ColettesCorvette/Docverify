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
	"strings"
	"time"
)

const DefaultModel = "mlx-community/Qwen2.5-VL-7B-Instruct-4bit"

// --- Format OpenAI-compatible (vllm-mlx, vLLM, etc. via /v1) ---------------

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
// les connexions TCP vers vllm-mlx (keepalive). Le timeout par requête est
// piloté par le contexte transmis, pas par le client.
var llmHTTPClient = &http.Client{
	Transport: &http.Transport{
		MaxIdleConns:        10,
		MaxIdleConnsPerHost: 5,
		IdleConnTimeout:     90 * time.Second,
	},
}

// Anti-contamination : aucun nonce dans le prompt. L'isolation entre requêtes
// repose entièrement sur --disable-prefix-cache côté vllm-mlx (voir
// deploy/vm/com.orbinea.vllm-mlx.plist). Sans ce flag, le prefix-cache multimodal
// réutilise le KV des placeholders image entre requêtes au prompt identique et
// fait FUITER le contenu d'une image sur la suivante (ex. solange transcrite avec
// les données du passeport cni8493). Un nonce [req:N] avait été essayé mais ne
// suffit pas seul sous rafale : NE PAS réactiver le prefix-cache sans rétablir
// une parade équivalente.

// RunLLM appelle l'endpoint OpenAI-compatible (vllm-mlx, vLLM, etc.)
// pour transcrire l'image fournie. Le contexte propage l'annulation côté
// client HTTP et le timeout global du job.
func RunLLM(ctx context.Context, imagePath, model, llmURL string) (string, error) {
	if err := pingLLM(ctx, llmURL); err != nil {
		return "", fmt.Errorf("Endpoint LLM non joignable (%s): %w\n"+
			"  → Vérifiez : curl %s/v1/models", llmURL, err, llmURL)
	}

	processedPath, cleanup, _ := preprocessForLLM(imagePath)
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
	fmt.Printf("   Image encodée : %.1f Ko\n", float64(len(imgB64))/1024)

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
		MaxTokens:   1000,
		Temperature: 0,
		Stream:      false,
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

	fmt.Printf("   OCR terminé (%d caractères)\n", len(text))
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
	log.Printf("LLM erreur transient (%v) — retry une fois", err)
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
// après crash vllm-mlx, reset TCP, broken pipe. Les timeouts contextuels ne
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

// pingLLM vérifie que l'endpoint LLM répond sur /v1/models. Hérite du
// contexte parent pour pouvoir être interrompu si le client HTTP raccroche
// avant même que le LLM ne soit sollicité.
func pingLLM(ctx context.Context, llmURL string) error {
	// 15 s (et non 5 s) pour tolérer un démarrage à froid de vllm-mlx : juste
	// après un boot/recapture, le serveur met quelques secondes à répondre sur
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
