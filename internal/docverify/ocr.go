package docverify

import (
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
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
//	Documents à fond guilloché (passeports) → ratio faible → Ollama direct
//
// Seuils calibrés sur exemples réels :
//
//	CNI française : 83% de pixels blancs  → Tesseract
//	Passeport MAR : 3.5% de pixels blancs → Ollama
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
		map[bool]string{true: "complexe → Ollama", false: "simple → Tesseract"}[complex])
	return complex
}

// ExtractText est le point d'entrée pour l'OCR d'une image.
// Pipeline adaptatif :
//  0. Détection fond complexe → Ollama direct (passeports, CNI)
//  1. Tesseract sur image brute (meilleur PSM parmi 3,4,6,11)
//  2. Tesseract sur image prétraitée ImageMagick
//  3. Ollama en fallback final
func ExtractText(imagePath, model, ollamaURL string) (string, error) {

	// Étape 0 : détecter si le fond est complexe (guilloché, texturé)
	if hasComplexBackground(imagePath) {
		fmt.Println("   Fond complexe détecté (guilloché/sécurité) → Ollama direct")
		return RunOllama(imagePath, model, ollamaURL)
	}

	if !tesseractAvailable() {
		fmt.Println("   Moteur OCR    : Ollama (Tesseract absent — brew install tesseract tesseract-lang)")
		return RunOllama(imagePath, model, ollamaURL)
	}

	// Étape 1 : Tesseract brut, meilleur PSM
	fmt.Println("   Moteur OCR    : Tesseract (image brute)")
	text, q := bestTesseractResult(imagePath)
	fmt.Printf("   Qualité OCR   : %.0f%%\n", q*100)
	if q >= ocrQualityThreshold {
		return text, nil
	}
	fmt.Printf("   ⚠️  Qualité insuffisante (%.0f%% < %.0f%%)\n", q*100, ocrQualityThreshold*100)

	// Étape 2 : prétraitement + retry
	if isImageMagickAvailable() {
		fmt.Println("   Prétraitement : ImageMagick → retry Tesseract")
		pp, cleanup, _ := preprocessImage(imagePath)
		defer cleanup()
		text2, q2 := bestTesseractResult(pp)
		fmt.Printf("   Qualité OCR   : %.0f%%\n", q2*100)
		if q2 >= ocrQualityThreshold {
			return text2, nil
		}
		fmt.Printf("   ⚠️  Qualité insuffisante après prétraitement (%.0f%%)\n", q2*100)
	}

	// Étape 3 : fallback Ollama
	fmt.Println("   Bascule sur Ollama (document complexe)")
	return RunOllama(imagePath, model, ollamaURL)
}

// bestTesseractResult essaie plusieurs modes PSM et retourne le texte
// ayant le meilleur score de qualité OCR.
func bestTesseractResult(imagePath string) (string, float64) {
	modes := []string{"6", "3", "4", "11"}
	bestText := ""
	bestQ := 0.0

	for _, psm := range modes {
		out, err := exec.Command("tesseract", imagePath, "stdout",
			"-l", "fra+eng", "--psm", psm).Output()
		if err != nil {
			out, err = exec.Command("tesseract", imagePath, "stdout",
				"--psm", psm).Output()
			if err != nil {
				continue
			}
		}
		text := strings.TrimSpace(string(out))
		q := ocrQuality(text)
		if q > bestQ {
			bestQ = q
			bestText = text
		}
	}
	return bestText, bestQ
}

func tesseractAvailable() bool {
	_, err := exec.LookPath("tesseract")
	return err == nil
}
