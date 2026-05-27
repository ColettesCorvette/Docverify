package docverify

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

const (
	ocrQualityThreshold = 0.50
)

var nonAlphaRe = regexp.MustCompile(`[^a-zA-ZÀ-ÿ\d]`)

// ocrQuality évalue la lisibilité d'un texte OCR (ratio de lignes propres).
func ocrQuality(text string) float64 {
	lines := strings.Split(text, "\n")
	var meaningful []string
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			meaningful = append(meaningful, l)
		}
	}
	if len(meaningful) == 0 {
		return 0
	}
	clean := 0
	for _, line := range meaningful {
		tokens := strings.Fields(line)
		if len(tokens) == 0 {
			continue
		}
		garbage := 0
		for _, t := range tokens {
			net := nonAlphaRe.ReplaceAllString(t, "")
			if len([]rune(net)) <= 2 {
				garbage++
			}
		}
		if float64(garbage)/float64(len(tokens)) <= 0.5 {
			clean++
		}
	}
	return float64(clean) / float64(len(meaningful))
}

// hasComplexBackground détecte si le fond de l'image est trop complexe pour Tesseract.
// Méthode : ratio de pixels blancs (>78% de luminosité).
//
//	Documents à fond blanc (factures, CNI) → ratio élevé  → Tesseract OK
//	Documents à fond guilloché (passeports) → ratio faible → LLM direct
//
// Seuils calibrés sur exemples réels :
//
//	CNI française : 83% de pixels blancs  → Tesseract
//	Passeport MAR : 3.5% de pixels blancs → LLM
const whitePixelThreshold = 0.25 // en dessous → fond trop chargé pour Tesseract

func hasComplexBackground(imagePath string) bool {
	if !isImageMagickAvailable() {
		return false
	}
	// Ratio de pixels blancs (luminosité > 78%)
	out, err := exec.Command("convert", imagePath,
		"-colorspace", "Gray",
		"-threshold", "78%",
		"-format", "%[fx:mean]",
		"info:").Output()
	if err != nil {
		return false
	}
	firstLine := strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0]
	ratio, err := strconv.ParseFloat(strings.TrimSpace(firstLine), 64)
	if err != nil {
		return false
	}
	complex := ratio < whitePixelThreshold
	fmt.Printf("   Fond image     : %.0f%% pixels blancs → %s\n",
		ratio*100,
		map[bool]string{true: "complexe → LLM", false: "simple → Tesseract"}[complex])
	return complex
}

// ExtractText est le point d'entrée pour l'OCR d'une image.
// Pipeline adaptatif :
//  0. Détection fond complexe → LLM direct (passeports, CNI)
//  1. Tesseract sur image brute (meilleur PSM parmi 3,4,6,11)
//  2. Tesseract sur image prétraitée ImageMagick
//  3. LLM en fallback final
//
// Retourne le texte OCR, un booléen usedLLM indiquant si le LLM a été
// utilisé (utile pour décider d'un fallback en aval si le score reste
// faible), et une erreur.
//
// Le contexte est propagé aux appels LLM pour permettre l'annulation côté
// client (Regia) ou le timeout global du job.
func ExtractText(ctx context.Context, imagePath, model, llmURL string) (string, bool, error) {

	// Étape 0 : détecter si le fond est complexe (guilloché, texturé)
	if hasComplexBackground(imagePath) {
		fmt.Println("   Fond complexe détecté (guilloché/sécurité) → LLM direct")
		text, err := RunLLM(ctx, imagePath, model, llmURL)
		return text, true, err
	}

	if !tesseractAvailable() {
		fmt.Println("   Moteur OCR    : LLM (Tesseract absent — brew install tesseract tesseract-lang)")
		text, err := RunLLM(ctx, imagePath, model, llmURL)
		return text, true, err
	}

	// Étape 1 : Tesseract brut, meilleur PSM
	fmt.Println("   Moteur OCR    : Tesseract (image brute)")
	text, q := bestTesseractResult(imagePath)
	fmt.Printf("   Qualité OCR   : %.0f%%\n", q*100)
	if q >= ocrQualityThreshold {
		return text, false, nil
	}
	fmt.Printf("   Qualité insuffisante (%.0f%% < %.0f%%)\n", q*100, ocrQualityThreshold*100)

	// Étape 2 : prétraitement + retry
	if isImageMagickAvailable() {
		fmt.Println("   Prétraitement : ImageMagick → retry Tesseract")
		pp, cleanup, _ := preprocessImage(imagePath)
		defer cleanup()
		text2, q2 := bestTesseractResult(pp)
		fmt.Printf("   Qualité OCR   : %.0f%%\n", q2*100)
		if q2 >= ocrQualityThreshold {
			return text2, false, nil
		}
		fmt.Printf("   Qualité insuffisante après prétraitement (%.0f%%)\n", q2*100)
	}

	// Étape 3 : fallback LLM
	fmt.Println("   Bascule sur LLM (document complexe)")
	text, err := RunLLM(ctx, imagePath, model, llmURL)
	return text, true, err
}

// bestTesseractResult essaie plusieurs modes PSM en parallèle et retourne le
// texte ayant le meilleur score de qualité OCR. Tesseract est un process
// externe par PSM ; les quatre invocations indépendantes tournent en
// concurrence pour diviser la latence par ~4 quand toutes aboutissent.
func bestTesseractResult(imagePath string) (string, float64) {
	modes := []string{"6", "3", "4", "11"}

	type psmResult struct {
		text string
		q    float64
	}
	results := make(chan psmResult, len(modes))

	var wg sync.WaitGroup
	for _, psm := range modes {
		wg.Add(1)
		go func(psm string) {
			defer wg.Done()
			out, err := exec.Command("tesseract", imagePath, "stdout",
				"-l", "fra+eng", "--psm", psm).Output()
			if err != nil {
				out, err = exec.Command("tesseract", imagePath, "stdout",
					"--psm", psm).Output()
				if err != nil {
					return
				}
			}
			text := strings.TrimSpace(string(out))
			results <- psmResult{text: text, q: ocrQuality(text)}
		}(psm)
	}
	go func() {
		wg.Wait()
		close(results)
	}()

	bestText := ""
	bestQ := 0.0
	for r := range results {
		if r.q > bestQ {
			bestQ = r.q
			bestText = r.text
		}
	}
	return bestText, bestQ
}

func tesseractAvailable() bool {
	_, err := exec.LookPath("tesseract")
	return err == nil
}
