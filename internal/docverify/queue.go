package docverify

import (
	"log"
	"os"
	"strconv"
	"time"
)

const (
	defaultWorkers = 5
	jobBufferSize  = 100
	jobTimeout     = 3 * time.Minute
)

// Job décrit un travail soumis à la queue.
// Le handler remplit FilePath, Input, Model, LLMURL et fournit ResultCh ;
// le worker exécute le pipeline et renvoie sur ResultCh.
type Job struct {
	FilePath string
	Input    Input
	Model    string
	LLMURL   string
	ResultCh chan JobResult
}

// JobResult encapsule la sortie du pipeline pour un job.
type JobResult struct {
	Analysis AnalysisResult
	Err      error
}

// Queue est un broker in-process : un channel bufferisé alimenté par le
// handler HTTP et consommé par un pool de workers.
type Queue struct {
	jobs    chan Job
	workers int
}

// NewQueue démarre N workers en arrière-plan et renvoie la queue prête à l'emploi.
func NewQueue(workers int) *Queue {
	q := &Queue{
		jobs:    make(chan Job, jobBufferSize),
		workers: workers,
	}
	for i := 0; i < workers; i++ {
		go q.runWorker(i)
	}
	log.Printf("Queue in-process démarrée avec %d workers", workers)
	return q
}

// Submit pousse un job dans la queue et bloque jusqu'au résultat ou timeout.
func (q *Queue) Submit(job Job) JobResult {
	job.ResultCh = make(chan JobResult, 1)
	q.jobs <- job
	select {
	case res := <-job.ResultCh:
		return res
	case <-time.After(jobTimeout):
		return JobResult{Err: errTimeout}
	}
}

func (q *Queue) runWorker(id int) {
	for job := range q.jobs {
		log.Printf("[worker %d] traitement %s", id, job.FilePath)
		res := runPipeline(job)
		job.ResultCh <- res
	}
}

// runPipeline exécute OCR + analyse pour un job, puis nettoie le fichier temporaire.
//
// Si la première passe est marquée illisible ou si son score est faible, on
// tente une seconde passe par segmentation. La passe segmentée n'est retenue
// que si elle extrait strictement plus d'informations que la première.
func runPipeline(job Job) JobResult {
	defer os.Remove(job.FilePath)

	var rawText string
	var err error
	if IsPDF(job.FilePath) {
		rawText, err = ExtractPDF(job.FilePath, job.Model, job.LLMURL)
	} else {
		rawText, err = ExtractText(job.FilePath, job.Model, job.LLMURL)
	}
	if err != nil {
		return JobResult{Err: err}
	}

	analysis := AnalyzeText(rawText, job.Input)

	if analysis.Unreadable || scoreAnalysis(analysis) < segmentationTriggerScore {
		log.Printf("   Score initial=%d, unreadable=%v — tentative de segmentation",
			scoreAnalysis(analysis), analysis.Unreadable)
		segText, segErr := ExtractWithSegmentation(job.FilePath, job.Model, job.LLMURL)
		if segErr != nil {
			log.Printf("   Segmentation échouée: %v", segErr)
		} else if segText != "" {
			segAnalysis := AnalyzeText(segText, job.Input)
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

const errTimeout queueErr = "timeout: pipeline OCR + analyse n'a pas répondu dans le délai imparti"
