package docverify

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DocType représente le type de document détecté
type DocType string

const (
	DocTypeUnknown  DocType = "inconnu"
	DocTypePassport DocType = "passeport"
	DocTypeID       DocType = "carte_identite"
	DocTypeInvoice  DocType = "facture"
	DocTypeLetter   DocType = "courrier"
)

// AnalysisResult contient le résultat de l'analyse du texte OCR
type AnalysisResult struct {
	OK           bool    `json:"ok"`
	DocType      DocType `json:"doc_type"` // type de document détecté
	NameFound    bool    `json:"name_found"`
	SurnameFound bool    `json:"surname_found,omitempty"` // présent seulement si surname fourni
	HasSurname   bool    `json:"has_surname,omitempty"`   // true si surname était dans l'input
	PostalFound  bool    `json:"postal_found"`
	PostalMatch  string  `json:"postal_match,omitempty"`
	// Pour les passeports/CNI : dates spécifiques
	DateFound  bool   `json:"date_found"`
	DateValid  bool   `json:"date_valid"`
	DocDate    string `json:"doc_date,omitempty"`
	ExpiryDate string `json:"expiry_date,omitempty"` // date d'expiration (passeport/CNI)
	IssueDate  string `json:"issue_date,omitempty"`  // date d'émission (passeport/CNI)
	BirthDate  string `json:"birth_date,omitempty"`  // date de naissance (passeport/CNI)
	Unreadable bool   `json:"unreadable,omitempty"`  // document illisible, review humaine requise
	// Confidence : niveau de confiance dans l'extraction (pas la validité).
	// nul | bas | moyen | haut. Sert à Regia pour décider de l'automatisation :
	// haut → auto, moyen/bas → confirmation manuelle, nul → rejet/review.
	Confidence string `json:"confidence,omitempty"`
	// SignatureVerified : true si la signature 2D-DOC a été validée contre la
	// TSL ANTS (anti-fraude). Renseigné uniquement sur le chemin 2D-DOC.
	SignatureVerified bool     `json:"signature_verified,omitempty"`
	Errors            []string `json:"errors,omitempty"`
	RawText           string   `json:"raw_text,omitempty"`
	// source trace le chemin qui a produit le résultat ; non sérialisé,
	// renseigné par le pipeline pour calculer Confidence.
	source confidenceSource
}

// confidenceSource identifie le chemin d'extraction, base du niveau de confiance.
type confidenceSource int

const (
	srcUnknown   confidenceSource = iota // chemin non renseigné (CLI direct)
	srcTwoDDOC                           // cachet 2D-DOC signé, données en clair
	srcTesseract                         // OCR Tesseract direct
	srcLLM                               // fallback LLM vision (pleine page ou extraction directe)
	srcSegment                           // rescousse par segmentation
)

const (
	confNul   = "nul"
	confBas   = "bas"
	confMoyen = "moyen"
	confHaut  = "haut"
)

// computeConfidence mappe (chemin, richesse d'extraction) vers un palier.
// Conservateur : ne promet "haut" que sur données signées ou OCR net, le LLM
// plafonne à "moyen", la segmentation de rescousse reste "bas". scoreAnalysis
// (0-9) sert de proxy de richesse des champs extraits.
func computeConfidence(a AnalysisResult) string {
	if a.Unreadable {
		return confNul
	}
	score := scoreAnalysis(a)
	switch a.source {
	case srcTwoDDOC:
		// Données signées : « haut » seulement si la signature est vérifiée,
		// sinon « moyen » (lisible mais non authentifié).
		if a.SignatureVerified {
			return confHaut
		}
		return confMoyen
	case srcSegment:
		return confBas
	case srcLLM:
		if score >= 4 {
			return confMoyen
		}
		return confBas
	default: // srcTesseract et srcUnknown : OCR net = haut
		switch {
		case score >= 5:
			return confHaut
		case score >= 4:
			return confMoyen
		default:
			return confBas
		}
	}
}

// minOCRChars : seuil sous lequel on considère que l'OCR a échoué.
// En dessous, le LLM a probablement renvoyé un message d'erreur ou rien.
const minOCRChars = 30

// --- Détection du type de document ---

// Mots-clés par type de document. Complété par la détection MRZ
// (mrzIDRe, mrzPassportRe) qui prime quand les libellés sont illisibles.
//
// La validation des chiffres de contrôle ICAO a été retirée le 2026-05-28
// car l'OCR n'est jamais parfait au caractère près, donc elle ne passait
// quasi jamais.
var docTypeKeywords = map[DocType][]string{
	DocTypePassport: {"passport", "passeport", "travel document", "mrz", "p<mar", "p<fra", "p<bel", "nationality", "nationalité", "date of expiry", "date d'expiration"},
	DocTypeID: {
		"carte nationale d'identité", "carte nationale", "carte d'identité",
		"national identity card", "identity card", "république française",
		"cni", "idcard",
	},
	DocTypeInvoice: {"facture", "invoice", "total ttc", "total ht", "tva", "échéance", "montant dû", "n° de facture"},
	DocTypeLetter:  {"madame", "monsieur", "veuillez", "cordialement", "objet :", "courrier"},
}

// mrzIDRe matche le préfixe de la première ligne d'une MRZ TD1 de pièce
// d'identité française (IDFRA...). Sur une CNI scannée, les libellés sont
// souvent trop bruités pour le scoring par mots-clés alors que la MRZ reste
// lisible. Tolère un caractère parasite entre ID et FRA inséré par l'OCR.
var mrzIDRe = regexp.MustCompile(`ID.?FRA`)

// mrzNameRes capture le nom porté par la première ligne d'une MRZ. Le nom y
// est collé au préfixe (IDFRADURAND<<< sur une CNI, P<MARBEN<AMAR<< sur un
// passeport) et ses mots sont séparés par des '<', donc invisible pour le
// matching sur frontières de mot sans expansion préalable (cas solange :
// champ Nom mal OCRisé, seule la MRZ porte le nom exact).
var mrzNameRes = []*regexp.Regexp{
	regexp.MustCompile(`ID.?FRA([A-Z<]+)`),          // CNI française
	regexp.MustCompile(`P[A-Z]?<[A-Z]{3}([A-Z<]+)`), // passeport, nom après le code pays
}

// mrzExpandName retourne les noms extraits des lignes MRZ sous forme de mots
// séparés par des espaces ("" si pas de MRZ). Attend un texte déjà en
// majuscules.
func mrzExpandName(upper string) string {
	var words []string
	for _, re := range mrzNameRes {
		if m := re.FindStringSubmatch(upper); m != nil {
			words = append(words, strings.FieldsFunc(m[1], func(r rune) bool { return r == '<' })...)
		}
	}
	return strings.Join(words, " ")
}

// mrzPassportRe matche la première ligne d'une MRZ de passeport (P<FRA,
// P<MAR...). Le filler < est exigé pour ne pas matcher un mot courant tel
// que PARIS. Même logique que la MRZ CNI : rattrape un libellé illisible.
var mrzPassportRe = regexp.MustCompile(`P[A-Z]?<[A-Z]{3}`)

// DetectDocType analyse le texte OCR et retourne le type de document le plus probable.
//
// La MRZ prime sur le scoring par mots-clés : un IDFRA ou un P<XXX est un
// signal bien plus fiable que des libellés OCR bruités.
//
// L'itération sur scores se fait dans un ordre stable pour rendre le verdict
// déterministe en cas d'égalité de scores entre deux types.
func DetectDocType(text string) DocType {
	upper := strings.ToUpper(text)
	if mrzIDRe.MatchString(upper) {
		return DocTypeID
	}
	if mrzPassportRe.MatchString(upper) {
		return DocTypePassport
	}

	lower := strings.ToLower(text)
	scores := map[DocType]int{}
	for docType, keywords := range docTypeKeywords {
		for _, kw := range keywords {
			if strings.Contains(lower, strings.ToLower(kw)) {
				scores[docType]++
			}
		}
	}
	bestType := DocTypeUnknown
	bestScore := 0
	for _, dt := range docTypeOrder {
		if scores[dt] > bestScore {
			bestScore = scores[dt]
			bestType = dt
		}
	}
	return bestType
}

// docTypeOrder fige un ordre stable pour itérer sur les types de documents
// quand plusieurs ont le même score. L'ordre traduit une priorité métier :
// passeport et CNI avant facture et courrier.
var docTypeOrder = []DocType{
	DocTypePassport,
	DocTypeID,
	DocTypeInvoice,
	DocTypeLetter,
}

// --- Parsing de la date ---

var dateFormats = []string{
	"02/01/2006", "2/01/2006", "02/1/2006", "2/1/2006",
	"02-01-2006", "2006-01-02",
	"02 January 2006", "2 January 2006",
	"02 janvier 2006", "2 janvier 2006",
}

var frMonths = map[string]string{
	"janvier": "January", "février": "February", "mars": "March",
	"avril": "April", "mai": "May", "juin": "June",
	"juillet": "July", "août": "August", "septembre": "September",
	"octobre": "October", "novembre": "November", "décembre": "December",
}

func normalizeFrenchDate(s string) string {
	lower := strings.ToLower(s)
	for fr, en := range frMonths {
		lower = strings.ReplaceAll(lower, fr, en)
	}
	return lower
}

func parseDate(token string) (time.Time, bool) {
	// Normaliser d'abord les formats non-standard
	// avant la normalisation des mois français
	token = normalizeRawDate(token)
	normalized := normalizeFrenchDate(token)
	for _, format := range dateFormats {
		t, err := time.Parse(format, normalized)
		if err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

var dateTokenRe = regexp.MustCompile(
	// DD/MM/YYYY, DD-MM-YYYY ou DD.MM.YYYY (point = format CNI française)
	`\b(\d{1,2}[/.\-]\d{1,2}[/.\-]\d{4})\b` +
		// YYYY/MM/DD ou YYYY-MM-DD
		`|\b(\d{4}[/\-]\d{2}[/\-]\d{2})\b` +
		// DD MM YYYY
		`|\b(\d{2}\s+\d{2}\s+\d{4})\b` +
		// DDMMYYYY collé
		`|\b(\d{2})(\d{2})(\d{4})\b` +
		// DD mois YYYY
		`|\b(\d{1,2}\s+(?:janvier|f[eé]vrier|mars|avril|mai|juin|juillet|ao[uû]t|septembre|octobre|novembre|d[eé]cembre|january|february|march|april|may|june|july|august|september|october|november|december)\s+\d{4})\b`,
)

// normalizeRawDate convertit les formats de date non-standard en DD/MM/YYYY
// pour faciliter le parsing.
func normalizeRawDate(s string) string {
	s = strings.TrimSpace(s)
	// DD MM YYYY (avec espaces)
	if spaceRe.MatchString(s) {
		parts := strings.Fields(s)
		if len(parts) == 3 {
			return parts[0] + "/" + parts[1] + "/" + parts[2]
		}
	}
	// DDMMYYYY
	if collapsedRe.MatchString(s) {
		if !plausibleDDMMYYYY(s) {
			return s // laisse tel quel : parseDate échouera, le token est écarté
		}
		return s[0:2] + "/" + s[2:4] + "/" + s[4:]
	}
	// DD.MM.YYYY (points, format CNI française) → DD/MM/YYYY
	if dotRe.MatchString(s) {
		return strings.ReplaceAll(s, ".", "/")
	}
	return s
}

var (
	spaceRe     = regexp.MustCompile(`^\d{2}\s+\d{2}\s+\d{4}$`)
	collapsedRe = regexp.MustCompile(`^\d{8}$`)
	dotRe       = regexp.MustCompile(`^\d{1,2}\.\d{1,2}\.\d{4}$`)
)

// plausibleDDMMYYYY rejette un token de 8 chiffres collés dont
// l'interprétation DDMMYYYY n'est pas un jour/mois/année plausible. Une
// référence client à 8 chiffres (ex. 11122023) reste syntaxiquement un
// jour/mois/année valide pour time.Parse — cette passe filtre au moins les
// valeurs hors bornes usuelles (année hors 1900-2099) qu'aucun format de
// date stricte n'écarterait.
func plausibleDDMMYYYY(s string) bool {
	if !collapsedRe.MatchString(s) {
		return true // pas un token collé, rien à filtrer ici
	}
	day, _ := strconv.Atoi(s[0:2])
	month, _ := strconv.Atoi(s[2:4])
	year, _ := strconv.Atoi(s[4:8])
	return day >= 1 && day <= 31 && month >= 1 && month <= 12 && year >= 1900 && year <= 2099
}

// Patterns de labels précédant une date sur les documents d'identité.
// On cherche le label dans les ~60 caractères avant la date dans le texte normalisé.
var (
	birthLabelRe  = regexp.MustCompile(`(?i)(date\s+de\s+nais|date\s+of\s+birth|naiss|birth|née?|born|dob)`)
	expiryLabelRe = regexp.MustCompile(`(?i)(expir|expiry|expiration|validit|d['']expir)`)
	issueLabelRe  = regexp.MustCompile(`(?i)(d[eé]livr|issue|émission|emit|délivrance)`)
)

// invoiceDateLabelRe détecte un contexte de datation explicite avant une
// date de facture/courrier : "date" (n'importe où dans les 60 caractères
// précédents), ou "émis le" / "facture du" / un simple "le" immédiatement
// avant la date (fréquent en tête de ligne OCR : "Le 15/03/2026"). Sert à
// préférer cette date à un nombre qui matche accidentellement un format de
// date (référence client, n° de facture...).
var invoiceDateLabelRe = regexp.MustCompile(`(?i)date|(?:[eé]mis\s+le|facture\s+du|\ble)\s*:?\s*$`)

// dateEntry associe une date à son contexte textuel (label trouvé avant elle)
type dateEntry struct {
	T       time.Time
	Raw     string
	Context string // texte précédant la date dans le document
}

// extractDateForPassport identifie les dates d'un document d'identité
// en s'appuyant en priorité sur les labels contextuels (DATE DE NAISS, EXPIRY DATE...),
// et en fallback sur la chronologie (futur=expiration, plus ancienne=naissance).
func extractDateForPassport(text string) (docDate time.Time, rawDate, issueRaw, expiryRaw, birthRaw string, found bool) {
	// Normaliser le texte une seule fois
	norm := strings.Join(strings.Fields(text), " ")
	normLow := strings.ToLower(norm)

	// Trouver toutes les dates avec leur position dans le texte normalisé
	allMatches := dateTokenRe.FindAllStringIndex(normLow, -1)
	if len(allMatches) == 0 {
		return time.Time{}, "", "", "", "", false
	}

	var entries []dateEntry
	seen := map[string]bool{}
	for _, loc := range allMatches {
		raw := strings.TrimSpace(norm[loc[0]:loc[1]])
		if seen[strings.ToLower(raw)] {
			continue
		}
		seen[strings.ToLower(raw)] = true

		normalized := normalizeRawDate(raw)
		t, ok := parseDate(normalized)
		if !ok {
			continue
		}
		// Contexte = les 60 caractères précédant la date
		ctxStart := loc[0] - 60
		if ctxStart < 0 {
			ctxStart = 0
		}
		ctx := normLow[ctxStart:loc[0]]
		entries = append(entries, dateEntry{T: t, Raw: raw, Context: ctx})
	}

	if len(entries) == 0 {
		return time.Time{}, "", "", "", "", false
	}

	// --- Étape 1 : classification par label contextuel (prioritaire) ---
	for _, e := range entries {
		switch {
		case birthLabelRe.MatchString(e.Context) && birthRaw == "":
			birthRaw = e.Raw
		case expiryLabelRe.MatchString(e.Context) && expiryRaw == "":
			expiryRaw = e.Raw
		case issueLabelRe.MatchString(e.Context) && issueRaw == "":
			issueRaw = e.Raw
			docDate = e.T
			rawDate = e.Raw
		}
	}

	// --- Étape 2 : fallback chronologique pour les dates non identifiées ---
	now := time.Now()
	for _, e := range entries {
		raw := e.Raw
		// Déjà classée par label ?
		if raw == birthRaw || raw == expiryRaw || raw == issueRaw {
			continue
		}
		switch {
		case e.T.After(now) && expiryRaw == "":
			expiryRaw = raw
		case !e.T.After(now) && e.T.Year() > 1950 && birthRaw != "" && issueRaw == "":
			// Date dans le passé, pas trop ancienne → émission
			issueRaw = raw
			docDate = e.T
			rawDate = raw
		case !e.T.After(now) && birthRaw == "":
			birthRaw = raw
		}
	}

	// Si on n'a trouvé que des dates sans contexte clair → heuristique chronologique
	if issueRaw == "" && birthRaw == "" && expiryRaw == "" {
		for _, e := range entries {
			switch {
			case e.T.After(now):
				expiryRaw = e.Raw
			case e.T.Year() <= 2010:
				birthRaw = e.Raw
			default:
				issueRaw = e.Raw
				docDate = e.T
				rawDate = e.Raw
			}
		}
	}

	// Date de référence = émission si trouvée. Pas de fallback sur entries[0]
	// car c'est souvent la date de naissance (causerait DocDate == BirthDate).
	// Si pas d'émission, la validité s'appuie sur ExpiryDate
	return docDate, rawDate, issueRaw, expiryRaw, birthRaw, true
}

// formatDate normalise une date brute vers DD/MM/YYYY pour l'affichage.
// Si non parseable, renvoie la chaîne brute
func formatDate(raw string) string {
	if raw == "" {
		return ""
	}
	if t, ok := parseDate(raw); ok {
		return t.Format("02/01/2006")
	}
	return raw
}

// extractDate retourne la date de facture/courrier la plus probable : en
// priorité une date immédiatement précédée d'un mot-clé de datation (date,
// émis le, facture du, le en tête de ligne), pour ne pas confondre une
// référence client à 8 chiffres avec la date du document. À défaut, retombe
// sur la première date plausible du texte (comportement historique, conservé
// pour ne pas casser le bench).
func extractDate(text string) (time.Time, string, bool) {
	normalized := strings.Join(strings.Fields(text), " ")
	normLow := strings.ToLower(normalized)

	allMatches := dateTokenRe.FindAllStringIndex(normLow, -1)

	var fallbackT time.Time
	var fallbackRaw string
	haveFallback := false

	for _, loc := range allMatches {
		raw := strings.TrimSpace(normalized[loc[0]:loc[1]])
		t, ok := parseDate(normalizeRawDate(raw))
		if !ok {
			continue
		}

		ctxStart := loc[0] - 60
		if ctxStart < 0 {
			ctxStart = 0
		}
		if invoiceDateLabelRe.MatchString(normLow[ctxStart:loc[0]]) {
			return t, raw, true
		}
		if !haveFallback {
			fallbackT, fallbackRaw, haveFallback = t, raw, true
		}
	}
	return fallbackT, fallbackRaw, haveFallback
}

// --- Recherche de nom ---

// accentReplacer retire les diacritiques usuels du français (translittération
// ASCII simple), stdlib seulement (pas de golang.org/x/text). Appliqué après
// ToUpper : compare "FRANÇOIS" attendu à "FRANCOIS" tel que l'OCR le rend
// souvent (cédille/accent perdu).
var accentReplacer = strings.NewReplacer(
	"À", "A", "Á", "A", "Â", "A", "Ã", "A", "Ä", "A", "Å", "A",
	"Æ", "AE",
	"Ç", "C",
	"È", "E", "É", "E", "Ê", "E", "Ë", "E",
	"Ì", "I", "Í", "I", "Î", "I", "Ï", "I",
	"Ñ", "N",
	"Ò", "O", "Ó", "O", "Ô", "O", "Õ", "O", "Ö", "O",
	"Ù", "U", "Ú", "U", "Û", "U", "Ü", "U",
	"Ý", "Y",
	"Œ", "OE",
)

// wordBoundaryContains vérifie que needle apparaît dans haystack sur une
// frontière de mot, pas comme sous-chaîne brute. Empêche un nom court
// ("LE") ou un code postal de matcher à l'intérieur d'un mot plus long ou
// d'une suite de chiffres (téléphone, SIRET).
func wordBoundaryContains(haystack, needle string) bool {
	if needle == "" {
		return false
	}
	re, err := regexp.Compile(`\b` + regexp.QuoteMeta(needle) + `\b`)
	if err != nil {
		return strings.Contains(haystack, needle) // filet de sécurité, ne devrait jamais arriver
	}
	return re.MatchString(haystack)
}

// searchName cherche un nom (et optionnellement un prénom) dans le texte OCR.
// Stratégies testées dans l'ordre :
//  1. Nom seul présent
//  2. Prénom seul présent
//  3. "Nom Prénom" (dans cet ordre)
//  4. "Prénom Nom" (ordre inversé)
//  5. Nom et prénom présents séparément n'importe où dans le texte
//
// Retourne (nameFound, surnameFound, description).
func searchName(text, name, surname string) (bool, bool, string) {
	upper := accentReplacer.Replace(strings.ToUpper(text))
	if mrzName := mrzExpandName(upper); mrzName != "" {
		upper += "\n" + mrzName
	}
	uName := accentReplacer.Replace(strings.ToUpper(strings.TrimSpace(name)))
	uSurname := accentReplacer.Replace(strings.ToUpper(strings.TrimSpace(surname)))

	nameFound := uName != "" && wordBoundaryContains(upper, uName)
	surnameFound := uSurname != "" && wordBoundaryContains(upper, uSurname)

	// Cas : nom seul (pas de prénom fourni)
	if uSurname == "" {
		if nameFound {
			return true, false, fmt.Sprintf("'%s' trouvé", name)
		}
		return false, false, fmt.Sprintf("'%s' non trouvé", name)
	}

	// Cas : nom ET prénom fournis — tester toutes les combinaisons
	bothFound := nameFound && surnameFound

	// Vérifier si la combinaison "Nom Prénom" ou "Prénom Nom" apparaît côte à côte
	// (pas forcément adjacents, mais sur la même ligne ou à proximité)
	togetherNP := strings.Contains(upper, uName+" "+uSurname) // NOM PRENOM
	togetherPN := strings.Contains(upper, uSurname+" "+uName) // PRENOM NOM

	// Vérifier aussi avec séparateur newline (ex: "NOM\nPRENOM")
	togetherNL := strings.Contains(upper, uName+"\n"+uSurname) ||
		strings.Contains(upper, uSurname+"\n"+uName)

	together := togetherNP || togetherPN || togetherNL

	switch {
	case together:
		order := "NOM PRÉNOM"
		if togetherPN {
			order = "PRÉNOM NOM"
		}
		return true, true, fmt.Sprintf("'%s %s' trouvés ensemble (%s)", name, surname, order)
	case bothFound:
		return true, true, fmt.Sprintf("'%s' et '%s' trouvés séparément", name, surname)
	case nameFound && !surnameFound:
		return true, false, fmt.Sprintf("'%s' trouvé, '%s' absent", name, surname)
	case !nameFound && surnameFound:
		return false, true, fmt.Sprintf("'%s' absent, '%s' trouvé", name, surname)
	default:
		return false, false, fmt.Sprintf("'%s' et '%s' non trouvés", name, surname)
	}
}

// --- Analyse principale ---

func AnalyzeText(text string, input Input) AnalysisResult {
	result := AnalysisResult{RawText: text}

	// 0. Détection précoce d'un document illisible : OCR vide ou trop court.
	if len(strings.TrimSpace(text)) < minOCRChars {
		result.DocType = DocTypeUnknown
		result.Unreadable = true
		result.OK = false
		result.Confidence = confNul
		result.Errors = append(result.Errors, "Document illisible — review manuelle requise")
		return result
	}

	// 1. Détection du type de document
	result.DocType = DetectDocType(text)

	// 2. Recherche du nom (et prénom si fourni). Le verdict final décide si
	// l'absence est rédhibitoire ; ici on se contente d'extraire.
	nameFound, surnameFound, _ := searchName(text, input.Name, input.Surname)
	result.NameFound = nameFound
	if input.Surname != "" {
		result.HasSurname = true
		result.SurnameFound = surnameFound
	}

	// 2. Code postal — non applicable pour passeports et CNI
	if result.DocType == DocTypePassport || result.DocType == DocTypeID {
		result.PostalFound = true // non requis pour documents d'identité
	} else {
		foundPostal := ""
		for _, cp := range input.PostalCodes {
			if wordBoundaryContains(text, cp) {
				foundPostal = cp
				break
			}
		}
		if foundPostal != "" {
			result.PostalFound = true
			result.PostalMatch = foundPostal
		}
	}

	// 3. Date — stratégie différente selon le type de document
	validMonths := input.ValidMonths
	if validMonths <= 0 {
		validMonths = 3
	}

	switch result.DocType {
	case DocTypePassport, DocTypeID:
		// Documents d'identité : dates d'émission, d'expiration et de naissance.
		docDate, rawDate, issueRaw, expiryRaw, birthRaw, found := extractDateForPassport(text)
		if found {
			result.DateFound = true
			result.DocDate = formatDate(rawDate)
			if issueRaw != "" {
				result.IssueDate = formatDate(issueRaw)
			}
			if birthRaw != "" {
				result.BirthDate = formatDate(birthRaw)
			}
			if expiryRaw != "" {
				result.ExpiryDate = formatDate(expiryRaw)
				// Valide si la date d'expiration est dans le futur.
				if t, ok := parseDate(expiryRaw); ok && t.After(time.Now()) {
					result.DateValid = true
				}
			} else {
				// Sans expiration : date d'émission + fenêtre de validité.
				if docDate.After(time.Now().AddDate(0, -validMonths, 0)) {
					result.DateValid = true
				}
			}
		}

	default:
		// Factures, courriers : première date trouvée + fenêtre de validité.
		docDate, rawDate, found := extractDate(text)
		if found {
			result.DateFound = true
			result.DocDate = formatDate(rawDate)
			if docDate.After(time.Now().AddDate(0, -validMonths, 0)) {
				result.DateValid = true
			}
		}
	}

	// Document illisible : type inconnu ET aucun signal exploitable.
	// On ne refuse pas sèchement : on marque pour review humaine côté Regia.
	if result.DocType == DocTypeUnknown && !result.NameFound && !result.PostalFound && !result.DateFound {
		result.Unreadable = true
		result.OK = false
		result.Confidence = confNul
		result.Errors = append(result.Errors, "Document illisible — review manuelle requise")
		return result
	}

	// Verdict : contrôles implicites dérivés du type détecté, évalués par
	// l'unique fonction de décision partagée avec le mode batch.
	result.OK, result.Errors = evaluateVerdict(result, verdictSpec{
		checks:      defaultChecks(result.DocType, input.Surname != ""),
		name:        input.Name,
		surname:     input.Surname,
		postalCodes: input.PostalCodes,
	})
	result.Confidence = computeConfidence(result)
	return result
}

// verdictSpec décrit les contrôles à appliquer pour rendre un verdict et les
// valeurs attendues servant aux messages. C'est l'unique entrée de
// evaluateVerdict, partagée par le mode mono-document (AnalyzeText, checks
// dérivés du type) et le mode batch (EvaluateChecks, checks explicites).
type verdictSpec struct {
	checks        []string
	name          string
	surname       string
	postalCodes   []string
	cities        []string
	expectedBirth string
}

// defaultChecks retourne les contrôles implicites du mode mono-document selon
// le type détecté : un document d'identité prouve l'existence d'une personne
// (nom + document non expiré), un justificatif de domicile exige en plus le
// code postal. Le prénom n'est contrôlé que s'il a été fourni.
func defaultChecks(docType DocType, hasSurname bool) []string {
	checks := []string{"name"}
	if hasSurname {
		checks = append(checks, "surname")
	}
	if docType != DocTypePassport && docType != DocTypeID {
		checks = append(checks, "postal")
	}
	return append(checks, "date")
}

// evaluateVerdict est l'unique source de vérité du verdict. Il lit les champs
// déjà extraits dans AnalysisResult et décide, contrôle par contrôle, si le
// document satisfait ceux demandés, en ignorant ceux qui ne s'appliquent pas
// au type détecté (code postal sur une CNI, naissance sur une facture).
func evaluateVerdict(a AnalysisResult, spec verdictSpec) (bool, []string) {
	isIdentity := a.DocType == DocTypePassport || a.DocType == DocTypeID
	isAddress := a.DocType == DocTypeInvoice || a.DocType == DocTypeLetter

	want := map[string]bool{}
	for _, c := range spec.checks {
		want[c] = true
	}

	ok := true
	var errs []string
	fail := func(msg string) {
		ok = false
		errs = append(errs, msg)
	}

	if want["name"] && !a.NameFound {
		fail(fmt.Sprintf("Nom '%s' non trouvé", spec.name))
	}
	if want["surname"] && !a.SurnameFound {
		fail(fmt.Sprintf("Prénom '%s' non trouvé", spec.surname))
	}
	if want["postal"] && !isIdentity && !a.PostalFound {
		fail(fmt.Sprintf("Aucun des codes postaux %v trouvé dans le document", spec.postalCodes))
	}
	if want["city"] && !isIdentity {
		found := false
		for _, city := range spec.cities {
			if city != "" && strings.Contains(strings.ToUpper(a.RawText), strings.ToUpper(city)) {
				found = true
				break
			}
		}
		if !found {
			fail(fmt.Sprintf("Ville %v non trouvée", spec.cities))
		}
	}
	if want["date"] {
		switch {
		case isIdentity && !a.DateValid:
			if a.ExpiryDate != "" {
				fail(fmt.Sprintf("Document expiré (expiration : %s)", a.ExpiryDate))
			} else {
				fail("Document expiré ou date d'expiration non trouvée")
			}
		case !isIdentity && !a.DateFound:
			fail("Date du document non trouvée")
		case !isIdentity && !a.DateValid:
			fail("Document trop ancien")
		}
	}
	if want["birth_date"] && !isAddress {
		switch {
		case spec.expectedBirth == "" && a.BirthDate == "":
			fail("Date de naissance non trouvée dans le document")
		case spec.expectedBirth != "" && normalizeBirth(a.BirthDate) != normalizeBirth(spec.expectedBirth):
			fail(fmt.Sprintf("Date de naissance attendue '%s', trouvée '%s'", spec.expectedBirth, a.BirthDate))
		}
	}
	return ok, errs
}

// normalizeBirth ramène une date de naissance à un format comparable
// (séparateurs uniformisés, minuscules) pour confronter attendu et extrait.
func normalizeBirth(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, " ", "/")
	s = strings.ReplaceAll(s, "-", "/")
	return strings.ToLower(s)
}

// ── Types métier ──────────────────────────────────────────────────────────────

// Individual représente un membre d'un dossier (mode batch).
type Individual struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Surname     string   `json:"surname"`
	BirthDate   string   `json:"birth_date"`
	PostalCodes []string `json:"postal_codes"`
	Cities      []string `json:"cities"`
	Referent    bool     `json:"referent"`
}

// Document représente un fichier à vérifier dans un batch.
type Document struct {
	ID          string   `json:"id"`
	Path        string   `json:"path"`
	Individual  string   `json:"individual"`
	Checks      []string `json:"checks"`
	Label       string   `json:"label"`
	ValidMonths int      `json:"valid_months"`
}

// BatchInput est le format d'entrée pour un batch (individus + documents).
// LLMURL cible un endpoint OpenAI-compatible (Docker Model Runner, vLLM, Ollama via /v1).
type BatchInput struct {
	Individuals []Individual `json:"individuals"`
	Documents   []Document   `json:"documents"`
	Model       string       `json:"model"`
	LLMURL      string       `json:"llm_url"`
}

// Input est l'ancien format mono-document (rétrocompatibilité).
type Input struct {
	Name         string   `json:"name"`
	Surname      string   `json:"surname"`
	ValidMonths  int      `json:"valid_months"`
	PostalCodes  []string `json:"postal_codes"`
	DocumentPath string   `json:"document_path"`
	Model        string   `json:"model"`
	LLMURL       string   `json:"llm_url"`
}

// ReferentAddress stocke l'adresse validée du référent pour propagation.
type ReferentAddress struct {
	PostalMatch string
	CityMatch   string
}

// DocResult est le résultat d'évaluation d'un document.
type DocResult struct {
	DocumentID          string         `json:"document_id"`
	Label               string         `json:"label,omitempty"`
	IndividualID        string         `json:"individual_id"`
	Checks              []string       `json:"checks"`
	OK                  bool           `json:"ok"`
	AddressFromReferent bool           `json:"address_from_referent,omitempty"`
	Errors              []string       `json:"errors,omitempty"`
	Analysis            AnalysisResult `json:"analysis"`
}

// BuildInput construit un Input adapté à l'individu et aux checks demandés.
func BuildInput(ind Individual, doc Document, batch BatchInput) Input {
	checksSet := map[string]bool{}
	for _, c := range doc.Checks {
		checksSet[c] = true
	}
	input := Input{
		Name:         ind.Name,
		Surname:      ind.Surname,
		DocumentPath: doc.Path,
		ValidMonths:  doc.ValidMonths,
		Model:        batch.Model,
		LLMURL:       batch.LLMURL,
	}
	if checksSet["postal"] {
		input.PostalCodes = ind.PostalCodes
	}
	return input
}

// EvaluateChecks applique les contrôles explicites d'un document de batch via
// l'unique fonction de verdict partagée avec AnalyzeText. Les valeurs attendues
// (nom, code postal, ville, naissance) proviennent de l'individu.
func EvaluateChecks(analysis AnalysisResult, doc Document, ind Individual) DocResult {
	ok, errs := evaluateVerdict(analysis, verdictSpec{
		checks:        doc.Checks,
		name:          ind.Name,
		surname:       ind.Surname,
		postalCodes:   ind.PostalCodes,
		cities:        ind.Cities,
		expectedBirth: ind.BirthDate,
	})
	analysis.Errors = errs
	analysis.OK = ok

	return DocResult{
		DocumentID:   doc.ID,
		Label:        doc.Label,
		IndividualID: ind.ID,
		Checks:       doc.Checks,
		OK:           ok,
		Errors:       errs,
		Analysis:     analysis,
	}
}
