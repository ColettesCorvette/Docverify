package docverify

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
)

func TestRegistryTokenAuth(t *testing.T) {
	// Créer des fichiers temporaires pour le template et la sortie Caddyfile
	templateFile, err := os.CreateTemp("", "Caddyfile.template-*")
	if err != nil {
		t.Fatalf("Échec création template temporaire : %v", err)
	}
	defer os.Remove(templateFile.Name())
	
	_, err = templateFile.WriteString("test template\n{upstreams}\n")
	if err != nil {
		t.Fatalf("Échec écriture template : %v", err)
	}
	templateFile.Close()

	outputFile, err := os.CreateTemp("", "Caddyfile-out-*")
	if err != nil {
		t.Fatalf("Échec création output temporaire : %v", err)
	}
	defer os.Remove(outputFile.Name())
	outputFile.Close()

	// 1. Tester sans token d'authentification requis
	t.Setenv("DOCVERIFY_REGISTRY_TOKEN", "")
	handler := handleRegister(templateFile.Name(), outputFile.Name())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/register?ip=10.30.0.99", nil)
	handler.ServeHTTP(rec, req)

	// Sans token requis, cela devrait passer (ou échouer sur la commande "caddy reload" si caddy n'est pas installé/accessible, 
	// mais la validation de token elle-même a réussi).
	// Pour éviter de bloquer sur l'absence de Caddy lors des tests unitaires standard,
	// nous pouvons juste vérifier que le code de retour n'est pas 401.
	if rec.Code == http.StatusUnauthorized {
		t.Errorf("Attendu que la requête passe sans token, mais a reçu 401")
	}

	// 2. Tester avec token d'authentification requis
	t.Setenv("DOCVERIFY_REGISTRY_TOKEN", "secret-token-123")
	handlerWithToken := handleRegister(templateFile.Name(), outputFile.Name())

	// Cas A : Pas de token fourni -> 401
	recA := httptest.NewRecorder()
	reqA := httptest.NewRequest(http.MethodPost, "/register?ip=10.30.0.99", nil)
	handlerWithToken.ServeHTTP(recA, reqA)

	if recA.Code != http.StatusUnauthorized {
		t.Errorf("Attendu HTTP 401 (Non autorisé) en l'absence de token, reçu %d", recA.Code)
	}

	// Cas B : Token invalide -> 401
	recB := httptest.NewRecorder()
	reqB := httptest.NewRequest(http.MethodPost, "/register?ip=10.30.0.99&token=wrong-token", nil)
	handlerWithToken.ServeHTTP(recB, reqB)

	if recB.Code != http.StatusUnauthorized {
		t.Errorf("Attendu HTTP 401 (Non autorisé) avec un token invalide, reçu %d", recB.Code)
	}

	// Cas C : Token valide -> ne doit pas être 401
	recC := httptest.NewRecorder()
	reqC := httptest.NewRequest(http.MethodPost, "/register?ip=10.30.0.99&token="+url.QueryEscape("secret-token-123"), nil)
	handlerWithToken.ServeHTTP(recC, reqC)

	if recC.Code == http.StatusUnauthorized {
		t.Errorf("Attendu que la requête soit autorisée avec le bon token, mais reçu 401")
	}
}
