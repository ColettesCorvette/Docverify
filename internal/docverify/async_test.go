package docverify

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
	"time"
)

var uuidV4Re = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestNewRequestID(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		id := newRequestID()
		if !uuidV4Re.MatchString(id) {
			t.Fatalf("ID non conforme UUID v4 : %q", id)
		}
		if seen[id] {
			t.Fatalf("collision d'ID : %q", id)
		}
		seen[id] = true
	}
}

func TestValidCallbackURL(t *testing.T) {
	valid := []string{
		"http://regia.local/cb",
		"https://example.com/hook?x=1",
		"http://10.30.0.5:8080/callback",
	}
	invalid := []string{
		"",
		"ftp://x/y",
		"https://",           // hôte absent
		"/relative/path",     // schéma absent
		"example.com/no-sch", // schéma absent
	}
	for _, u := range valid {
		if !validCallbackURL(u) {
			t.Errorf("devrait être valide : %q", u)
		}
	}
	for _, u := range invalid {
		if validCallbackURL(u) {
			t.Errorf("devrait être invalide : %q", u)
		}
	}
}

func TestCallbackAllowlist(t *testing.T) {
	t.Setenv("DOCVERIFY_CALLBACK_ALLOWED_HOSTS", "regia.int.orbinea.example, regia.local")
	if !validCallbackURL("https://regia.local/cb") {
		t.Error("regia.local devrait passer la liste blanche")
	}
	if !validCallbackURL("https://regia.int.orbinea.example/hook") {
		t.Error("regia.int.orbinea.example devrait passer la liste blanche")
	}
	if validCallbackURL("https://evil.example.com/cb") {
		t.Error("evil.example.com ne devrait pas passer la liste blanche")
	}
}

func TestBlockedCallbackIP(t *testing.T) {
	blocked := []string{"127.0.0.1", "::1", "169.254.169.254", "0.0.0.0", "224.0.0.1", "fe80::1"}
	allowed := []string{"10.30.0.5", "192.168.40.10", "172.16.0.1", "8.8.8.8"}
	for _, s := range blocked {
		if !isBlockedCallbackIP(net.ParseIP(s)) {
			t.Errorf("%s devrait être bloquée", s)
		}
	}
	for _, s := range allowed {
		if isBlockedCallbackIP(net.ParseIP(s)) {
			t.Errorf("%s devrait être autorisée", s)
		}
	}
}

func TestPostCallback(t *testing.T) {
	// Le client durci refuse le loopback : on le remplace par un client neutre
	// le temps de tester la mécanique de livraison vers le serveur httptest local.
	old := callbackClient
	callbackClient = &http.Client{Timeout: 5 * time.Second}
	defer func() { callbackClient = old }()

	received := make(chan asyncCallback, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var cb asyncCallback
		json.NewDecoder(r.Body).Decode(&cb)
		received <- cb
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	postCallback(srv.URL, asyncCallback{
		JobID:  "test-123",
		Status: "done",
		Result: &AnalysisResult{OK: true, DocType: DocTypeInvoice},
	})

	select {
	case got := <-received:
		if got.JobID != "test-123" || got.Status != "done" {
			t.Errorf("entête callback inattendu : %+v", got)
		}
		if got.Result == nil || !got.Result.OK || got.Result.DocType != DocTypeInvoice {
			t.Errorf("résultat callback inattendu : %+v", got.Result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("callback non reçu dans le délai")
	}
}
