package docverify

import (
	"context"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// imageMagickLimits borne la mémoire/carte mémoire/disque qu'un process
// ImageMagick peut consommer sur un upload arbitraire (bombe de
// décompression, image pathologique). Appliqué à chaque invocation
// convert/identify du pipeline.
var imageMagickLimits = []string{
	"-limit", "memory", "256MiB",
	"-limit", "map", "512MiB",
	"-limit", "disk", "1GiB",
}

// imageMagickArgs préfixe imageMagickLimits aux arguments d'un appel
// convert/identify.
func imageMagickArgs(args ...string) []string {
	return append(append([]string{}, imageMagickLimits...), args...)
}

// preprocessImage améliore une image avant l'OCR via ImageMagick.
// Pipeline optimisé pour les documents avec fond complexe (passeports, CNI, etc.) :
//   - Niveaux de gris
//   - Normalisation + contrast-stretch : améliore lisibilité sans binariser
//     (la binarisation dégrade les docs avec fond guilloché)
//
// Retourne l'image originale si ImageMagick est absent (dégradé gracieux).
func preprocessImage(ctx context.Context, imagePath string) (string, func()) {
	if _, err := exec.LookPath("convert"); err != nil {
		return imagePath, func() {}
	}

	tmpDir, err := os.MkdirTemp("", "docverify-preprocess-*")
	if err != nil {
		return imagePath, func() {}
	}

	ext := filepath.Ext(imagePath)
	if ext == "" {
		ext = ".png"
	}
	outPath := filepath.Join(tmpDir, "preprocessed"+ext)

	cmd := exec.CommandContext(ctx, "convert", imageMagickArgs(
		imagePath,
		"-colorspace", "Gray", // niveaux de gris
		"-normalize",                 // étire l'histogramme
		"-contrast-stretch", "3%x3%", // coupe les extrêmes pour booster le contraste
		outPath,
	)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("   Prétraitement image échoué: %s", strings.TrimSpace(string(out)))
		os.RemoveAll(tmpDir)
		return imagePath, func() {}
	}

	return outPath, func() { os.RemoveAll(tmpDir) }
}

func isImageMagickAvailable() bool {
	_, err := exec.LookPath("convert")
	return err == nil
}

// llmDownscaleLimit borne la taille du fichier source au-delà de laquelle on
// réduit la définition avant l'envoi au LLM. Un base64 trop lourd fait hang
// le GPU Metal du backend LLM (observé en cluster sur des scans > 2 Mo).
const llmDownscaleLimit = 1 * (1 << 20) // 1 Mo

// preprocessForLLM prépare une image pour l'envoi au LLM vision.
//
// Par défaut l'image est transmise telle quelle : un VLM lit mieux l'original
// qu'une version retouchée. Le sharpen (unsharp/despeckle) est un réflexe
// hérité de Tesseract qui dégrade la transcription LLM — il a causé la
// régression cni8493 du 2026-05-27 et a donc été retiré.
//
// Seule exception : si le fichier source dépasse llmDownscaleLimit, on réduit
// la définition à 2000 px max (sans aucun filtre) pour éviter un base64 trop
// lourd. Le suffixe `>` n'agit que si l'image dépasse la dimension.
//
// Retourne l'image originale si ImageMagick est absent, si l'image est sous le
// seuil, ou si la conversion échoue.
func preprocessForLLM(ctx context.Context, imagePath string) (string, func()) {
	if !isImageMagickAvailable() {
		return imagePath, func() {}
	}

	info, err := os.Stat(imagePath)
	if err != nil || info.Size() <= llmDownscaleLimit {
		return imagePath, func() {}
	}

	tmpDir, err := os.MkdirTemp("", "docverify-llm-preprocess-*")
	if err != nil {
		return imagePath, func() {}
	}

	ext := filepath.Ext(imagePath)
	if ext == "" {
		ext = ".png"
	}
	outPath := filepath.Join(tmpDir, "llm"+ext)

	log.Printf("   Image source %.1f Mo > %d Mo : downscale 1280px avant LLM",
		float64(info.Size())/(1<<20), llmDownscaleLimit>>20)

	cmd := exec.CommandContext(ctx, "convert", imageMagickArgs(imagePath, "-resize", "1280x1280>", outPath)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("   Downscale LLM échoué: %s", strings.TrimSpace(string(out)))
		os.RemoveAll(tmpDir)
		return imagePath, func() {}
	}

	return outPath, func() { os.RemoveAll(tmpDir) }
}
