package docverify

// Vérification de la signature cryptographique du 2D-DOC. Le cachet porte une
// signature (ECDSA le plus souvent, RSA pour d'anciennes autorités) sur l'entête
// et la zone de données. La vérifier prouve l'authenticité anti-fraude et
// justifie le niveau de confiance le plus haut.
//
// Logique de signature adaptée de zanni/go2ddoc (licence MIT) : payload =
// entête + données avant le séparateur US, signature en base32 sans padding,
// ECDSA au format brut (r||s) ou RSA PKCS#1 v1.5, hash selon la courbe.
//
// Les certificats viennent de la TSL ANTS embarquée (liste signée des autorités
// 2D-DOC). Résolution offline : (autorité, identifiant) dérivés du sujet du
// certificat (CommonName + unité organisationnelle), convention ANTS.

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"embed"
	"encoding/base32"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"hash"
	"math/big"
	"strings"
	"sync"
)

//go:embed assets/tsl_signed.xml
var tslXML []byte

// leavesFS : certificats feuilles publiés par chaque autorité (un bundle DER par
// AC, nommé FRxx.der), pré-fetchés au build par scripts/fetch-2ddoc-leaves.sh.
// Les vrais cachets sont signés par ces feuilles (ECDSA P-256), dont le
// CommonName est l'identifiant de certificat de l'entête.
//
//go:embed assets/leaves
var leavesFS embed.FS

var (
	certStoreOnce sync.Once
	certStoreMu   sync.RWMutex
	certStore     = map[string]map[string]*x509.Certificate{}
)

// ensureCertStore charge une fois les certificats : feuilles embarquées (clé
// (AC, CommonName)) puis certs d'AC de la TSL (repli). Tout est offline.
func ensureCertStore() {
	certStoreOnce.Do(func() {
		// Feuilles : un bundle par autorité, l'AC vient du nom de fichier.
		if entries, err := leavesFS.ReadDir("assets/leaves"); err == nil {
			for _, e := range entries {
				if e.IsDir() {
					continue
				}
				ac := strings.TrimSuffix(e.Name(), ".der")
				data, err := leavesFS.ReadFile("assets/leaves/" + e.Name())
				if err != nil {
					continue
				}
				for _, c := range scanDERCerts(data) {
					if cn := strings.TrimSpace(c.Subject.CommonName); cn != "" {
						registerCert(ac, cn, c)
					}
				}
			}
		}
		// Certs d'AC de la TSL, en repli.
		for _, der := range extractTSLCerts(tslXML) {
			c, err := x509.ParseCertificate(der)
			if err != nil {
				continue
			}
			if ac, id := certIdentifiers(c); ac != "" && id != "" {
				registerCert(ac, id, c)
			}
		}
	})
}

// scanDERCerts extrait tous les certificats X509 d'un blob : bundles de DER
// concaténés ou PKCS#7, en repérant les séquences ASN.1 (0x30 0x82 longueur).
func scanDERCerts(data []byte) []*x509.Certificate {
	var out []*x509.Certificate
	for i := 0; i+4 < len(data); {
		if data[i] != 0x30 || data[i+1] != 0x82 {
			i++
			continue
		}
		n := (int(data[i+2])<<8 | int(data[i+3])) + 4
		if i+n > len(data) {
			i++
			continue
		}
		if c, err := x509.ParseCertificate(data[i : i+n]); err == nil {
			out = append(out, c)
			i += n
			continue
		}
		i++
	}
	return out
}

// extractTSLCerts récupère tous les certificats X509 (DER) encodés en base64
// dans la TSL, où qu'ils soient dans l'arbre ETSI.
func extractTSLCerts(data []byte) [][]byte {
	var out [][]byte
	dec := xml.NewDecoder(strings.NewReader(string(data)))
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "X509Certificate" {
			continue
		}
		var b64 string
		if dec.DecodeElement(&b64, &se) != nil {
			continue
		}
		der, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(b64), ""))
		if err == nil {
			out = append(out, der)
		}
	}
	return out
}

// certIdentifiers dérive (autorité, identifiant de certificat) du sujet d'un
// certificat 2D-DOC : autorité = CommonName, identifiant = 4 premiers caractères
// de la dernière unité organisationnelle (convention ANTS).
func certIdentifiers(c *x509.Certificate) (ac, id string) {
	ac = strings.TrimSpace(c.Subject.CommonName)
	if ous := c.Subject.OrganizationalUnit; len(ous) > 0 {
		if r := []rune(strings.TrimSpace(ous[len(ous)-1])); len(r) >= 4 {
			id = string(r[:4])
		}
	}
	return ac, id
}

// registerCert ajoute un certificat au magasin sous (autorité, identifiant).
// Utilisé au chargement de la TSL et par les tests.
func registerCert(ac, id string, c *x509.Certificate) {
	certStoreMu.Lock()
	defer certStoreMu.Unlock()
	if certStore[ac] == nil {
		certStore[ac] = map[string]*x509.Certificate{}
	}
	certStore[ac][id] = c
}

// resolveCert retrouve le certificat d'une (autorité, identifiant). Repli sur
// l'unique certificat de l'autorité si l'identifiant exact est absent.
func resolveCert(ac, id string) (*x509.Certificate, error) {
	ensureCertStore()
	certStoreMu.RLock()
	defer certStoreMu.RUnlock()
	m, ok := certStore[ac]
	if !ok {
		return nil, errors.New("autorité 2D-DOC inconnue : " + ac)
	}
	if c, ok := m[id]; ok {
		return c, nil
	}
	if len(m) == 1 {
		for _, c := range m {
			return c, nil
		}
	}
	return nil, errors.New("certificat 2D-DOC introuvable : " + ac + "/" + id)
}

// VerifyDDOCSignature vérifie la signature d'un 2D-DOC parsé. Retourne false sans
// erreur si la signature ne valide pas, et une erreur si on ne peut pas vérifier
// (certificat introuvable, signature mal formée, type de clé non géré).
func VerifyDDOCSignature(d *TwoDDoc) (bool, error) {
	if !d.HasSignature || d.Signature == "" || d.SignedPayload == "" {
		return false, errors.New("signature absente")
	}
	c, err := resolveCert(d.AC, d.CertID)
	if err != nil {
		return false, err
	}
	sig, err := base32.StdEncoding.WithPadding(base32.NoPadding).
		DecodeString(strings.ToUpper(strings.TrimSpace(d.Signature)))
	if err != nil {
		return false, err
	}

	switch pub := c.PublicKey.(type) {
	case *ecdsa.PublicKey:
		if len(sig)%2 != 0 {
			return false, errors.New("signature ECDSA (r||s) invalide")
		}
		h := hashForCurve(pub.Curve)
		h.Write([]byte(d.SignedPayload))
		half := len(sig) / 2
		r := new(big.Int).SetBytes(sig[:half])
		s := new(big.Int).SetBytes(sig[half:])
		return ecdsa.Verify(pub, h.Sum(nil), r, s), nil
	case *rsa.PublicKey:
		h := sha256.New()
		h.Write([]byte(d.SignedPayload))
		return rsa.VerifyPKCS1v15(pub, crypto.SHA256, h.Sum(nil), sig) == nil, nil
	default:
		return false, errors.New("type de clé 2D-DOC non géré")
	}
}

// hashForCurve donne la fonction de hachage associée à la courbe, selon la spec
// 2D-DOC : P-256 → SHA-256, P-384 → SHA-384, P-521 → SHA-512.
func hashForCurve(c elliptic.Curve) hash.Hash {
	switch c {
	case elliptic.P384():
		return sha512.New384()
	case elliptic.P521():
		return sha512.New()
	default:
		return sha256.New()
	}
}
