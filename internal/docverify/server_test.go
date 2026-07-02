package docverify

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
)

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

func TestHealthLiveAlwaysOK(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health/live", nil)
	makeLivenessHandler()(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("/health/live: code = %d, attendu 200", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("décodage réponse: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("/health/live status = %v, attendu \"ok\"", body["status"])
	}
}

func TestHealthDegradedWhenLLMUnreachableButLiveStaysOK(t *testing.T) {
	t.Setenv("LLM_URL", "http://127.0.0.1:1/injoignable")

	q := &Queue{}
	healthRec := httptest.NewRecorder()
	makeHealthHandler(q)(healthRec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if healthRec.Code != http.StatusServiceUnavailable {
		t.Errorf("/health code = %d, attendu 503 (LLM_URL invalide)", healthRec.Code)
	}

	liveRec := httptest.NewRecorder()
	makeLivenessHandler()(liveRec, httptest.NewRequest(http.MethodGet, "/health/live", nil))
	if liveRec.Code != http.StatusOK {
		t.Errorf("/health/live code = %d, attendu 200 même avec LLM down", liveRec.Code)
	}
}

func TestSupportingDocumentRejectsContentExtMismatch(t *testing.T) {
	q := &Queue{}
	handler := makeSupportingDocumentHandler(q)

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, _ := w.CreateFormFile("doc", "fake.pdf")
	// signature PNG dans un fichier déclaré .pdf
	part.Write([]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 0, 0, 0, 0})
	w.WriteField("name", "DUPONT")
	w.WriteField("postal_code", "99000")
	w.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/supporting-documents", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("attendu 400, obtenu %d: %s", rec.Code, rec.Body.String())
	}
}

func TestSupportingDocumentRejectsEmptyPostalCode(t *testing.T) {
	q := &Queue{}
	handler := makeSupportingDocumentHandler(q)

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, _ := w.CreateFormFile("doc", "doc.pdf")
	part.Write([]byte("%PDF-1.4\n%%EOF"))
	w.WriteField("name", "DUPONT")
	w.WriteField("postal_code", "")
	w.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/supporting-documents", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("attendu 400 pour postal_code vide, obtenu %d: %s", rec.Code, rec.Body.String())
	}
}
