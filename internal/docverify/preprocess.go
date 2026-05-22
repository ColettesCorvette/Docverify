package docverify

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
		"-colorspace", "Gray", // niveaux de gris
		"-normalize",                 // étire l'histogramme
		"-contrast-stretch", "3%x3%", // coupe les extrêmes pour booster le contraste
		outPath,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Printf("   Prétraitement image échoué: %s\n", strings.TrimSpace(string(out)))
		os.RemoveAll(tmpDir)
		return imagePath, func() {}, nil
	}

	return outPath, func() { os.RemoveAll(tmpDir) }, nil
}

func isImageMagickAvailable() bool {
	_, err := exec.LookPath("convert")
	return err == nil
}

// preprocessForLLM améliore une image avant l'envoi au LLM vision.
// Pipeline orienté lisibilité visuelle, pas binarisation :
//   - resize 200% : upscale pour rendre les petits caractères MRZ/dates lisibles
//   - unsharp 0x1 : sharpening léger
//   - despeckle : retire les artefacts JPEG sans dégrader le texte
//
// Retourne l'image originale si ImageMagick est absent ou si la conversion échoue.
func preprocessForLLM(imagePath string) (string, func(), error) {
	if !isImageMagickAvailable() {
		return imagePath, func() {}, nil
	}

	tmpDir, err := os.MkdirTemp("", "docverify-llm-preprocess-*")
	if err != nil {
		return imagePath, func() {}, nil
	}

	ext := filepath.Ext(imagePath)
	if ext == "" {
		ext = ".png"
	}
	outPath := filepath.Join(tmpDir, "llm"+ext)

	cmd := exec.Command("convert",
		imagePath,
		"-resize", "200%",
		"-unsharp", "0x1",
		"-despeckle",
		outPath,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Printf("   Prétraitement LLM échoué: %s\n", strings.TrimSpace(string(out)))
		os.RemoveAll(tmpDir)
		return imagePath, func() {}, nil
	}

	return outPath, func() { os.RemoveAll(tmpDir) }, nil
}
