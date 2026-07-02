package docverify

import "testing"

func TestParseDateSeparators(t *testing.T) {
	cases := []struct {
		in string
		ok bool
	}{
		{"31/05/2030", true},
		{"31-05-2030", true},
		{"31.05.2030", true}, // séparateur point : CNI papier (fix 2026-05-29)
		{"2030-05-31", true}, // ISO YYYY-MM-DD
		{"31 05 2030", true}, // espaces
		{"31052030", true},   // collé DDMMYYYY
		{"1 janvier 2020", true},
		{"1 January 2020", true},
		{"pas une date", false},
		{"99/99/9999", false},
		{"", false},
	}
	for _, c := range cases {
		if _, ok := parseDate(c.in); ok != c.ok {
			t.Errorf("parseDate(%q) ok=%v, attendu %v", c.in, ok, c.ok)
		}
	}
}

func TestNormalizeRawDate(t *testing.T) {
	cases := []struct{ in, want string }{
		{"31.05.2030", "31/05/2030"}, // point -> slash
		{"31 05 2030", "31/05/2030"}, // espaces -> slash
		{"31052030", "31/05/2030"},   // collé -> slash
		{"31/05/2030", "31/05/2030"}, // déjà ok, inchangé
	}
	for _, c := range cases {
		if got := normalizeRawDate(c.in); got != c.want {
			t.Errorf("normalizeRawDate(%q) = %q, attendu %q", c.in, got, c.want)
		}
	}
}

func TestDetectDocType(t *testing.T) {
	cases := []struct {
		name string
		text string
		want DocType
	}{
		{"MRZ CNI prioritaire", "texte bruité IDFRADURAND<<< blabla", DocTypeID},
		{"MRZ passeport", "ligne P<MARBENAMAR<<< autre", DocTypePassport},
		{"facture mots-clés", "FACTURE total TTC TVA échéance montant dû", DocTypeInvoice},
		{"courrier mots-clés", "Madame, Monsieur, veuillez... cordialement", DocTypeLetter},
		{"CNI mots-clés", "carte nationale d'identité république française", DocTypeID},
		{"inconnu", "blabla sans signal particulier xyz", DocTypeUnknown},
	}
	for _, c := range cases {
		if got := DetectDocType(c.text); got != c.want {
			t.Errorf("%s: DetectDocType = %q, attendu %q", c.name, got, c.want)
		}
	}
}

func TestDetectDocTypeDeterministe(t *testing.T) {
	// Le tie-break par docTypeOrder doit rendre le verdict stable sur N runs
	// (Go randomise l'itération de map ; sans ordre stable, ça alternait).
	text := "carte nationale d'identité république française passeport nationalité"
	first := DetectDocType(text)
	for i := 0; i < 50; i++ {
		if got := DetectDocType(text); got != first {
			t.Fatalf("DetectDocType non déterministe: run %d = %q, run 0 = %q", i, got, first)
		}
	}
}

func TestExtractDateForPassportPoints(t *testing.T) {
	// CNI papier (solange) : dates au format point. Avant le fix, found=false.
	txt := "Né le 01.09.1959 Carte valable jusqu'au 31.05.2030 Délivrée le 01.06.2015"
	_, _, issue, expiry, birth, found := extractDateForPassport(txt)
	if !found {
		t.Fatal("extractDateForPassport: found=false sur dates à points (régression du fix séparateur)")
	}
	if birth == "" || expiry == "" || issue == "" {
		t.Errorf("dates non classées: birth=%q expiry=%q issue=%q", birth, expiry, issue)
	}
}

func TestExtractDateForPassportSansDate(t *testing.T) {
	_, _, _, _, _, found := extractDateForPassport("aucune date ici, juste du texte")
	if found {
		t.Error("extractDateForPassport: found=true sur un texte sans date")
	}
}

func TestSearchName(t *testing.T) {
	cases := []struct {
		text, name, surname string
		wantName, wantSurn  bool
	}{
		{"DUPONT JEAN habite ici", "DUPONT", "JEAN", true, true}, // NOM PRÉNOM
		{"JEAN DUPONT", "DUPONT", "JEAN", true, true},            // PRÉNOM NOM
		{"DUPONT seul", "DUPONT", "JEAN", true, false},           // nom seul
		{"rien de pertinent", "DUPONT", "JEAN", false, false},
		{"DUPONT", "DUPONT", "", true, false}, // sans prénom fourni
		{"absent", "DUPONT", "", false, false},
	}
	for _, c := range cases {
		gotName, gotSurn, _ := searchName(c.text, c.name, c.surname)
		if gotName != c.wantName || gotSurn != c.wantSurn {
			t.Errorf("searchName(%q,%q,%q) = (%v,%v), attendu (%v,%v)",
				c.text, c.name, c.surname, gotName, gotSurn, c.wantName, c.wantSurn)
		}
	}
}

func TestSearchNameAccentInsensitive(t *testing.T) {
	found, _, _ := searchName("PIECE AU NOM DE FRANCOIS MARTIN", "FRANÇOIS", "")
	if !found {
		t.Error("searchName devrait matcher FRANÇOIS malgré le F sans cédille OCRisé")
	}
}

func TestSearchNameWordBoundary(t *testing.T) {
	if found, _, _ := searchName("UNE TABLETTE POSEE SUR LA TABLE", "LE", ""); found {
		t.Error("searchName ne devrait pas matcher 'LE' à l'intérieur de 'TABLETTE'/'TABLE'")
	}
	if found, _, _ := searchName("MERCI DE VOUS ADRESSER A MONSIEUR LE MAIRE", "LE", ""); !found {
		t.Error("searchName devrait matcher 'LE' isolé")
	}
}

func TestAnalyzeTextPostalWordBoundary(t *testing.T) {
	text := "FACTURE Total TTC TVA Tel: 0600000000"
	result := AnalyzeText(text, Input{Name: "X", PostalCodes: []string{"99000"}})
	if result.PostalFound {
		t.Error("PostalFound=true, le code postal ne devrait pas matcher à l'intérieur d'un numéro de téléphone")
	}
}

func TestAnalyzeTextPostalWordBoundaryPositive(t *testing.T) {
	text := "FACTURE Total TTC TVA 99000 VILLEFRANCHE"
	result := AnalyzeText(text, Input{Name: "X", PostalCodes: []string{"99000"}})
	if !result.PostalFound {
		t.Error("PostalFound=false, le code postal isolé aurait dû matcher")
	}
}

func TestScoreAnalysis(t *testing.T) {
	// DocType doit être explicitement DocTypeUnknown : le zero-value "" de
	// DocType compterait à tort (""!="inconnu"). En pratique AnalyzeText
	// renseigne toujours DocType via DetectDocType, donc ce cas n'arrive pas.
	empty := scoreAnalysis(AnalysisResult{DocType: DocTypeUnknown})
	if empty != 0 {
		t.Errorf("scoreAnalysis(rien trouvé) = %d, attendu 0", empty)
	}
	full := scoreAnalysis(AnalysisResult{
		NameFound: true, SurnameFound: true,
		PostalFound: true, PostalMatch: "99000",
		DateFound: true, DateValid: true,
		BirthDate: "01/01/1990", IssueDate: "01/01/2020", ExpiryDate: "01/01/2030",
		DocType: DocTypeID,
	})
	if full != 9 {
		t.Errorf("scoreAnalysis(complet) = %d, attendu 9", full)
	}
}

func TestEvaluateVerdictCNINonExpiree(t *testing.T) {
	// CNI : nom + non expiré requis, code postal ignoré.
	a := AnalysisResult{DocType: DocTypeID, NameFound: true, DateValid: true}
	ok, errs := evaluateVerdict(a, verdictSpec{checks: defaultChecks(DocTypeID, false), name: "X"})
	if !ok {
		t.Errorf("CNI valide jugée invalide: %v", errs)
	}
	// CNI expirée -> invalide
	a.DateValid = false
	ok, _ = evaluateVerdict(a, verdictSpec{checks: defaultChecks(DocTypeID, false), name: "X"})
	if ok {
		t.Error("CNI expirée jugée valide")
	}
}

func TestEvaluateVerdictFacturePostalRequis(t *testing.T) {
	// Facture : nom + code postal + date récente requis.
	a := AnalysisResult{DocType: DocTypeInvoice, NameFound: true, PostalFound: true, DateFound: true, DateValid: true}
	ok, errs := evaluateVerdict(a, verdictSpec{checks: defaultChecks(DocTypeInvoice, false), name: "X"})
	if !ok {
		t.Errorf("facture valide jugée invalide: %v", errs)
	}
	// Sans code postal -> invalide
	a.PostalFound = false
	ok, _ = evaluateVerdict(a, verdictSpec{checks: defaultChecks(DocTypeInvoice, false), name: "X"})
	if ok {
		t.Error("facture sans code postal jugée valide")
	}
}

func TestPlausibleDDMMYYYY(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"15032026", true},  // 15/03/2026 plausible
		{"01011800", false}, // année hors 1900-2099
		{"32132023", false}, // jour 32 et mois 13 hors bornes
		{"abcdefgh", true},  // pas un token collé (non numérique) → rien à filtrer ici
	}
	for _, c := range cases {
		if got := plausibleDDMMYYYY(c.in); got != c.want {
			t.Errorf("plausibleDDMMYYYY(%q) = %v, attendu %v", c.in, got, c.want)
		}
	}
}

func TestExtractDatePrefersKeywordOverReference(t *testing.T) {
	text := "Référence client : 11122023 Date d'émission : 15/03/2026"
	_, raw, found := extractDate(text)
	if !found {
		t.Fatal("extractDate: found=false, attendu une date")
	}
	if raw != "15/03/2026" {
		t.Errorf("extractDate = %q, attendu la date étiquetée '15/03/2026', pas la référence client", raw)
	}
}

func TestExtractDateFallsBackWithoutKeyword(t *testing.T) {
	text := "Aucun mot-clé ici, juste 15/03/2026 quelque part"
	_, raw, found := extractDate(text)
	if !found || raw != "15/03/2026" {
		t.Errorf("extractDate fallback = (%q, %v), attendu (\"15/03/2026\", true)", raw, found)
	}
}

func TestComputeConfidence(t *testing.T) {
	// Champs riches : nom + type + dates → scoreAnalysis élevé.
	rich := AnalysisResult{
		NameFound: true, DocType: DocTypeID, DateFound: true,
		DateValid: true, ExpiryDate: "01/01/2030",
	}
	// Champs pauvres : un seul signal.
	poor := AnalysisResult{NameFound: true, DocType: DocTypeUnknown}

	cases := []struct {
		name string
		a    AnalysisResult
		src  confidenceSource
		sig  bool // signature 2D-DOC vérifiée
		want string
	}{
		{"unreadable -> nul", AnalysisResult{Unreadable: true}, srcTesseract, false, confNul},
		{"2D-DOC signé vérifié -> haut", rich, srcTwoDDOC, true, confHaut},
		{"2D-DOC non vérifié -> moyen", rich, srcTwoDDOC, false, confMoyen},
		{"2D-DOC vérifié même pauvre -> haut", poor, srcTwoDDOC, true, confHaut},
		{"Tesseract net -> haut", rich, srcTesseract, false, confHaut},
		{"Tesseract pauvre -> bas", poor, srcTesseract, false, confBas},
		{"LLM riche -> moyen (plafonné)", rich, srcLLM, false, confMoyen},
		{"LLM pauvre -> bas", poor, srcLLM, false, confBas},
		{"segmentation -> bas même riche", rich, srcSegment, false, confBas},
		{"inconnu riche -> haut", rich, srcUnknown, false, confHaut},
	}
	for _, c := range cases {
		a := c.a
		a.source = c.src
		a.SignatureVerified = c.sig
		if got := computeConfidence(a); got != c.want {
			t.Errorf("%s: got %q, want %q (score=%d)", c.name, got, c.want, scoreAnalysis(a))
		}
	}
}
