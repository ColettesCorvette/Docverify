package docverify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// ScanResult contient le verdict de l'antivirus.
type ScanResult struct {
	Clean    bool          `json:"clean"`
	Threat   string        `json:"threat,omitempty"` // nom de la menace si infecté
	Duration time.Duration `json:"-"`
}

// Scanner est l'interface implémentée par n'importe quel backend AV (Avast, ClamAV, etc.).
type Scanner interface {
	Scan(data []byte) (*ScanResult, error)
	Ping() error
}

// --- Implémentation Avast ---

// AvastScanner dialogue avec le pod Avast en HTTP.
// L'endpoint et le protocole exacts seront ajustés une fois la licence et la doc d'image disponibles.
type AvastScanner struct {
	BaseURL    string
	HTTPClient *http.Client
}

// NewAvastScanner crée un scanner pointant vers le Service ClusterIP Avast.
// Par défaut : lit la variable d'env AVAST_URL, fallback sur http://avast-svc:8080.
func NewAvastScanner() *AvastScanner {
	base := os.Getenv("AVAST_URL")
	if base == "" {
		base = "http://avast-svc:8080"
	}
	return &AvastScanner{
		BaseURL: base,
		HTTPClient: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

// Ping vérifie que le pod Avast est joignable.
func (a *AvastScanner) Ping() error {
	resp, err := a.HTTPClient.Get(a.BaseURL + "/health")
	if err != nil {
		return fmt.Errorf("avast: ping échoué: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("avast: ping a renvoyé %d", resp.StatusCode)
	}
	return nil
}

// Scan envoie les octets du fichier au pod Avast et retourne le verdict.
//
// TODO: adapter le format de requête (multipart, body brut, wrapper JSON…)
// dès que la doc Avast/l'image seront disponibles.
func (a *AvastScanner) Scan(data []byte) (*ScanResult, error) {
	start := time.Now()

	req, err := http.NewRequest(http.MethodPost, a.BaseURL+"/scan", bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("avast: construction requête: %w", err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := a.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("avast: requête échouée: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("avast: lecture réponse: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("avast: HTTP %d — %s", resp.StatusCode, string(body))
	}

	// TODO: parser le vrai format de réponse Avast.
	// Pour l'instant, on attend : { "clean": true/false, "threat": "..." }
	var result ScanResult
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("avast: parsing réponse: %w", err)
	}

	result.Duration = time.Since(start)
	return &result, nil
}

// --- Scanner Noop (pour dev/test sans pod AV) ---

// NoopScanner renvoie toujours "clean". À utiliser en dev quand aucun pod AV ne tourne.
type NoopScanner struct{}

func (n *NoopScanner) Scan(_ []byte) (*ScanResult, error) {
	return &ScanResult{Clean: true}, nil
}

func (n *NoopScanner) Ping() error {
	return nil
}
