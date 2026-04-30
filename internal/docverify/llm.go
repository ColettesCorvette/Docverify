package docverify

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const DefaultModel = "mlx-community/gemma-4-e4b-it-4bit"

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

// RunLLM appelle l'endpoint OpenAI-compatible (vllm-mlx, vLLM, etc.)
// pour transcrire l'image fournie.
func RunLLM(imagePath, model, llmURL string) (string, error) {
	if err := pingLLM(llmURL); err != nil {
		return "", fmt.Errorf("Endpoint LLM non joignable (%s): %w\n"+
			"  → Vérifiez : curl %s/v1/models", llmURL, err, llmURL)
	}

	imgB64, err := encodeImageBase64(imagePath)
	if err != nil {
		return "", fmt.Errorf("encodage image: %w", err)
	}
	fmt.Printf("   Image encodée : %.1f Ko\n", float64(len(imgB64))/1024)

	dataURL := fmt.Sprintf("data:%s;base64,%s", mimeFromExt(imagePath), imgB64)

	payload := chatRequest{
		Model: model,
		Messages: []chatMessage{{
			Role: "user",
			Content: []contentPart{
				{Type: "text", Text: "Transcribe every word visible in this document image. Output only the raw text, no commentary."},
				{Type: "image_url", ImageURL: &imageURL{URL: dataURL}},
			},
		}},
		MaxTokens:   500,
		Temperature: 0,
		Stream:      false,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	apiURL := strings.TrimRight(llmURL, "/") + "/v1/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	done := make(chan struct{})
	go spinner(done)

	resp, err := (&http.Client{}).Do(req)
	close(done)
	fmt.Print("\r")

	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("timeout dépassé (3 min)")
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

	fmt.Printf("   ✅ OCR terminé (%d caractères)                    \n", len(text))
	return text, nil
}

// spinner affiche une animation tant que done n'est pas fermé.
func spinner(done <-chan struct{}) {
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	i := 0
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			fmt.Printf("\r   %s Analyse OCR en cours...", frames[i%len(frames)])
			i++
		}
	}
}

// pingLLM vérifie que l'endpoint LLM répond sur /v1/models.
func pingLLM(llmURL string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url := strings.TrimRight(llmURL, "/") + "/v1/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
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
