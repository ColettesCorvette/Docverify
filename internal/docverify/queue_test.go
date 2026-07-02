package docverify

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
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

// TestSubmitCleansFileOnClientGoneBeforeStart couvre le cas errClientGone
// avant que le job n'entre dans le channel : le fichier ne doit pas fuir.
func TestSubmitCleansFileOnClientGoneBeforeStart(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "test-cleanup-clientgone-*.jpg")
	if err != nil {
		t.Fatalf("création fichier temporaire: %v", err)
	}
	tmpFile.Close()

	// Channel non-bufferisé, 0 worker : q.jobs <- item ne sera jamais prêt.
	q := &Queue{jobs: make(chan queueItem)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // annulé avant l'appel : seul ce cas est prêt dans le select

	res := q.Submit(ctx, Job{FilePath: tmpFile.Name()})
	if res.Err == nil {
		t.Fatal("attendu une erreur (client parti), obtenu nil")
	}
	if _, err := os.Stat(tmpFile.Name()); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("fichier temporaire %s toujours présent après Submit", tmpFile.Name())
	}
}

// TestSubmitCleansFileOnQueueFull couvre le cas errQueueFull : le job
// n'entre jamais dans le channel, personne d'autre ne nettoiera.
func TestSubmitCleansFileOnQueueFull(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "test-cleanup-queuefull-*.jpg")
	if err != nil {
		t.Fatalf("création fichier temporaire: %v", err)
	}
	tmpFile.Close()

	// Channel non-bufferisé, 0 worker : q.jobs <- item bloque jusqu'à
	// enqueueTimeout (5s), déclenchant errQueueFull de façon déterministe.
	q := &Queue{jobs: make(chan queueItem)}

	res := q.Submit(context.Background(), Job{FilePath: tmpFile.Name()})
	if !errors.Is(res.Err, errQueueFull) {
		t.Fatalf("attendu errQueueFull, obtenu %v", res.Err)
	}
	if _, err := os.Stat(tmpFile.Name()); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("fichier temporaire %s toujours présent après Submit", tmpFile.Name())
	}
}

// TestWorkerCleansFileOnExpiredQueueItem couvre le cas où le job entre bien
// dans le channel mais expire avant d'être pris par un worker (branche
// `continue` de runWorker).
func TestWorkerCleansFileOnExpiredQueueItem(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "test-cleanup-expired-*.jpg")
	if err != nil {
		t.Fatalf("création fichier temporaire: %v", err)
	}
	tmpFile.Close()

	q := NewQueue(1)
	defer close(q.jobs)

	// Contexte déjà expiré : queueCtx (dérivé) l'est aussi dès sa création,
	// donc le worker qui dépile l'item prend systématiquement la branche
	// `continue` (même technique que TestQueueTimeoutMapping).
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	cancel()

	q.Submit(ctx, Job{FilePath: tmpFile.Name()})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(tmpFile.Name()); errors.Is(err, fs.ErrNotExist) {
			return // nettoyé, test réussi
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("fichier temporaire %s toujours présent après expiration en file", tmpFile.Name())
}

// TestSubmitDeadlineIsTimeoutNotClientGone : un contexte parent dont la
// DEADLINE expire (le budget interne du mode async, par ex.) doit être
// rapporté comme un timeout, pas comme une annulation client.
func TestSubmitDeadlineIsTimeoutNotClientGone(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "test-deadline-*.jpg")
	if err != nil {
		t.Fatalf("création fichier temporaire: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	q := &Queue{jobs: make(chan queueItem)} // 0 worker : q.jobs<-item ne se termine jamais
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	res := q.Submit(ctx, Job{FilePath: tmpFile.Name()})
	if !errors.Is(res.Err, errClientTimeout) {
		t.Fatalf("attendu errClientTimeout (deadline dépassée), obtenu %v", res.Err)
	}
}

// TestSubmitRealCancelIsClientGone : une annulation réelle (pas une
// deadline) doit rester rapportée comme errClientGone.
func TestSubmitRealCancelIsClientGone(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "test-cancel-*.jpg")
	if err != nil {
		t.Fatalf("création fichier temporaire: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	tmpFile.Close()

	q := &Queue{jobs: make(chan queueItem)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res := q.Submit(ctx, Job{FilePath: tmpFile.Name()})
	if !errors.Is(res.Err, errClientGone) {
		t.Fatalf("attendu errClientGone (annulation réelle), obtenu %v", res.Err)
	}
}
