package docverify

import (
	"context"
	"errors"
	"io/fs"
	"log"
	"os"
	"strconv"
	"sync/atomic"
	"time"
)

const (
	defaultWorkers = 1
	jobBufferSize  = 100
	queueTimeout   = 45 * time.Second
	execTimeout    = 120 * time.Second
	enqueueTimeout = 5 * time.Second
)

// Job décrit un travail soumis à la queue. Les champs sont remplis par le
// handler ; le worker exécute le pipeline et renvoie un JobResult via le
// channel interne à queueItem.
type Job struct {
	FilePath string
	Input    Input
	Model    string
	LLMURL   string
}

// JobResult encapsule la sortie du pipeline pour un job.
type JobResult struct {
	Analysis AnalysisResult
	Err      error
}

// queueItem associe un job à son contexte d'annulation et à son channel de
// retour. Reste interne à la queue.
type queueItem struct {
	ctx      context.Context
	job      Job
	resultCh chan JobResult
	queueCtx context.Context
	startCh  chan struct{}
}

// Queue est un broker in-process : un channel bufferisé alimenté par le
// handler HTTP et consommé par un pool de workers.
type Queue struct {
	jobs      chan queueItem
	workers   int
	inFlight  int64 // jobs en cours de traitement (atomic), pour /health
	processed int64 // jobs terminés depuis le démarrage (atomic), compteur de docs traités
}

// QueueStats est un instantané de l'état de la queue, exposé par /health pour
// le monitoring du cluster.
type QueueStats struct {
	Workers   int   `json:"workers"`   // taille du pool de workers
	Queued    int   `json:"queued"`    // jobs en attente dans le buffer
	Capacity  int   `json:"capacity"`  // capacité du buffer (jobBufferSize)
	InFlight  int   `json:"inflight"`  // jobs actuellement traités par un worker
	Processed int64 `json:"processed"` // total de docs traités depuis le démarrage du nœud
}

// Stats renvoie un instantané non bloquant de l'état de la queue.
func (q *Queue) Stats() QueueStats {
	return QueueStats{
		Workers:   q.workers,
		Queued:    len(q.jobs),
		Capacity:  cap(q.jobs),
		InFlight:  int(atomic.LoadInt64(&q.inFlight)),
		Processed: atomic.LoadInt64(&q.processed),
	}
}

// NewQueue démarre N workers en arrière-plan et renvoie la queue prête à l'emploi.
func NewQueue(workers int) *Queue {
	q := &Queue{
		jobs:    make(chan queueItem, jobBufferSize),
		workers: workers,
	}
	for i := 0; i < workers; i++ {
		go q.runWorker(i)
	}
	log.Printf("Queue in-process démarrée avec %d workers", workers)
	return q
}

// cleanupJobFile supprime le fichier temporaire d'un job qui n'atteindra
// jamais runPipeline (queue saturée, client parti avant prise en charge, job
// expiré en file) — runPipeline ne s'exécute alors jamais et son
// `defer os.Remove` ne se déclenche pas. Double-remove bénin avec ce defer :
// os.Remove sur un fichier déjà supprimé échoue silencieusement (ignoré).
func cleanupJobFile(path string) {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Printf("suppression fichier temporaire %s échouée: %v", path, err)
	}
}

// Submit pousse un job dans la queue et bloque jusqu'au résultat. Le timeout
// est découplé : queueTimeout (45s) max pour attendre dans la file d'attente,
// et execTimeout (120s) max pour le traitement une fois pris par un worker.
func (q *Queue) Submit(ctx context.Context, job Job) JobResult {
	resultCh := make(chan JobResult, 1)
	startCh := make(chan struct{})

	queueCtx, queueCancel := context.WithTimeout(ctx, queueTimeout)
	defer queueCancel()

	item := queueItem{
		ctx:      ctx,
		job:      job,
		resultCh: resultCh,
		queueCtx: queueCtx,
		startCh:  startCh,
	}

	enqueueTimer := time.NewTimer(enqueueTimeout)
	defer enqueueTimer.Stop()
	select {
	case q.jobs <- item:
	case <-enqueueTimer.C:
		cleanupJobFile(job.FilePath)
		return JobResult{Err: errQueueFull}
	case <-ctx.Done():
		cleanupJobFile(job.FilePath)
		return JobResult{Err: ctxDoneErr(ctx)}
	}

	// Attente du démarrage de l'exécution par un worker ou du timeout d'attente
	select {
	case <-startCh:
		// Le job a commencé son exécution !
	case <-queueCtx.Done():
		if ctx.Err() != nil {
			return JobResult{Err: ctxDoneErr(ctx)}
		}
		return JobResult{Err: errQueueTimeout}
	}

	// Attente du résultat final
	select {
	case res := <-resultCh:
		return res
	case <-ctx.Done():
		return JobResult{Err: ctxDoneErr(ctx)}
	}
}

func (q *Queue) runWorker(id int) {
	for item := range q.jobs {
		// Vérification si le job a déjà expiré dans la file d'attente
		if item.queueCtx.Err() != nil {
			log.Printf("[worker %d] job %s ignoré (expiré dans la file d'attente)", id, item.job.FilePath)
			cleanupJobFile(item.job.FilePath)
			continue
		}

		// On signale au Submit que l'on prend le job en charge
		close(item.startCh)

		log.Printf("[worker %d] traitement %s", id, item.job.FilePath)

		// Contexte d'exécution avec un timeout frais de 120 secondes
		atomic.AddInt64(&q.inFlight, 1)
		execCtx, execCancel := context.WithTimeout(item.ctx, execTimeout)
		res := runPipeline(execCtx, item.job)
		execCancel()
		atomic.AddInt64(&q.inFlight, -1)
		atomic.AddInt64(&q.processed, 1)

		if res.Err != nil && errors.Is(res.Err, context.DeadlineExceeded) {
			res.Err = errTimeout
		}

		// resultCh est buffered 1 : non bloquant même si Submit a déjà rendu
		// la main suite à un timeout ou une annulation côté client.
		item.resultCh <- res
	}
}

// runPipeline exécute OCR + analyse pour un job, puis nettoie le fichier temporaire.
//
// Si la première passe est marquée illisible ou si son score est faible, on
// tente une seconde passe par segmentation. La passe segmentée n'est retenue
// que si elle extrait strictement plus d'informations que la première.
func runPipeline(ctx context.Context, job Job) JobResult {
	defer os.Remove(job.FilePath)

	// 2D-DOC : cachet Datamatrix des justificatifs FR. Données signées, en clair
	// → extraction fiable sans OCR ni LLM. Présent, on court-circuite tout le
	// pipeline (la facture n'atteint jamais le LLM, ce qui décharge le backend LLM).
	// Absent ou illisible, on poursuit le pipeline OCR normal.
	if analysis, ok := TryTwoDDOC(ctx, job.FilePath, job.Input); ok {
		log.Printf("   2D-DOC détecté — pipeline court-circuité")
		analysis.RawText = ""
		return JobResult{Analysis: analysis}
	}

	var rawText string
	var usedLLM bool
	var err error
	if IsPDF(job.FilePath) {
		rawText, usedLLM, err = ExtractPDF(ctx, job.FilePath, job.Model, job.LLMURL)
	} else {
		rawText, usedLLM, err = ExtractText(ctx, job.FilePath, job.Model, job.LLMURL)
	}
	if err != nil {
		return JobResult{Err: err}
	}

	analysis := AnalyzeText(rawText, job.Input)
	if usedLLM {
		analysis.source = srcLLM
	} else {
		analysis.source = srcTesseract
	}
	debug := os.Getenv("DOCVERIFY_DEBUG_OCR") == "1"
	if debug {
		debugLogRawText("initial", analysis)
	}

	// Fallback LLM full-page : si la passe initiale est Tesseract (ou
	// pdftotext) et que le score sémantique reste faible, on tente le LLM
	// sur l'image entière avant de segmenter. Sur des scans HD où Tesseract
	// rend un texte bruité (cas solange, CNI N&B 2480x3508), le LLM avec
	// le contexte global peut extraire des champs que Tesseract rate.
	//
	// La pleine page est tentée même sur un document multi-zones (recto+verso
	// de CNI sur une A4). Auparavant on la sautait au profit direct de la
	// segmentation, parce que le LLM full-page partait en boucle sur le
	// remplissage `<` des MRZ et n'atteignait pas le verso. Avec
	// FrequencyPenalty (cf. llm.go) cette boucle est cassée : la pleine page
	// lit recto et verso de bout en bout (vérifié stable sur solange), ce que
	// le skip rendait impossible. Bénéfice principal : un seul appel LLM
	// pleine page au lieu de N appels concurrents sur des crops, donc beaucoup
	// moins de charge backend. La segmentation reste en filet de sécurité
	// juste après si le score est encore insuffisant.
	if !usedLLM && scoreAnalysis(analysis) < segmentationTriggerScore && !IsPDF(job.FilePath) {
		log.Printf("   Score initial=%d (Tesseract) — fallback LLM image entière",
			scoreAnalysis(analysis))
		llmText, llmErr := RunLLM(ctx, job.FilePath, job.Model, job.LLMURL)
		if llmErr != nil {
			log.Printf("   LLM full-page échoué: %v", llmErr)
		} else if llmText != "" {
			llmAnalysis := AnalyzeText(llmText, job.Input)
			if debug {
				debugLogRawText("llm-full", llmAnalysis)
			}
			if scoreAnalysis(llmAnalysis) > scoreAnalysis(analysis) {
				log.Printf("   LLM full-page retenu: score %d -> %d",
					scoreAnalysis(analysis), scoreAnalysis(llmAnalysis))
				analysis = llmAnalysis
				analysis.source = srcLLM
			} else {
				log.Printf("   LLM full-page n'améliore pas, conservation initiale")
			}
		}
	}

	if analysis.Unreadable || scoreAnalysis(analysis) < segmentationTriggerScore {
		log.Printf("   Score=%d, unreadable=%v — tentative de segmentation",
			scoreAnalysis(analysis), analysis.Unreadable)
		segText, segErr := ExtractWithSegmentation(ctx, job.FilePath, job.Model, job.LLMURL)
		if segErr != nil {
			log.Printf("   Segmentation échouée: %v", segErr)
		} else if segText != "" {
			segAnalysis := AnalyzeText(segText, job.Input)
			if debug {
				debugLogRawText("segment", segAnalysis)
			}
			if scoreAnalysis(segAnalysis) > scoreAnalysis(analysis) {
				log.Printf("   Segmentation retenue: score %d -> %d",
					scoreAnalysis(analysis), scoreAnalysis(segAnalysis))
				analysis = segAnalysis
				analysis.source = srcSegment
			} else {
				log.Printf("   Segmentation n'améliore pas, résultat initial conservé")
			}
		}
	}

	analysis.Confidence = computeConfidence(analysis)
	analysis.RawText = ""
	return JobResult{Analysis: analysis}
}

// debugLogRawText émet les 1500 premiers caractères du texte OCR dans les
// logs serveur. Activé seulement quand DOCVERIFY_DEBUG_OCR=1 ; à utiliser
// ponctuellement pour diagnostiquer un sample qui ressort doc_type=inconnu
// alors qu'on attend un verdict positif. Le tag distingue la passe initiale
// de la passe segmentation. Désactivé en production pour ne pas exposer
// de PII dans les logs K8s.
func debugLogRawText(phase string, a AnalysisResult) {
	preview := a.RawText
	const maxLen = 1500
	if len(preview) > maxLen {
		preview = preview[:maxLen] + "...[tronqué]"
	}
	log.Printf("[DEBUG OCR %s] doc_type=%s name_found=%v date_found=%v score=%d raw_text=%q",
		phase, a.DocType, a.NameFound, a.DateFound, scoreAnalysis(a), preview)
}

// DefaultWorkers lit QUEUE_WORKERS ou retombe sur 5.
func DefaultWorkers() int {
	if v := os.Getenv("QUEUE_WORKERS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return defaultWorkers
}

type queueErr string

func (e queueErr) Error() string { return string(e) }

const (
	errQueueTimeout  queueErr = "queue timeout: le job a attendu trop longtemps dans la file sans être pris en charge"
	errTimeout       queueErr = "timeout: pipeline OCR + analyse n'a pas répondu dans le délai d'exécution imparti"
	errQueueFull     queueErr = "queue saturée: réessayez plus tard"
	errClientGone    queueErr = "requête annulée par le client"
	errClientTimeout queueErr = "timeout: le budget alloué au traitement a expiré avant la fin du job"
)

// ctxDoneErr traduit l'annulation d'un contexte parent en erreur exacte :
// deadline dépassée (timeout, y compris le budget interne du mode async) vs
// annulation réelle (client HTTP déconnecté). Go distingue les deux via
// context.DeadlineExceeded / context.Canceled, quel que soit qui a posé le
// timeout — la synchronisation http.Request.Context() n'a pas de deadline
// propre, donc son ctx.Done() ne peut venir que d'une vraie déconnexion.
func ctxDoneErr(ctx context.Context) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return errClientTimeout
	}
	return errClientGone
}
