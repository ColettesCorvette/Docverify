package docverify

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// scoreAnalysis attribue 1 point par information utile extraite. Sert à
// comparer deux passes OCR pour conserver le meilleur résultat.
func scoreAnalysis(r AnalysisResult) int {
	s := 0
	if r.NameFound {
		s++
	}
	if r.SurnameFound {
		s++
	}
	if r.PostalFound && r.PostalMatch != "" {
		s++
	}
	if r.DateFound {
		s++
	}
	if r.DateValid {
		s++
	}
	if r.BirthDate != "" {
		s++
	}
	if r.IssueDate != "" {
		s++
	}
	if r.ExpiryDate != "" {
		s++
	}
	if r.DocType != DocTypeUnknown {
		s++
	}
	return s
}

// segmentationTriggerScore : seuil en dessous duquel on tente le fallback.
// Calé pour ne pas se déclencher sur une CNI normale qui sort facilement
// 4-5 points, mais pour rattraper les cas où l'analyse globale échoue.
const segmentationTriggerScore = 4

// Region décrit une zone rectangulaire de contenu détectée dans une image.
type Region struct {
	X, Y          int
	Width, Height int
	Area          int
}

// detectRegions identifie les zones rectangulaires de contenu sur une image
// majoritairement vide. Cas d'usage typique : une page A4 contenant le recto
// et le verso d'une CNI séparés par un grand espace blanc.
//
// Pipeline ImageMagick :
//  1. niveaux de gris
//  2. flou gaussien pour fusionner texte et motifs en blobs solides
//  3. seuil 80% pour isoler le contenu sombre du fond blanc
//  4. négation pour retourner contenu blanc / fond noir
//  5. connected-components 8-voisinage + filtre de surface
//
// Retourne les régions triées de haut en bas.
func detectRegions(ctx context.Context, imagePath string) ([]Region, error) {
	if _, err := exec.LookPath("convert"); err != nil {
		return nil, fmt.Errorf("ImageMagick non disponible: %w", err)
	}

	out, err := exec.CommandContext(ctx, "identify", imageMagickArgs("-format", "%w %h", imagePath)...).Output()
	if err != nil {
		return nil, fmt.Errorf("identify: %w", err)
	}
	parts := strings.Fields(strings.TrimSpace(string(out)))
	if len(parts) < 2 {
		return nil, fmt.Errorf("dimensions invalides: %q", string(out))
	}
	imgW, _ := strconv.Atoi(parts[0])
	imgH, _ := strconv.Atoi(parts[1])
	imgArea := imgW * imgH
	if imgArea == 0 {
		return nil, fmt.Errorf("dimensions nulles")
	}

	// Rayon de flou ~2% de la plus petite dimension, min 10px.
	// Assez large pour fusionner les sous-éléments d'un même document, assez
	// petit pour ne pas ponter deux documents séparés par l'espace blanc d'une A4.
	blur := imgW
	if imgH < blur {
		blur = imgH
	}
	blur /= 50
	if blur < 10 {
		blur = 10
	}

	cmd := exec.CommandContext(ctx, "convert", imageMagickArgs(
		imagePath,
		"-colorspace", "Gray",
		"-blur", fmt.Sprintf("0x%d", blur),
		"-threshold", "80%",
		"-negate",
		"-define", fmt.Sprintf("connected-components:area-threshold=%d", imgArea/200),
		"-define", "connected-components:verbose=true",
		"-connected-components", "8",
		"null:",
	)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdoutBytes, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("connected-components: %w\n%s", err, stderr.String())
	}
	output := string(stdoutBytes) + stderr.String()

	// Parse les lignes "  N: WxH+X+Y cx.x,cy.y AREA [extra...] gray(VV)".
	// Selon la version d'ImageMagick, des colonnes optionnelles peuvent
	// apparaître entre l'aire et la couleur. On capture la dernière parenthèse.
	re := regexp.MustCompile(
		`(?m)^\s*\d+:\s+(\d+)x(\d+)\+(\d+)\+(\d+)\s+[\d.,]+\s+(\d+).*?\(([^)]+)\)\s*$`,
	)

	var regions []Region
	for _, m := range re.FindAllStringSubmatch(output, -1) {
		w, _ := strconv.Atoi(m[1])
		h, _ := strconv.Atoi(m[2])
		x, _ := strconv.Atoi(m[3])
		y, _ := strconv.Atoi(m[4])
		area, _ := strconv.Atoi(m[5])
		color := m[6]

		// Après négation : contenu = blanc, fond = noir. On ignore le fond.
		if !isLightColor(color) {
			continue
		}
		// Ignorer une bbox qui couvre quasiment toute l'image.
		if w*h > 9*imgArea/10 {
			continue
		}
		// Minimum 1% d'aire image, sécurité après area-threshold.
		if area < imgArea/100 {
			continue
		}
		regions = append(regions, Region{X: x, Y: y, Width: w, Height: h, Area: area})
	}

	sort.Slice(regions, func(i, j int) bool {
		return regions[i].Y < regions[j].Y
	})

	// Fusion verticale : les sous-blobs proches d'un même document ne doivent
	// pas être traités séparément. Photo + zone texte + MRZ d'une CNI = 3 blobs
	// séparés mais un seul document. Le gap intra-doc est ~30-100px, le gap
	// inter-doc sur une A4 est typiquement >200px.
	mergeGap := imgH / 20
	if mergeGap < 80 {
		mergeGap = 80
	}
	regions = mergeRegions(regions, mergeGap)

	return regions, nil
}

func mergeRegions(regions []Region, mergeGap int) []Region {
	if len(regions) <= 1 {
		return regions
	}
	merged := make([]Region, 0, len(regions))
	current := regions[0]
	for i := 1; i < len(regions); i++ {
		r := regions[i]
		currentBottom := current.Y + current.Height
		gap := r.Y - currentBottom
		if gap < mergeGap {
			x1 := min(current.X, r.X)
			y1 := min(current.Y, r.Y)
			x2 := max(current.X+current.Width, r.X+r.Width)
			y2 := max(current.Y+current.Height, r.Y+r.Height)
			current = Region{
				X: x1, Y: y1,
				Width:  x2 - x1,
				Height: y2 - y1,
				Area:   current.Area + r.Area,
			}
		} else {
			merged = append(merged, current)
			current = r
		}
	}
	merged = append(merged, current)
	return merged
}

// isLightColor accepte "v", "r,g,b" ou "r,g,b,a" avec valeurs en entier 0-255,
// en pourcentage ou en float normalisé 0.0-1.0. Retourne true si la luminance
// moyenne dépasse 70%.
func isLightColor(color string) bool {
	parts := strings.Split(color, ",")
	var total float64
	var n int
	for _, p := range parts {
		p = strings.TrimSpace(p)
		isPct := strings.HasSuffix(p, "%")
		p = strings.TrimSuffix(p, "%")

		f, err := strconv.ParseFloat(p, 64)
		if err != nil {
			continue
		}

		var v float64
		switch {
		case isPct:
			v = f / 100.0
		case f <= 1.0:
			v = f
		default:
			v = f / 255.0
		}
		total += v
		n++
	}
	if n == 0 {
		return false
	}
	return total/float64(n) > 0.70
}

func cropRegion(ctx context.Context, imagePath string, r Region, outPath string, padding int) error {
	x := r.X - padding
	y := r.Y - padding
	w := r.Width + 2*padding
	h := r.Height + 2*padding
	if x < 0 {
		w += x
		x = 0
	}
	if y < 0 {
		h += y
		y = 0
	}
	geom := fmt.Sprintf("%dx%d+%d+%d", w, h, x, y)
	cmd := exec.CommandContext(ctx, "convert", imageMagickArgs(imagePath, "-crop", geom, "+repage", outPath)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("crop %s: %w\n%s", geom, err, string(out))
	}
	return nil
}

// ExtractWithSegmentation détecte les zones de contenu d'une image ou d'un PDF
// et applique l'OCR LLM sur chaque zone séparément, puis concatène les résultats.
//
// Retourne ("", nil) si moins de 2 régions sont détectées : la segmentation
// n'apporterait rien et le pipeline standard a déjà été tenté.
//
// Les crops temporaires sont stockés dans un dossier tempdir nettoyé en fin
// d'exécution. Le contexte est propagé à chaque appel RunLLM concurrent.
func ExtractWithSegmentation(ctx context.Context, path, model, llmURL string) (string, error) {
	imagePath := path
	if IsPDF(path) {
		ip, cleanup, err := convertPDFToImage(ctx, path)
		if err != nil {
			return "", err
		}
		defer cleanup()
		imagePath = ip
	}

	regions, err := detectRegions(ctx, imagePath)
	if err != nil {
		return "", err
	}
	if len(regions) < 2 {
		fmt.Printf("   Segmentation : %d zone(s) détectée(s), bascule inutile\n", len(regions))
		return "", nil
	}

	fmt.Printf("   Segmentation : %d zones détectées\n", len(regions))

	zonesDir, err := os.MkdirTemp("", "docverify-zones-*")
	if err != nil {
		return "", fmt.Errorf("dossier zones: %w", err)
	}
	defer os.RemoveAll(zonesDir)

	type zoneResult struct {
		idx  int
		text string
		err  error
	}
	results := make(chan zoneResult, len(regions))

	var wg sync.WaitGroup
	for i, r := range regions {
		cropPath := filepath.Join(zonesDir, fmt.Sprintf("zone-%02d.png", i+1))
		if err := cropRegion(ctx, imagePath, r, cropPath, 20); err != nil {
			fmt.Printf("   Crop zone %d échoué: %v\n", i+1, err)
			continue
		}
		fmt.Printf("   Zone %d/%d %dx%d à (%d,%d)\n",
			i+1, len(regions), r.Width, r.Height, r.X, r.Y)

		wg.Add(1)
		go func(idx int, cropPath string) {
			defer wg.Done()
			text, err := RunLLM(ctx, cropPath, model, llmURL)
			results <- zoneResult{idx: idx, text: text, err: err}
		}(i, cropPath)
	}
	go func() {
		wg.Wait()
		close(results)
	}()

	byIdx := make(map[int]string, len(regions))
	for r := range results {
		if r.err != nil {
			fmt.Printf("   OCR zone %d échoué: %v\n", r.idx+1, r.err)
			continue
		}
		byIdx[r.idx] = r.text
	}

	var combined strings.Builder
	successCount := 0
	for i := range regions {
		text, ok := byIdx[i]
		if !ok {
			continue
		}
		combined.WriteString("=== Zone ")
		combined.WriteString(strconv.Itoa(i + 1))
		combined.WriteString(" ===\n")
		combined.WriteString(text)
		combined.WriteString("\n\n")
		successCount++
	}

	if successCount == 0 {
		return "", fmt.Errorf("aucune zone n'a pu être OCRisée")
	}
	return strings.TrimSpace(combined.String()), nil
}
