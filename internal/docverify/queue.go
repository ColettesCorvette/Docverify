package docverify

import (
	"context"
	"log"
	"os"
	"strconv"
	"time"
)

const (
	defaultWorkers = 5
	jobBufferSize  = 100
	jobTimeout     = 90 * time.Second
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
}

// Queue est un broker in-process : un channel bufferisé alimenté par le
// handler HTTP et consommé par un pool de workers.
type Queue struct {
	jobs    chan queueItem
	workers int
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

// Submit pousse un job dans la queue et bloque jusqu'au résultat. Le contexte
// permet trois sorties anticipées : queue saturée (enqueueTimeout), pipeline
// dépassant jobTimeout, ou annulation côté client (ctx.Done).
func (q *Queue) Submit(ctx context.Context, job Job) JobResult {
	resultCh := make(chan JobResult, 1)
	item := queueItem{ctx: ctx, job: job, resultCh: resultCh}

	enqueueTimer := time.NewTimer(enqueueTimeout)
	defer enqueueTimer.Stop()
	select {
	case q.jobs <- item:
	case <-enqueueTimer.C:
		return JobResult{Err: errQueueFull}
	case <-ctx.Done():
		return JobResult{Err: errClientGone}
	}

	jobTimer := time.NewTimer(jobTimeout)
	defer jobTimer.Stop()
	select {
	case res := <-resultCh:
		return res
	case <-jobTimer.C:
		return JobResult{Err: errTimeout}
	case <-ctx.Done():
		return JobResult{Err: errClientGone}
	}
}

func (q *Queue) runWorker(id int) {
	for item := range q.jobs {
		log.Printf("[worker %d] traitement %s", id, item.job.FilePath)
		res := runPipeline(item.ctx, item.job)
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
	debug := os.Getenv("DOCVERIFY_DEBUG_OCR") == "1"
	if debug {
		debugLogRawText("initial", analysis)
	}

	// Fallback LLM full-page : si la passe initiale est Tesseract (ou
	// pdftotext) et que le score sémantique reste faible, on tente le LLM
	// sur l'image entière avant de segmenter. Sur des scans HD où Tesseract
	// rend un texte bruité (cas solange, CNI N&B 2480x3508), le LLM avec
	// le contexte global peut extraire des champs que Tesseract rate.
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
			} else {
				log.Printf("   Segmentation n'améliore pas, résultat initial conservé")
			}
		}
	}

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
	errTimeout    queueErr = "timeout: pipeline OCR + analyse n'a pas répondu dans le délai imparti"
	errQueueFull  queueErr = "queue saturée: réessayez plus tard"
	errClientGone queueErr = "requête annulée par le client"
)
