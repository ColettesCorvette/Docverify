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

// Seuils de taille du fichier source pour décider du pipeline de
// prétraitement avant envoi au LLM. Calibré sur les observations cluster
// du 2026-05-27 :
//   - Qwen-7B accepte sans broncher jusqu'à ~3 Mo de base64.
//   - L'upscale 200% sur une image > 1 Mo gonfle au-delà → GPU hang Metal
//     (cas cni8493 sous Qwen-32B).
//   - solange (2.6 Mo, 2480x3508, 8.7 MP) sans préprocess donne un OCR
//     dégradé : Qwen-7B trouve le nom mais rate doc_type et date.
const (
	llmPreprocessLightLimit = 1 << 20       // 1 Mo : bascule en mode léger
	llmPreprocessSkipLimit  = 3 * (1 << 20) // 3 Mo : skip total
)

// preprocessForLLM améliore une image avant l'envoi au LLM vision.
//
// Trois régimes selon la taille du fichier source :
//
//	< 1 Mo  : upscale 200% + unsharp + despeckle. Petite image, on grossit
//	          pour rendre lisible les caractères fins MRZ et dates.
//	1-3 Mo  : downscale à 2000 px max + unsharp + despeckle. Image déjà
//	          grande, on évite d'exploser le base64 envoyé à vllm-mlx tout
//	          en gardant un sharpen utile à l'OCR sur scan bruité.
//	> 3 Mo  : skip total, image originale envoyée telle quelle. Au-delà,
//	          aucune retouche ne sauve l'envoi.
//
// Le suffixe `>` sur `-resize` n'agit que si l'image dépasse la dimension,
// donc une image 1200x800 source en mode léger ne sera pas redimensionnée.
//
// Retourne l'image originale si ImageMagick est absent ou si la conversion
// échoue.
func preprocessForLLM(imagePath string) (string, func(), error) {
	if !isImageMagickAvailable() {
		return imagePath, func() {}, nil
	}

	info, err := os.Stat(imagePath)
	if err != nil {
		return imagePath, func() {}, nil
	}

	if info.Size() > llmPreprocessSkipLimit {
		fmt.Printf("   Prétraitement LLM sauté: image source %.1f Mo > %d Mo\n",
			float64(info.Size())/(1<<20), llmPreprocessSkipLimit>>20)
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

	var resizeArg, mode string
	if info.Size() > llmPreprocessLightLimit {
		resizeArg, mode = "2000x2000>", "léger"
	} else {
		resizeArg, mode = "200%", "upscale"
	}
	fmt.Printf("   Prétraitement LLM %s: resize %s + unsharp + despeckle (source %.1f Mo)\n",
		mode, resizeArg, float64(info.Size())/(1<<20))

	cmd := exec.Command("convert",
		imagePath,
		"-resize", resizeArg,
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
