package docverify

import (
	"fmt"
	"regexp"
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
	DateFound  bool     `json:"date_found"`
	DateValid  bool     `json:"date_valid"`
	DocDate    string   `json:"doc_date,omitempty"`
	ExpiryDate string   `json:"expiry_date,omitempty"` // date d'expiration (passeport/CNI)
	IssueDate  string   `json:"issue_date,omitempty"`  // date d'émission (passeport/CNI)
	BirthDate  string   `json:"birth_date,omitempty"`  // date de naissance (passeport/CNI)
	Unreadable bool     `json:"unreadable,omitempty"`  // document illisible, review humaine requise
	Errors     []string `json:"errors,omitempty"`
	RawText    string   `json:"raw_text,omitempty"`
}

// minOCRChars : seuil sous lequel on considère que l'OCR a échoué.
// En dessous, le LLM a probablement renvoyé un message d'erreur ou rien.
const minOCRChars = 30

// --- Détection du type de document ---

// Mots-clés par type de document.
//
// TODO: ajouter une détection MRZ par regex `^P<[A-Z]{3}` pour reconnaître
// n'importe quel passeport indépendamment de la langue. Cas réel : passeports
// marocains (BEN AMAR) où le LLM ne transcrit pas "passport"/"passeport"
// et où le doc tombe en doc_type=inconnu.
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

// DetectDocType analyse le texte OCR et retourne le type de document le plus probable.
func DetectDocType(text string) DocType {
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
	for dt, score := range scores {
		if score > bestScore {
			bestScore = score
			bestType = dt
		}
	}
	return bestType
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
	// DD/MM/YYYY ou DD-MM-YYYY
	`\b(\d{1,2}[/\-]\d{1,2}[/\-]\d{4})\b` +
		// YYYY/MM/DD ou YYYY-MM-DD
		`|\b(\d{4}[/\-]\d{2}[/\-]\d{2})\b` +
		// DD MM YYYY
		`|\b(\d{2}\s+\d{2}\s+\d{4})\b` +
		// DDMMYYYY collé
		`|\b(\d{2})(\d{2})(\d{4})\b` +
		// DD mois YYYY
		`|\b(\d{1,2}\s+(?:janvier|f[eé]vrier|mars|avril|mai|juin|juillet|ao[uû]t|septembre|octobre|novembre|d[eé]cembre|january|february|march|april|may|june|july|august|september|october|november|december)\s+\d{4})\b`,
)

// extractAllDates retourne toutes les dates trouvées dans le texte.
func extractAllDates(text string) []time.Time {
	normalized := strings.Join(strings.Fields(text), " ")
	matches := dateTokenRe.FindAllString(strings.ToLower(normalized), -1)
	var dates []time.Time
	seen := map[string]bool{}
	for _, match := range matches {
		match = strings.TrimSpace(match)
		if seen[match] {
			continue
		}
		seen[match] = true
		if t, ok := parseDate(match); ok {
			dates = append(dates, t)
		}
	}
	return dates
}

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
		return s[0:2] + "/" + s[2:4] + "/" + s[4:]
	}
	return s
}

var (
	spaceRe     = regexp.MustCompile(`^\d{2}\s+\d{2}\s+\d{4}$`)
	collapsedRe = regexp.MustCompile(`^\d{8}$`)
)

// extractAllDatesWithRaw retourne toutes les dates avec leur représentation textuelle.
func extractAllDatesWithRaw(text string) []struct {
	T   time.Time
	Raw string
} {
	normalized := strings.Join(strings.Fields(text), " ")
	matches := dateTokenRe.FindAllString(strings.ToLower(normalized), -1)
	var result []struct {
		T   time.Time
		Raw string
	}
	seen := map[string]bool{}
	for _, match := range matches {
		raw := strings.TrimSpace(match)
		if seen[raw] {
			continue
		}
		seen[raw] = true
		// Normaliser avant parsing
		normalized := normalizeRawDate(raw)
		if t, ok := parseDate(normalized); ok {
			result = append(result, struct {
				T   time.Time
				Raw string
			}{t, raw})
		}
	}
	return result
}

// Patterns de labels précédant une date sur les documents d'identité.
// On cherche le label dans les ~60 caractères avant la date dans le texte normalisé.
var (
	birthLabelRe  = regexp.MustCompile(`(?i)(date\s+de\s+nais|date\s+of\s+birth|naiss|birth|née?|born|dob)`)
	expiryLabelRe = regexp.MustCompile(`(?i)(expir|expiry|expiration|validit|d['']expir)`)
	issueLabelRe  = regexp.MustCompile(`(?i)(d[eé]livr|issue|émission|emit|délivrance)`)
)

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

// extractDate retourne la première date trouvée (pour factures/courriers).
func extractDate(text string) (time.Time, string, bool) {
	normalized := strings.Join(strings.Fields(text), " ")
	matches := dateTokenRe.FindAllString(strings.ToLower(normalized), -1)
	for _, match := range matches {
		raw := strings.TrimSpace(match)
		norm := normalizeRawDate(raw)
		if t, ok := parseDate(norm); ok {
			return t, raw, true
		}
	}
	return time.Time{}, "", false
}

// --- Recherche de nom ---

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
	upper := strings.ToUpper(text)
	uName := strings.ToUpper(strings.TrimSpace(name))
	uSurname := strings.ToUpper(strings.TrimSpace(surname))

	nameFound := uName != "" && strings.Contains(upper, uName)
	surnameFound := uSurname != "" && strings.Contains(upper, uSurname)

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
		result.Errors = append(result.Errors, "Document illisible — review manuelle requise")
		return result
	}

	// 1. Détection du type de document
	result.DocType = DetectDocType(text)

	// 1. Recherche du nom (et prénom si fourni)
	nameFound, surnameFound, nameMsg := searchName(text, input.Name, input.Surname)
	result.NameFound = nameFound
	if input.Surname != "" {
		result.HasSurname = true
		result.SurnameFound = surnameFound
	}
	// Valider : si prénom fourni, les deux doivent être trouvés
	nameOK := nameFound && (input.Surname == "" || surnameFound)
	if !nameOK {
		result.Errors = append(result.Errors, nameMsg)
	}

	// 2. Code postal — non applicable pour passeports et CNI
	if result.DocType == DocTypePassport || result.DocType == DocTypeID {
		result.PostalFound = true // non requis pour documents d'identité
	} else {
		foundPostal := ""
		for _, cp := range input.PostalCodes {
			if strings.Contains(text, cp) {
				foundPostal = cp
				break
			}
		}
		if foundPostal != "" {
			result.PostalFound = true
			result.PostalMatch = foundPostal
		} else {
			result.Errors = append(result.Errors,
				fmt.Sprintf("Aucun des codes postaux %v trouvé dans le document", input.PostalCodes))
		}
	}

	// 3. Date — stratégie différente selon le type de document
	validMonths := input.ValidMonths
	if validMonths <= 0 {
		validMonths = 3
	}

	switch result.DocType {
	case DocTypePassport, DocTypeID:
		// Pour les documents d'identité : chercher la date d'émission et d'expiration
		docDate, rawDate, issueRaw, expiryRaw, birthRaw, found := extractDateForPassport(text)
		if !found {
			result.Errors = append(result.Errors, "Aucune date trouvée dans le document")
		} else {
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
				// Validité passeport : date d'expiration dans le futur
				expiryTime, ok := func() (time.Time, bool) {
					t, ok := parseDate(expiryRaw)
					return t, ok
				}()
				if ok && expiryTime.After(time.Now()) {
					result.DateValid = true
				} else {
					result.Errors = append(result.Errors,
						fmt.Sprintf("Document expiré : expiration=%s", expiryRaw))
				}
			} else {
				// Pas d'expiration trouvée : utiliser la date d'émission + validité
				limit := time.Now().AddDate(0, -validMonths, 0)
				if docDate.After(limit) {
					result.DateValid = true
				} else {
					result.Errors = append(result.Errors,
						fmt.Sprintf("Document trop ancien : date=%s, limite=%s (%d mois)",
							docDate.Format("02/01/2006"), limit.Format("02/01/2006"), validMonths))
				}
			}
		}

	default:
		// Factures, courriers : première date trouvée
		docDate, rawDate, found := extractDate(text)
		if !found {
			result.Errors = append(result.Errors, "Aucune date trouvée dans le document")
		} else {
			result.DateFound = true
			result.DocDate = formatDate(rawDate)
			limit := time.Now().AddDate(0, -validMonths, 0)
			if docDate.After(limit) {
				result.DateValid = true
			} else {
				result.Errors = append(result.Errors,
					fmt.Sprintf("Document trop ancien : date=%s, limite=%s (%d mois)",
						docDate.Format("02/01/2006"), limit.Format("02/01/2006"), validMonths))
			}
		}
	}

	surnameOK := input.Surname == "" || result.SurnameFound

	// Document illisible : type inconnu ET aucun signal exploitable.
	// On ne refuse pas sèchement : on marque pour review humaine côté Regia.
	if result.DocType == DocTypeUnknown && !result.NameFound && !result.PostalFound && !result.DateFound {
		result.Unreadable = true
		result.OK = false
		result.Errors = append(result.Errors, "Document illisible — review manuelle requise")
		return result
	}

	switch result.DocType {
	case DocTypePassport, DocTypeID:
		// CNI / Passeport : prouve l'existence de la personne
		// ✅ requis  : nom, prénom (si fourni), date de naissance, document non expiré
		// ❌ ignoré  : date "récente de moins de N mois" (non pertinent)
		// ❌ ignoré  : code postal (n'existe pas sur ce type de document)
		result.OK = result.NameFound && surnameOK && result.DateValid
		// DateValid = non expiré (géré dans le switch ci-dessus)

	case DocTypeInvoice, DocTypeLetter:
		// Facture / Courrier : justificatif de domicile récent
		// ✅ requis  : nom, code postal (si fourni), date récente
		// ❌ ignoré  : date de naissance (n'existe pas sur ce type de document)
		result.OK = result.NameFound && surnameOK && result.PostalFound && result.DateFound && result.DateValid

	default:
		// Type inconnu : tout vérifier
		result.OK = result.NameFound && surnameOK && result.PostalFound && result.DateFound && result.DateValid
	}

	return result
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
// LLMURL cible un endpoint OpenAI-compatible (vllm-mlx, vLLM, Ollama via /v1).
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

// EvaluateChecks évalue uniquement les checks demandés sur l'analyse,
// en tenant compte du type de document détecté pour ignorer les checks non pertinents.
func EvaluateChecks(analysis AnalysisResult, doc Document, ind Individual) DocResult {
	checksSet := map[string]bool{}
	for _, c := range doc.Checks {
		checksSet[c] = true
	}

	isIdentityDoc := analysis.DocType == DocTypePassport || analysis.DocType == DocTypeID
	isAddressDoc := analysis.DocType == DocTypeInvoice || analysis.DocType == DocTypeLetter

	ok := true
	var errors []string

	if checksSet["name"] && !analysis.NameFound {
		ok = false
		errors = append(errors, fmt.Sprintf("Nom '%s' non trouvé", ind.Name))
	}
	if checksSet["surname"] && !analysis.SurnameFound {
		ok = false
		errors = append(errors, fmt.Sprintf("Prénom '%s' non trouvé", ind.Surname))
	}
	if checksSet["postal"] {
		if isIdentityDoc {
			analysis.PostalFound = true
		} else if !analysis.PostalFound {
			ok = false
			errors = append(errors, fmt.Sprintf("Code postal %v non trouvé", ind.PostalCodes))
		}
	}
	if checksSet["city"] && !isIdentityDoc {
		cityFound := false
		for _, city := range ind.Cities {
			if strings.Contains(strings.ToUpper(analysis.RawText), strings.ToUpper(city)) {
				cityFound = true
				break
			}
		}
		if !cityFound {
			ok = false
			errors = append(errors, fmt.Sprintf("Ville %v non trouvée", ind.Cities))
		}
	}
	if checksSet["date"] {
		if isIdentityDoc {
			if !analysis.DateValid {
				ok = false
				if analysis.ExpiryDate != "" {
					errors = append(errors, fmt.Sprintf("Document expiré (expiration : %s)", analysis.ExpiryDate))
				} else {
					errors = append(errors, "Document expiré ou date d'expiration non trouvée")
				}
			}
		} else if !analysis.DateFound || !analysis.DateValid {
			ok = false
			if !analysis.DateFound {
				errors = append(errors, "Date du document non trouvée")
			} else {
				errors = append(errors, "Document trop ancien")
			}
		}
	}
	if checksSet["birth_date"] && !isAddressDoc {
		if ind.BirthDate == "" {
			if analysis.BirthDate == "" {
				ok = false
				errors = append(errors, "Date de naissance non trouvée dans le document")
			}
		} else {
			norm := func(s string) string {
				s = strings.TrimSpace(s)
				s = strings.ReplaceAll(s, " ", "/")
				s = strings.ReplaceAll(s, "-", "/")
				return strings.ToLower(s)
			}
			birthOK := analysis.BirthDate != "" && norm(analysis.BirthDate) == norm(ind.BirthDate)
			if !birthOK {
				ok = false
				errors = append(errors, fmt.Sprintf(
					"Date de naissance attendue '%s', trouvée '%s'",
					ind.BirthDate, analysis.BirthDate))
			}
		}
	}

	analysis.Errors = errors
	analysis.OK = ok

	return DocResult{
		DocumentID:   doc.ID,
		Label:        doc.Label,
		IndividualID: ind.ID,
		Checks:       doc.Checks,
		OK:           ok,
		Errors:       errors,
		Analysis:     analysis,
	}
}
