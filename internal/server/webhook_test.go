package server_test

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"

	"pilo/internal/deploy"
	"pilo/internal/server"
	"pilo/internal/store"
)

func TestGitHubWebhook(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_webhook.db")

	st, err := store.New(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer st.Close()

	if err := st.SetWebhookSecret("mysecret"); err != nil {
		t.Fatalf("failed to set webhook secret: %v", err)
	}

	dep := deploy.New(filepath.Join(tmpDir, "repos"))
	router := server.NewRouter(nil)
	handlers := server.NewHandlers(st, dep, router, "http://localhost:8080")
	router.SetAdmin(handlers.Mux())

	payload := []byte(`{
		"ref": "refs/heads/main",
		"repository": {
			"name": "my-app",
			"owner": {
				"login": "octocat"
			}
		}
	}`)

	mac := hmac.New(sha256.New, []byte("mysecret"))
	mac.Write(payload)
	validSig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	// Test 1: Invalid signature should return 401
	req1 := httptest.NewRequest("POST", "/webhooks/github", bytes.NewReader(payload))
	req1.Header.Set("X-Hub-Signature-256", "sha256=invalid")
	rec1 := httptest.NewRecorder()
	router.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 Unauthorized for invalid signature, got %d", rec1.Code)
	}

	// Test 2: Valid signature should return 200 OK
	req2 := httptest.NewRequest("POST", "/webhooks/github", bytes.NewReader(payload))
	req2.Header.Set("X-Hub-Signature-256", validSig)
	rec2 := httptest.NewRecorder()
	router.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Errorf("expected 200 OK for valid signature, got %d", rec2.Code)
	}

	// GitHub sends form-encoded payloads when the hook content type is "form".
	formBody := []byte(url.Values{"payload": {string(payload)}}.Encode())
	formMAC := hmac.New(sha256.New, []byte("mysecret"))
	formMAC.Write(formBody)
	formSig := "sha256=" + hex.EncodeToString(formMAC.Sum(nil))
	req3 := httptest.NewRequest("POST", "/webhooks/github", bytes.NewReader(formBody))
	req3.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req3.Header.Set("X-Hub-Signature-256", formSig)
	rec3 := httptest.NewRecorder()
	router.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusOK {
		t.Errorf("expected 200 OK for form-encoded webhook, got %d: %s", rec3.Code, rec3.Body.String())
	}
}
