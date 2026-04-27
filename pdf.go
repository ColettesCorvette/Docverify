package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// minTextChars : seuil minimum de caractères pour considérer un PDF comme "texte"
// En dessous = PDF scanné, on bascule sur OCR image
const minTextChars = 50

// ExtractPDF est le point d'entrée pour les PDF.
// Détecte automatiquement si le PDF est texte ou scanné :
//   - PDF texte  → pdftotext (rapide, parfait)
//   - PDF scanné → pdftoppm + Tesseract/Ollama
func ExtractPDF(pdfPath, model, ollamaURL string) (string, error) {
	// 1. Tenter pdftotext en premier
	if text, ok := tryPDFToText(pdfPath); ok {
		fmt.Println("   Type PDF      : texte natif (pdftotext)")
		return text, nil
	}

	// 2. PDF scanné : convertir en image puis OCR
	fmt.Println("   Type PDF      : scanné → conversion image + OCR")
	imgPath, cleanup, err := convertPDFToImage(pdfPath)
	if err != nil {
		return "", err
	}
	defer cleanup()
	fmt.Printf("   Image générée : %s\n", imgPath)

	return ExtractText(imgPath, model, ollamaURL)
}

// tryPDFToText tente d'extraire le texte via pdftotext.
// Retourne (texte, true) si le PDF contient suffisamment de texte natif,
// (vide, false) si pdftotext est absent ou si le PDF est scanné.
func tryPDFToText(pdfPath string) (string, bool) {
	if _, err := exec.LookPath("pdftotext"); err != nil {
		fmt.Println("   ⚠️  pdftotext absent (brew install poppler) — bascule OCR image")
		return "", false
	}

	// pdftotext -l 1 : première page seulement, "-" = sortie stdout
	out, err := exec.Command("pdftotext", "-l", "1", pdfPath, "-").Output()
	if err != nil {
		return "", false
	}

	text := strings.TrimSpace(string(out))

	// Si le texte extrait est trop court, le PDF est probablement scanné
	if len([]rune(text)) < minTextChars {
		fmt.Printf("   ⚠️  pdftotext : seulement %d caractères extraits → PDF scanné\n", len([]rune(text)))
		return "", false
	}

	return text, true
}

// convertPDFToImage convertit la première page d'un PDF en PNG via pdftoppm.
func convertPDFToImage(pdfPath string) (imgPath string, cleanup func(), err error) {
	if _, lookErr := exec.LookPath("pdftoppm"); lookErr != nil {
		return "", nil, fmt.Errorf(
			"pdftoppm introuvable — installez poppler-utils :\n" +
				"  macOS : brew install poppler\n" +
				"  Linux : apt install poppler-utils",
		)
	}

	tmpDir, err := os.MkdirTemp("", "docverify-pdf-*")
	if err != nil {
		return "", nil, fmt.Errorf("creation dossier temp: %w", err)
	}

	outPrefix := filepath.Join(tmpDir, "page")

	cmd := exec.Command("pdftoppm",
		"-r", "200",
		"-png",
		"-l", "1",
		pdfPath,
		outPrefix,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		os.RemoveAll(tmpDir)
		return "", nil, fmt.Errorf("pdftoppm: %w\n%s", err, string(out))
	}

	matches, err := filepath.Glob(outPrefix + "*.png")
	if err != nil || len(matches) == 0 {
		os.RemoveAll(tmpDir)
		return "", nil, fmt.Errorf("aucune image PNG generee par pdftoppm")
	}

	cleanup = func() { os.RemoveAll(tmpDir) }
	return matches[0], cleanup, nil
}

// IsPDF retourne true si le fichier a une extension PDF (insensible a la casse)
func IsPDF(path string) bool {
	return strings.EqualFold(filepath.Ext(path), ".pdf")
}
