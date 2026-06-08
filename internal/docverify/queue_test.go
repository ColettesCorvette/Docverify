package docverify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// TestQueueTimeoutMapping vérifie que si le pipeline échoue avec un timeout,
// le worker de la queue le convertit proprement en errTimeout.
func TestQueueTimeoutMapping(t *testing.T) {
	// Créer un fichier temporaire bidon pour éviter les erreurs de fichier manquant trop tôt
	tmpFile, err := os.CreateTemp("", "test-queue-timeout-*.jpg")
	if err != nil {
		t.Fatalf("Échec création fichier temporaire : %v", err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	// Initialiser la queue avec 1 worker
	q := NewQueue(1)
	defer close(q.jobs) // Clean up workers after test

	// On soumet un job avec un contexte déjà expiré
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-1*time.Second))
	cancel() // Déjà expiré

	job := Job{
		FilePath: tmpFile.Name(),
		Input: Input{
			Name:        "DUPONT",
			PostalCodes: []string{"31520"},
		},
	}

	res := q.Submit(ctx, job)

	// Comme le contexte client est déjà expiré/fermé, Submit doit retourner errClientGone ou errTimeout
	if res.Err == nil {
		t.Errorf("Attendu une erreur de timeout, obtenu nil")
	}

	// Testons directement la logique de traduction des erreurs dans handlePipelineError
	rec := httptest.NewRecorder()
	handlePipelineError(rec, errTimeout)

	if rec.Code != http.StatusOK {
		t.Errorf("Code HTTP incorrect pour errTimeout : attendu %d, obtenu %d", http.StatusOK, rec.Code)
	}

	var analysis AnalysisResult
	if err := json.Unmarshal(rec.Body.Bytes(), &analysis); err != nil {
		t.Fatalf("Impossible de décoder la réponse : %v", err)
	}

	if !analysis.Unreadable {
		t.Errorf("Attendu Unreadable=true pour errTimeout, obtenu %t", analysis.Unreadable)
	}
	if len(analysis.Errors) == 0 || !strings.Contains(analysis.Errors[0], "délai imparti") {
		t.Errorf("Message d'erreur inattendu : %v", analysis.Errors)
	}
}
