package docverify

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Le 2D-DOC est le cachet électronique visible (Datamatrix) apposé sur les
// justificatifs français : factures d'énergie, de téléphonie, attestations.
// Il encode en clair l'identité et l'adresse du titulaire et porte une
// signature ECDSA. Le décoder donne une extraction fiable sans OCR ni LLM ;
// pour une facture qui en porte un, tout le pipeline lourd est court-circuité,
// ce qui allège d'autant la charge du backend LLM.
//
// Périmètre : extraction des données — identité, adresse, date d'émission. La
// signature ECDSA n'est pas vérifiée et ne le sera pas : on fait confiance au
// contenu du cachet comme on fait confiance à l'OCR, et la vérification
// anti-fraude via l'annuaire des certificats ANTS sort du périmètre de docverify.

const (
	ddocFieldSep = "\x1d" // GS : séparateur entre champs de données
	ddocSigSep   = "\x1f" // US : séparateur entre zone de données et signature
	// ddocDecodeMs : timeout dmtxread. Un cachet présent décode en ~150 ms ;
	// un document sans cachet scanne l'image jusqu'à ce délai (= la pénalité).
	ddocDecodeMs = "2000"
)

// ddocHeaderLen donne la longueur de l'entête à longueur fixe par version de
// 2D-DOC. Seule la v02 est gérée en V1 (couvre nos justificatifs) ; les v03/v04
// insèrent un pays émetteur et décalent les offsets — à ajouter si besoin.
var ddocHeaderLen = map[string]int{"02": 22}

// ddocEmitDateOffset : position de la date d'émission dans l'entête, par version.
var ddocEmitDateOffset = map[string]int{"02": 12}

// ddocEpoch : les dates 2D-DOC comptent les jours depuis le 1er janvier 2000.
var ddocEpoch = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

// TwoDDoc contient les éléments extraits d'un 2D-DOC.
type TwoDDoc struct {
	Version      string
	AC           string    // autorité de certification émettrice
	CertID       string    // identifiant du certificat
	TypeID       string    // type de document (code ANTS)
	EmitDate     time.Time // date d'émission (entête)
	DataZone     string    // données brutes hors signature (champs séparés par GS)
	HasSignature bool
}

// Decode2DDOC tente de lire un Datamatrix 2D-DOC dans le fichier via dmtxread
// (libdmtx). Pour un PDF, la première page est convertie en image. Retourne
// (chaîne brute, true) si un cachet est trouvé (préfixe "DC"), sinon ("", false).
// L'absence de dmtxread n'est pas une erreur : on dégrade vers le pipeline OCR
// normal, comme pour les autres outils externes (Tesseract, poppler).
func Decode2DDOC(ctx context.Context, path string) (string, bool) {
	if _, err := exec.LookPath("dmtxread"); err != nil {
		return "", false
	}

	imagePath := path
	if IsPDF(path) {
		ip, cleanup, err := convertPDFToImage(path)
		if err != nil {
			return "", false
		}
		defer cleanup()
		imagePath = ip
	}

	cmd := exec.CommandContext(ctx, "dmtxread", "-m", ddocDecodeMs, "-N1", imagePath)
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	raw := strings.TrimSpace(string(out))
	if len(raw) < 4 || !strings.HasPrefix(raw, "DC") {
		return "", false
	}
	return raw, true
}

// Parse2DDOC découpe la chaîne brute d'un 2D-DOC : entête à longueur fixe
// (marqueur, version, AC, certificat, dates, type), zone de données (champs
// séparés par GS) et signature (après US). Seule la date d'émission de l'entête
// est décodée ; les champs de données restent bruts et sont exploités comme du
// texte par AnalyzeFrom2DDOC.
func Parse2DDOC(raw string) (*TwoDDoc, error) {
	if len(raw) < 4 || !strings.HasPrefix(raw, "DC") {
		return nil, fmt.Errorf("marqueur 2D-DOC absent")
	}
	version := raw[2:4]
	hlen, ok := ddocHeaderLen[version]
	if !ok {
		return nil, fmt.Errorf("version 2D-DOC %q non gérée", version)
	}
	if len(raw) < hlen {
		return nil, fmt.Errorf("entête tronquée (%d < %d)", len(raw), hlen)
	}

	header := raw[:hlen]
	body := raw[hlen:]

	doc := &TwoDDoc{
		Version: version,
		AC:      header[4:8],
		CertID:  header[8:12],
		TypeID:  header[hlen-2 : hlen],
	}

	off := ddocEmitDateOffset[version]
	if t, ok := decodeDdocDate(header[off : off+4]); ok {
		doc.EmitDate = t
	}

	if i := strings.Index(body, ddocSigSep); i >= 0 {
		doc.DataZone = body[:i]
		doc.HasSignature = true
	} else {
		doc.DataZone = body
	}

	return doc, nil
}

// decodeDdocDate convertit une date 2D-DOC (nombre de jours depuis le
// 1er janvier 2000, codé en hexadécimal sur 4 caractères) en time.Time.
func decodeDdocDate(h string) (time.Time, bool) {
	days, err := strconv.ParseInt(strings.TrimSpace(h), 16, 64)
	if err != nil || days <= 0 {
		return time.Time{}, false
	}
	return ddocEpoch.AddDate(0, 0, int(days)), true
}

// syntheticText reconstruit un texte exploitable par AnalyzeText à partir des
// champs de données, un champ par ligne. Les codes de champ et séparateurs ne
// gênent pas : la recherche de nom et de code postal travaille par inclusion.
func (d *TwoDDoc) syntheticText() string {
	var b strings.Builder
	for _, f := range strings.Split(d.DataZone, ddocFieldSep) {
		if f = strings.TrimSpace(f); f != "" {
			b.WriteString(f)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// AnalyzeFrom2DDOC produit un AnalysisResult à partir d'un 2D-DOC. La recherche
// nom/code postal réutilise AnalyzeText sur les données en clair ; la date
// provient de l'entête signée (et non de l'OCR) ; le type est forcé à facture
// (un 2D-DOC est un justificatif de domicile). Le verdict est ensuite ré-évalué
// avec les contrôles d'un justificatif via l'unique fonction de décision.
func AnalyzeFrom2DDOC(d *TwoDDoc, input Input) AnalysisResult {
	a := AnalyzeText(d.syntheticText(), input)
	a.DocType = DocTypeInvoice
	a.Unreadable = false

	if !d.EmitDate.IsZero() {
		validMonths := input.ValidMonths
		if validMonths <= 0 {
			validMonths = 3
		}
		a.DateFound = true
		a.DocDate = d.EmitDate.Format("02/01/2006")
		a.DateValid = d.EmitDate.After(time.Now().AddDate(0, -validMonths, 0))
	}

	a.OK, a.Errors = evaluateVerdict(a, verdictSpec{
		checks:      defaultChecks(DocTypeInvoice, input.Surname != ""),
		name:        input.Name,
		surname:     input.Surname,
		postalCodes: input.PostalCodes,
	})
	a.source = srcTwoDDOC
	a.Confidence = computeConfidence(a)
	return a
}

// TryTwoDDOC tente l'extraction par cachet 2D-DOC sur le fichier fourni.
// Retourne (résultat, true) si un cachet exploitable est présent ; sinon
// (zéro, false), auquel cas l'appelant poursuit son pipeline OCR normal.
// Point d'entrée partagé par le pipeline HTTP (runPipeline) et le CLI.
func TryTwoDDOC(ctx context.Context, path string, input Input) (AnalysisResult, bool) {
	raw, ok := Decode2DDOC(ctx, path)
	if !ok {
		return AnalysisResult{}, false
	}
	tdoc, err := Parse2DDOC(raw)
	if err != nil {
		return AnalysisResult{}, false
	}
	return AnalyzeFrom2DDOC(tdoc, input), true
}
