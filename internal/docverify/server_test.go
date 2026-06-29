package docverify

import "testing"

func TestParseValidMonths(t *testing.T) {
	cases := map[string]int{
		"":       defaultValidMonths, // champ absent → défaut
		"abc":    defaultValidMonths, // non numérique → défaut
		"0":      defaultValidMonths, // <= 0 → défaut
		"-4":     defaultValidMonths, // négatif → défaut
		"3":      3,
		"12":     12, // cas client : justificatif de domicile valable 12 mois
	}
	for raw, want := range cases {
		if got := parseValidMonths(raw); got != want {
			t.Errorf("parseValidMonths(%q) = %d, want %d", raw, got, want)
		}
	}
}
