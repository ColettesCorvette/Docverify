package docverify

import "strings"

// validateMRZ extrait et vérifie la MRZ d'un texte OCR.
//
// Supporte deux formats ICAO 9303 :
//   - TD3 : passeport, 2 lignes de 44 caractères, première ligne préfixée 'P'
//   - TD1 : CNI biométrique, 3 lignes de 30 caractères, première ligne préfixée 'I', 'A' ou 'C'
//
// Renvoie true si au moins un format est détecté ET que tous ses chiffres
// de contrôle passent. Quand la MRZ valide, fields contient les champs
// décodés depuis la zone : pays, numéro, dates, nationalité, etc.
//
// La validité MRZ est une garantie forte : les chiffres de contrôle ICAO
// rendent une transcription correcte au caractère près. À utiliser comme
// signal de confiance pour le verdict.
func validateMRZ(text string) (bool, map[string]string) {
	lines := extractMRZLines(text)
	if ok, f := validateTD3(lines); ok {
		return true, f
	}
	if ok, f := validateTD1(lines); ok {
		return true, f
	}
	return false, nil
}

// extractMRZLines filtre les lignes du texte OCR qui ressemblent à du MRZ :
// caractères A-Z, 0-9 ou '<' uniquement, longueur 30 ou 44.
func extractMRZLines(text string) []string {
	var out []string
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		line = strings.ReplaceAll(line, " ", "")
		if !isMRZLine(line) {
			continue
		}
		out = append(out, line)
	}
	return out
}

func isMRZLine(s string) bool {
	if len(s) != 30 && len(s) != 44 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !mrzCharOK(s[i]) {
			return false
		}
	}
	return true
}

func mrzCharOK(c byte) bool {
	return (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '<'
}

// validateTD3 vérifie un passeport ICAO TD3.
//
// Layout ligne 2 :
//
//	[0:9]   numéro de passeport
//	[9]     check digit numéro
//	[10:13] nationalité
//	[13:19] date de naissance YYMMDD
//	[19]    check digit naissance
//	[20]    sexe
//	[21:27] date d'expiration YYMMDD
//	[27]    check digit expiration
//	[28:42] numéro personnel
//	[42]    check digit numéro personnel
//	[43]    check digit composite
func validateTD3(lines []string) (bool, map[string]string) {
	var l1, l2 string
	for i := 0; i+1 < len(lines); i++ {
		if len(lines[i]) == 44 && len(lines[i+1]) == 44 {
			l1, l2 = lines[i], lines[i+1]
			break
		}
	}
	if l1 == "" || !strings.HasPrefix(l1, "P") {
		return false, nil
	}
	if !checkDigit(l2[0:9], l2[9]) {
		return false, nil
	}
	if !checkDigit(l2[13:19], l2[19]) {
		return false, nil
	}
	if !checkDigit(l2[21:27], l2[27]) {
		return false, nil
	}
	if !checkDigit(l2[28:42], l2[42]) {
		return false, nil
	}
	composite := l2[0:10] + l2[13:20] + l2[21:28] + l2[28:43]
	if !checkDigit(composite, l2[43]) {
		return false, nil
	}
	return true, map[string]string{
		"format":       "TD3",
		"country":      l1[2:5],
		"passport_num": strings.TrimRight(l2[0:9], "<"),
		"nationality":  l2[10:13],
		"birth_date":   l2[13:19],
		"sex":          l2[20:21],
		"expiry_date":  l2[21:27],
		"personal_num": strings.TrimRight(l2[28:42], "<"),
	}
}

// validateTD1 vérifie une CNI biométrique ICAO TD1.
//
// Layout :
//
//	l1 [0:2]   type de document, commence par I, A ou C
//	l1 [2:5]   pays émetteur
//	l1 [5:14]  numéro de document
//	l1 [14]    check digit numéro
//	l1 [15:30] optional data 1
//	l2 [0:6]   date de naissance YYMMDD
//	l2 [6]     check digit naissance
//	l2 [7]     sexe
//	l2 [8:14]  date d'expiration YYMMDD
//	l2 [14]    check digit expiration
//	l2 [15:18] nationalité
//	l2 [18:29] optional data 2
//	l2 [29]    check digit composite
//	l3 [0:30]  nom + prénoms
func validateTD1(lines []string) (bool, map[string]string) {
	var l1, l2, l3 string
	for i := 0; i+2 < len(lines); i++ {
		if len(lines[i]) == 30 && len(lines[i+1]) == 30 && len(lines[i+2]) == 30 {
			l1, l2, l3 = lines[i], lines[i+1], lines[i+2]
			break
		}
	}
	if l1 == "" {
		return false, nil
	}
	if c := l1[0]; c != 'I' && c != 'A' && c != 'C' {
		return false, nil
	}
	if !checkDigit(l1[5:14], l1[14]) {
		return false, nil
	}
	if !checkDigit(l2[0:6], l2[6]) {
		return false, nil
	}
	if !checkDigit(l2[8:14], l2[14]) {
		return false, nil
	}
	composite := l1[5:30] + l2[0:7] + l2[8:15] + l2[18:29]
	if !checkDigit(composite, l2[29]) {
		return false, nil
	}
	return true, map[string]string{
		"format":      "TD1",
		"doc_type":    l1[0:2],
		"country":     l1[2:5],
		"doc_num":     strings.TrimRight(l1[5:14], "<"),
		"birth_date":  l2[0:6],
		"sex":         l2[7:8],
		"expiry_date": l2[8:14],
		"nationality": l2[15:18],
		"name_block":  strings.TrimRight(l3, "<"),
	}
}

// checkDigit calcule le chiffre de contrôle ICAO 9303 avec poids 7-3-1
// cycliques et le compare au caractère attendu.
func checkDigit(s string, expected byte) bool {
	weights := [3]int{7, 3, 1}
	sum := 0
	for i := 0; i < len(s); i++ {
		v, ok := mrzCharValue(s[i])
		if !ok {
			return false
		}
		sum += v * weights[i%3]
	}
	e, ok := mrzCharValue(expected)
	if !ok {
		return false
	}
	return sum%10 == e
}

// mrzCharValue traduit un caractère MRZ en sa valeur numérique :
// 0-9 = 0-9, A-Z = 10-35, '<' = 0.
func mrzCharValue(c byte) (int, bool) {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0'), true
	case c >= 'A' && c <= 'Z':
		return int(c-'A') + 10, true
	case c == '<':
		return 0, true
	default:
		return 0, false
	}
}
