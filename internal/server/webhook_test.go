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
	"strings"
	"testing"
	"time"

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

func TestWebhookLoggingAndDisplay(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_webhook_display.db")

	st, err := store.New(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer st.Close()

	u, err := st.CreateUser("webtest@example.com", "password123")
	if err != nil {
		t.Fatalf("failed to create user: %v", err)
	}
	sess, _ := st.CreateSession(u.ID, 1*time.Hour)
	cookie := &http.Cookie{Name: "pilo_session", Value: sess.ID}

	_ = st.SetUserSetting(u.ID, "webhook_secret", "hooksecret123")

	p := &store.Project{
		ID:        "my-log-app",
		Slug:      "my-log-app",
		UserID:    u.ID,
		RepoOwner: "octocat",
		RepoName:  "my-log-app",
		Branch:    "main",
		Status:    store.StatusLive,
	}
	if err := st.Put(p); err != nil {
		t.Fatalf("failed to save project: %v", err)
	}

	dep := deploy.New(filepath.Join(tmpDir, "repos"))
	router := server.NewRouter(nil)
	handlers := server.NewHandlers(st, dep, router, "http://localhost:8080")
	router.SetAdmin(handlers.Mux())

	// 1. Send GitHub ping event
	pingPayload := []byte(`{
		"zen": "Non-blocking is better than blocking.",
		"hook_id": 123456,
		"repository": {
			"name": "my-log-app",
			"owner": {
				"login": "octocat"
			}
		}
	}`)
	mac := hmac.New(sha256.New, []byte("hooksecret123"))
	mac.Write(pingPayload)
	pingSig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	reqPing := httptest.NewRequest("POST", "/webhooks/github", bytes.NewReader(pingPayload))
	reqPing.Header.Set("X-GitHub-Event", "ping")
	reqPing.Header.Set("X-GitHub-Delivery", "delivery-ping-1")
	reqPing.Header.Set("X-Hub-Signature-256", pingSig)
	recPing := httptest.NewRecorder()
	router.ServeHTTP(recPing, reqPing)

	if recPing.Code != http.StatusOK {
		t.Errorf("expected 200 OK for ping event, got %d", recPing.Code)
	}

	// 2. Send push event
	pushPayload := []byte(`{
		"ref": "refs/heads/main",
		"after": "9876543210abcdef",
		"head_commit": {
			"id": "9876543210abcdef",
			"message": "fix: update landing page typography"
		},
		"repository": {
			"name": "my-log-app",
			"owner": {
				"login": "octocat"
			}
		}
	}`)
	macPush := hmac.New(sha256.New, []byte("hooksecret123"))
	macPush.Write(pushPayload)
	pushSig := "sha256=" + hex.EncodeToString(macPush.Sum(nil))

	reqPush := httptest.NewRequest("POST", "/webhooks/github", bytes.NewReader(pushPayload))
	reqPush.Header.Set("X-GitHub-Event", "push")
	reqPush.Header.Set("X-GitHub-Delivery", "delivery-push-2")
	reqPush.Header.Set("X-Hub-Signature-256", pushSig)
	recPush := httptest.NewRecorder()
	router.ServeHTTP(recPush, reqPush)

	if recPush.Code != http.StatusOK {
		t.Errorf("expected 200 OK for push event, got %d", recPush.Code)
	}

	// Verify webhook logs in database
	logs, err := st.WebhookLogsForProject(p.Slug, 10)
	if err != nil {
		t.Fatalf("failed to query webhook logs: %v", err)
	}
	if len(logs) != 2 {
		t.Fatalf("expected 2 webhook logs for project, got %d", len(logs))
	}
	// logs[0] is the most recent (push), logs[1] is ping
	if logs[0].Event != "push" || logs[1].Event != "ping" {
		t.Errorf("expected [push, ping], got [%s, %s]", logs[0].Event, logs[1].Event)
	}

	// 3. GET /projects/my-log-app should display webhook logs section and table
	reqDetail := httptest.NewRequest("GET", "/projects/my-log-app", nil)
	reqDetail.AddCookie(cookie)
	recDetail := httptest.NewRecorder()
	router.ServeHTTP(recDetail, reqDetail)

	if recDetail.Code != http.StatusOK {
		t.Fatalf("expected 200 OK from detail page, got %d", recDetail.Code)
	}
	detailHTML := recDetail.Body.String()
	if !strings.Contains(detailHTML, "webhook logs") {
		t.Errorf("expected 'webhook logs' section heading in project detail page")
	}
	if !strings.Contains(detailHTML, "webhook-logs-table") {
		t.Errorf("expected 'webhook-logs-table' in project detail page")
	}
	if !strings.Contains(detailHTML, "ping") || !strings.Contains(detailHTML, "push") {
		t.Errorf("expected ping and push events listed in webhook logs table")
	}

	// 4. GET / (index) should display LastWebhookEvent
	reqIndex := httptest.NewRequest("GET", "/", nil)
	reqIndex.AddCookie(cookie)
	recIndex := httptest.NewRecorder()
	router.ServeHTTP(recIndex, reqIndex)

	if recIndex.Code != http.StatusOK {
		t.Fatalf("expected 200 OK from index page, got %d", recIndex.Code)
	}
	indexHTML := recIndex.Body.String()
	if !strings.Contains(indexHTML, "last event:") {
		t.Errorf("expected 'last event:' on index page under webhook box, got: %s", indexHTML)
	}
	if !strings.Contains(indexHTML, "my-log-app") {
		t.Errorf("expected project slug in last event text")
	}
}
