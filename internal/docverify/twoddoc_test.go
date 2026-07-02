package docverify

import (
	"strings"
	"testing"
)

// Chaînes 2D-DOC réelles décodées par dmtxread sur les samples (GS = \x1d,
// US = \x1f). Servent de vérité de terrain pour le parsing et l'analyse.
const (
	// justifs.jpg — justificatif EDF au nom de LEROY PIERRE, 17999 VALMONT.
	rawEDF = "DC02FR03EDFD256D256D00018D6ABD86196244E69964C9A7017FF7D7" +
		"\x1d10/LEROY/PIERRE\x1d20\x1d21\x1d226T RUE DES ACACIAS" +
		"\x1d23\x1d2417999\x1d25VALMONT\x1d26FR" +
		"\x1fSSF5TPU35KQILDE375TIFN7TNZNAMQUNHU4PQC2C7EIXCLUE4MEM24YL3IAFOLEHBRZAAYSRHMTTHD3FJHPSWNXPSY5YE7ACBHIWCNQ"

	// justif2.pdf — facture au nom de MME SOPHIE BERTRAND, 99000.
	rawBERTRAND = "DC02FR06BYT0257425740126FR299000010MME/SOPHIE/BERTRAND" +
		"\x1d223 RUE DES IRIS\x1d1810000000000000\x1d02FACTURE FNB\x1d03GP" +
		"\x1d1D10,00\x1d19100000000\x1d07100000" +
		"\x1f2XRJDMBGSYHP4MLU27NH5Y2SSJAQJYIMUEJQ2F3QLRFV7YSJGNBP7NUBAUNT72ZUMH5SX4PMLPOKLFWJ2UWKL3JJ7DZ673EPIJS7VFY"
)

func TestDecodeDdocDate(t *testing.T) {
	cases := map[string]string{
		"256D": "26/03/2026",
		"2574": "02/04/2026",
	}
	for hex, want := range cases {
		got, ok := decodeDdocDate(hex)
		if !ok {
			t.Fatalf("decodeDdocDate(%q) a échoué", hex)
		}
		if g := got.Format("02/01/2006"); g != want {
			t.Errorf("decodeDdocDate(%q) = %s, attendu %s", hex, g, want)
		}
	}
	if _, ok := decodeDdocDate("ZZZZ"); ok {
		t.Errorf("decodeDdocDate(\"ZZZZ\") aurait dû échouer")
	}
}

func TestParse2DDOC_EDF(t *testing.T) {
	doc, err := Parse2DDOC(rawEDF)
	if err != nil {
		t.Fatalf("Parse2DDOC: %v", err)
	}
	if doc.Version != "02" {
		t.Errorf("Version = %q, attendu \"02\"", doc.Version)
	}
	if doc.AC != "FR03" {
		t.Errorf("AC = %q, attendu \"FR03\"", doc.AC)
	}
	if doc.CertID != "EDFD" {
		t.Errorf("CertID = %q, attendu \"EDFD\"", doc.CertID)
	}
	if !doc.HasSignature {
		t.Errorf("HasSignature = false, attendu true")
	}
	if got := doc.EmitDate.Format("02/01/2006"); got != "26/03/2026" {
		t.Errorf("EmitDate = %s, attendu 26/03/2026", got)
	}
	// La signature ne doit pas fuiter dans la zone de données.
	if want := "SSF5TPU"; strings.Contains(doc.DataZone, want) {
		t.Errorf("DataZone contient la signature (%q)", want)
	}
}

func TestParse2DDOC_VersionNonGeree(t *testing.T) {
	if _, err := Parse2DDOC("DC99FR03EDFD256D256D00xx"); err == nil {
		t.Errorf("une version non gérée aurait dû renvoyer une erreur")
	}
	if _, err := Parse2DDOC("XX02..."); err == nil {
		t.Errorf("un marqueur absent aurait dû renvoyer une erreur")
	}
}

func TestAnalyzeFrom2DDOC_EDF(t *testing.T) {
	doc, err := Parse2DDOC(rawEDF)
	if err != nil {
		t.Fatalf("Parse2DDOC: %v", err)
	}
	a := AnalyzeFrom2DDOC(doc, Input{Name: "LEROY", Surname: "PIERRE", PostalCodes: []string{"17999"}})

	if !a.NameFound {
		t.Errorf("NameFound = false, attendu true")
	}
	if !a.SurnameFound {
		t.Errorf("SurnameFound = false, attendu true")
	}
	if !a.PostalFound || a.PostalMatch != "17999" {
		t.Errorf("PostalMatch = %q (found=%v), attendu 17999", a.PostalMatch, a.PostalFound)
	}
	if a.DocType != DocTypeInvoice {
		t.Errorf("DocType = %q, attendu facture", a.DocType)
	}
	if !a.DateFound || a.DocDate != "26/03/2026" {
		t.Errorf("DocDate = %q (found=%v), attendu 26/03/2026", a.DocDate, a.DateFound)
	}
}

func TestAnalyzeFrom2DDOC_FallbackCPComposite(t *testing.T) {
	// rawBERTRAND concatène AC+CertID+CP+n°voie sans séparateur dans son
	// premier champ ("FR299000010..."), donnant en texte synthétique
	// "26 FR299000010MME/SOPHIE/BERTRAND" : "99000" y apparaît bien en
	// sous-chaîne, mais entouré de chiffres (...99000010...), donc sans
	// frontière de mot — wordBoundaryContains échoue seul sur ce champ.
	// Ce test verrouille explicitement que le fallback en sous-chaîne de
	// AnalyzeFrom2DDOC prend le relais et pose PostalFound/PostalMatch.
	doc, err := Parse2DDOC(rawBERTRAND)
	if err != nil {
		t.Fatalf("Parse2DDOC: %v", err)
	}
	if wordBoundaryContains(doc.syntheticText(), "99000") {
		t.Fatalf("précondition invalide : wordBoundaryContains trouve déjà 99000 sans le fallback")
	}
	a := AnalyzeFrom2DDOC(doc, Input{Name: "BERTRAND", PostalCodes: []string{"99000"}})

	if !a.PostalFound || a.PostalMatch != "99000" {
		t.Errorf("PostalMatch = %q (found=%v), attendu 99000 via le fallback sous-chaîne", a.PostalMatch, a.PostalFound)
	}
}

func TestAnalyzeFrom2DDOC_FallbackNeCourtCircuitePasLaPasseStricte(t *testing.T) {
	// rawEDF a une frontière de mot propre autour du CP ("24" + "17999"
	// -> "24 17999" en texte synthétique) : la passe stricte AnalyzeText
	// (wordBoundaryContains) doit trouver 17999 sans recours au fallback.
	// On fournit aussi un faux code postal qui n'apparaît nulle part dans
	// le payload, pour s'assurer que c'est bien le CP valide qui gagne et
	// non un artefact du fallback.
	doc, err := Parse2DDOC(rawEDF)
	if err != nil {
		t.Fatalf("Parse2DDOC: %v", err)
	}
	if !wordBoundaryContains(doc.syntheticText(), "17999") {
		t.Fatalf("précondition invalide : wordBoundaryContains ne trouve pas 17999 sans le fallback")
	}
	a := AnalyzeFrom2DDOC(doc, Input{Name: "LEROY", Surname: "PIERRE", PostalCodes: []string{"99999", "17999"}})

	if !a.PostalFound || a.PostalMatch != "17999" {
		t.Errorf("PostalMatch = %q (found=%v), attendu 17999 via la passe stricte", a.PostalMatch, a.PostalFound)
	}
}

func TestAnalyzeFrom2DDOC_NomAbsent(t *testing.T) {
	// Le 2D-DOC est au nom de BERTRAND : un input ORBINEA ne doit pas matcher,
	// et le verdict doit être négatif quelle que soit la date.
	doc, err := Parse2DDOC(rawBERTRAND)
	if err != nil {
		t.Fatalf("Parse2DDOC: %v", err)
	}
	a := AnalyzeFrom2DDOC(doc, Input{Name: "ORBINEA", PostalCodes: []string{"99000"}})

	if a.NameFound {
		t.Errorf("NameFound = true, attendu false (titulaire BERTRAND)")
	}
	if !a.PostalFound || a.PostalMatch != "99000" {
		t.Errorf("PostalMatch = %q (found=%v), attendu 99000", a.PostalMatch, a.PostalFound)
	}
	if a.DocType != DocTypeInvoice {
		t.Errorf("DocType = %q, attendu facture", a.DocType)
	}
	if a.OK {
		t.Errorf("OK = true, attendu false (nom non trouvé)")
	}
}
