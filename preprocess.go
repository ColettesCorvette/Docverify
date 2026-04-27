package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// preprocessImage améliore une image avant l'OCR via ImageMagick.
// Pipeline optimisé pour les documents avec fond complexe (passeports, CNI, etc.) :
//   - Niveaux de gris
//   - Normalisation + contrast-stretch : améliore lisibilité sans binariser
//     (la binarisation dégrade les docs avec fond guilloché)
//
// Retourne l'image originale si ImageMagick est absent (dégradé gracieux).
func preprocessImage(imagePath string) (string, func(), error) {
	if _, err := exec.LookPath("convert"); err != nil {
		return imagePath, func() {}, nil
	}

	tmpDir, err := os.MkdirTemp("", "docverify-preprocess-*")
	if err != nil {
		return imagePath, func() {}, nil
	}

	ext := filepath.Ext(imagePath)
	if ext == "" {
		ext = ".png"
	}
	outPath := filepath.Join(tmpDir, "preprocessed"+ext)

	cmd := exec.Command("convert",
		imagePath,
		"-colorspace", "Gray",       // niveaux de gris
		"-normalize",                 // étire l'histogramme
		"-contrast-stretch", "3%x3%", // coupe les extrêmes pour booster le contraste
		outPath,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Printf("   ⚠️  Prétraitement image échoué: %s\n", strings.TrimSpace(string(out)))
		os.RemoveAll(tmpDir)
		return imagePath, func() {}, nil
	}

	return outPath, func() { os.RemoveAll(tmpDir) }, nil
}

func isImageMagickAvailable() bool {
	_, err := exec.LookPath("convert")
	return err == nil
}
