package docverify

import "testing"

func TestOcrQuality(t *testing.T) {
	if q := ocrQuality(""); q != 0 {
		t.Errorf("ocrQuality(vide) = %f, attendu 0", q)
	}
	clean := ocrQuality("Bonjour je suis une ligne tout a fait correcte avec des mots")
	if clean <= 0.5 {
		t.Errorf("ocrQuality(texte propre) = %f, attendu > 0.5", clean)
	}
	garbage := ocrQuality("@ # $ % ^ & * ( )")
	if garbage > 0.5 {
		t.Errorf("ocrQuality(charabia) = %f, attendu <= 0.5", garbage)
	}
}
