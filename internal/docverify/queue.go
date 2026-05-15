package docverify

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	jobsKey       = "docverify:jobs"
	resultKeyTmpl = "docverify:result:%s"
	resultTTL     = 5 * time.Minute
	submitTimeout = 3 * time.Minute
)

// Job est le payload sérialisé déposé dans la liste Redis docverify:jobs.
// Le worker qui le consomme rejoue le pipeline OCR + analyse.
//
// Filename + Content transitent par Redis pour que n'importe quel worker
// puisse traiter le job, quel que soit le pod qui a reçu la requête HTTP.
// Le /tmp de chaque pod est isolé, donc un simple chemin ne suffirait pas.
type Job struct {
	ID       string `json:"id"`
	Filename string `json:"filename"`
	Content  []byte `json:"content"`
	Input    Input  `json:"input"`
	Model    string `json:"model"`
	LLMURL   string `json:"llm_url"`
}

// jobResult encapsule la réponse renvoyée par le worker au handler via la
// clé docverify:result:{id}.
type jobResult struct {
	Result AnalysisResult `json:"result"`
	Error  string         `json:"error,omitempty"`
}

// Queue distribue les jobs OCR via Redis et fait tourner un pool de workers
// qui consomment la liste docverify:jobs.
type Queue struct {
	rdb     *redis.Client
	workers int
	host    string
}

// NewQueue ouvre une connexion Redis et démarre `workers` goroutines qui
// font BRPOP sur la liste des jobs. Retourne une erreur si la connexion
// initiale échoue.
func NewQueue(redisURL string, workers int) (*Queue, error) {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("parse redis url: %w", err)
	}
	rdb := redis.NewClient(opts)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("ping redis: %w", err)
	}

	host, _ := os.Hostname()
	q := &Queue{rdb: rdb, workers: workers, host: host}
	for i := 0; i < workers; i++ {
		go q.worker(i)
	}
	log.Printf("Queue Redis prête : %d workers sur %s", workers, host)
	return q, nil
}

// Submit dépose un job dans la queue et bloque jusqu'à réception du résultat
// par le worker qui l'a traité, ou jusqu'à expiration de submitTimeout.
func (q *Queue) Submit(ctx context.Context, job Job) (AnalysisResult, error) {
	if job.ID == "" {
		job.ID = newJobID()
	}

	payload, err := json.Marshal(job)
	if err != nil {
		return AnalysisResult{}, fmt.Errorf("marshal job: %w", err)
	}

	if err := q.rdb.LPush(ctx, jobsKey, payload).Err(); err != nil {
		return AnalysisResult{}, fmt.Errorf("LPUSH jobs: %w", err)
	}

	waitCtx, cancel := context.WithTimeout(ctx, submitTimeout)
	defer cancel()

	resultKey := fmt.Sprintf(resultKeyTmpl, job.ID)
	res, err := q.rdb.BRPop(waitCtx, 0, resultKey).Result()
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return AnalysisResult{}, fmt.Errorf("timeout en attente du worker (%s)", submitTimeout)
		}
		return AnalysisResult{}, fmt.Errorf("BRPOP result: %w", err)
	}

	var jr jobResult
	if err := json.Unmarshal([]byte(res[1]), &jr); err != nil {
		return AnalysisResult{}, fmt.Errorf("unmarshal result: %w", err)
	}
	if jr.Error != "" {
		return AnalysisResult{}, errors.New(jr.Error)
	}
	return jr.Result, nil
}

// worker boucle sur BRPOP docverify:jobs et exécute le pipeline pour chaque
// job, puis publie le résultat sur la clé dédiée du handler.
func (q *Queue) worker(id int) {
	ctx := context.Background()
	for {
		res, err := q.rdb.BRPop(ctx, 0, jobsKey).Result()
		if err != nil {
			log.Printf("worker %d : BRPOP jobs : %v — retry dans 1s", id, err)
			time.Sleep(time.Second)
			continue
		}

		var job Job
		if err := json.Unmarshal([]byte(res[1]), &job); err != nil {
			log.Printf("worker %d : payload invalide ignoré : %v", id, err)
			continue
		}

		log.Printf("worker %d@%s : job %s en traitement", id, q.host, job.ID)
		result, runErr := q.runPipeline(job)

		jr := jobResult{Result: result}
		if runErr != nil {
			jr.Error = runErr.Error()
			log.Printf("worker %d : job %s échoué : %v", id, job.ID, runErr)
		}

		payload, _ := json.Marshal(jr)
		resultKey := fmt.Sprintf(resultKeyTmpl, job.ID)

		pipe := q.rdb.TxPipeline()
		pipe.LPush(ctx, resultKey, payload)
		pipe.Expire(ctx, resultKey, resultTTL)
		if _, err := pipe.Exec(ctx); err != nil {
			log.Printf("worker %d : publish result %s : %v", id, job.ID, err)
		}
	}
}

// runPipeline matérialise le contenu du job dans le /tmp local du worker,
// puis exécute la séquence OCR + AnalyzeText. Le fichier est supprimé en
// fin de pipeline pour libérer /tmp.
func (q *Queue) runPipeline(job Job) (AnalysisResult, error) {
	tmpPath, err := writeJobFile(job)
	if err != nil {
		return AnalysisResult{}, fmt.Errorf("écriture /tmp : %w", err)
	}
	defer os.Remove(tmpPath)

	log.Printf("worker@%s : job %s LLM=%s file=%s", q.host, job.ID, job.LLMURL, tmpPath)

	var rawText string
	if IsPDF(tmpPath) {
		rawText, err = ExtractPDF(tmpPath, job.Model, job.LLMURL)
	} else {
		rawText, err = ExtractText(tmpPath, job.Model, job.LLMURL)
	}
	if err != nil {
		return AnalysisResult{}, fmt.Errorf("OCR : %w", err)
	}

	result := AnalyzeText(rawText, job.Input)
	result.RawText = ""
	return result, nil
}

// writeJobFile écrit le contenu du job dans /tmp et retourne le chemin.
// Préfixe le nom par l'ID du job pour éviter toute collision entre workers
// du même pod qui traitent des fichiers homonymes en parallèle.
func writeJobFile(job Job) (string, error) {
	name := fmt.Sprintf("%s-%s", job.ID, sanitizeFilename(job.Filename))
	path := "/tmp/" + name
	if err := os.WriteFile(path, job.Content, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// sanitizeFilename garde seulement la base et substitue les caractères
// problématiques. Le nom n'a pas besoin d'être unique, l'ID le rend unique.
func sanitizeFilename(name string) string {
	base := name
	if i := lastSlash(name); i >= 0 {
		base = name[i+1:]
	}
	if base == "" {
		base = "upload"
	}
	return base
}

func lastSlash(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '/' || s[i] == '\\' {
			return i
		}
	}
	return -1
}

// newJobID génère un identifiant hex aléatoire de 16 octets.
func newJobID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// DefaultRedisURL retourne l'URL Redis utilisée par défaut.
// Variable d'env : REDIS_URL. Fallback : service K8s redis:6379.
func DefaultRedisURL() string {
	if url := os.Getenv("REDIS_URL"); url != "" {
		return url
	}
	return "redis://redis:6379/0"
}

// DefaultWorkers retourne le nombre de workers à démarrer.
// Variable d'env : QUEUE_WORKERS. Fallback : 5, aligné sur --max-num-seqs
// de vllm-mlx.
func DefaultWorkers() int {
	if v := os.Getenv("QUEUE_WORKERS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 5
}
