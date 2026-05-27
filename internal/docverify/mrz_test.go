package docverify

import "testing"

// Spécimen ICAO 9303 — passeport TD3 publié officiellement. Tous les chiffres
// de contrôle sont calculés correctement, validateMRZ doit retourner true.
const td3Specimen = `Some preamble text
P<UTOERIKSSON<<ANNA<MARIA<<<<<<<<<<<<<<<<<<<
L898902C36UTO7408122F1204159ZE184226B<<<<<10
Some trailing text`

func TestValidateMRZ_TD3_Specimen(t *testing.T) {
	ok, fields := validateMRZ(td3Specimen)
	if !ok {
		t.Fatalf("validateMRZ devrait accepter le spécimen ICAO TD3, got false (fields=%v)", fields)
	}
	wantField := func(key, want string) {
		t.Helper()
		if got := fields[key]; got != want {
			t.Errorf("fields[%q] = %q, want %q", key, got, want)
		}
	}
	wantField("format", "TD3")
	wantField("country", "UTO")
	wantField("passport_num", "L898902C3")
	wantField("nationality", "UTO")
	wantField("birth_date", "740812")
	wantField("expiry_date", "120415")
	wantField("sex", "F")
}

func TestValidateMRZ_NoMRZ(t *testing.T) {
	ok, _ := validateMRZ("Ceci est juste du texte sans MRZ.\nAvec deux lignes.")
	if ok {
		t.Error("validateMRZ devrait retourner false sur un texte sans MRZ")
	}
}

func TestValidateMRZ_CorruptedChecksum(t *testing.T) {
	// On change le check digit du numéro de passeport ('6' devient '7').
	corrupted := `P<UTOERIKSSON<<ANNA<MARIA<<<<<<<<<<<<<<<<<<<
L898902C37UTO7408122F1204159ZE184226B<<<<<10`
	ok, _ := validateMRZ(corrupted)
	if ok {
		t.Error("validateMRZ devrait rejeter une MRZ avec un check digit modifié")
	}
}

func TestCheckDigit_KnownValues(t *testing.T) {
	cases := []struct {
		input    string
		expected byte
		want     bool
	}{
		{"L898902C3", '6', true},
		{"740812", '2', true},
		{"120415", '9', true},
		{"L898902C3", '7', false},
	}
	for _, c := range cases {
		if got := checkDigit(c.input, c.expected); got != c.want {
			t.Errorf("checkDigit(%q, %q) = %v, want %v", c.input, c.expected, got, c.want)
		}
	}
}
