package docverify

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"strings"
	"testing"
)

// loadTestCert charge le certificat de test (ECDSA P-256) fourni avec les
// samples 2D-DOC issus de zanni/go2ddoc.
func loadTestCert(t *testing.T) *x509.Certificate {
	t.Helper()
	pemBytes, err := os.ReadFile("testdata/ddoc_test_cert.pem")
	if err != nil {
		t.Fatalf("lecture cert de test: %v", err)
	}
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		t.Fatal("PEM cert de test invalide")
	}
	c, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse cert de test: %v", err)
	}
	return c
}

func TestVerifyDDOCSignature(t *testing.T) {
	raw, err := os.ReadFile("testdata/ddoc_v2.txt")
	if err != nil {
		t.Fatalf("lecture sample: %v", err)
	}
	doc, err := Parse2DDOC(strings.TrimRight(string(raw), "\r\n"))
	if err != nil {
		t.Fatalf("parse 2D-DOC: %v", err)
	}
	if !doc.HasSignature {
		t.Fatal("le sample devrait porter une signature")
	}
	// Le cachet de test est émis sous l'autorité FR00, certificat 0001.
	if doc.AC != "FR00" || doc.CertID != "0001" {
		t.Fatalf("AC/CertID inattendus : %q / %q", doc.AC, doc.CertID)
	}
	registerCert(doc.AC, doc.CertID, loadTestCert(t))

	ok, err := VerifyDDOCSignature(doc)
	if err != nil {
		t.Fatalf("vérification: %v", err)
	}
	if !ok {
		t.Fatal("signature valide attendue, obtenu invalide")
	}

	// Falsification : un payload modifié ne doit plus valider.
	tampered := *doc
	tampered.SignedPayload = doc.SignedPayload + "X"
	if ok, _ := VerifyDDOCSignature(&tampered); ok {
		t.Fatal("un payload falsifié ne doit pas valider")
	}
}
