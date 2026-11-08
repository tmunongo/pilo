package server_test

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"pilo/internal/store"
)

func TestHealthEndpoint(t *testing.T) {
	_, router, _ := setupTestServer(t)

	req := httptest.NewRequest("GET", "/health", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Errorf("expected 200 'ok' from /health, got %d %q", rec.Code, rec.Body.String())
	}
}

func TestForgotPasswordForm(t *testing.T) {
	_, router, _ := setupTestServer(t)

	req := httptest.NewRequest("GET", "/forgot-password", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK from GET /forgot-password, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `action="/forgot-password"`) {
		t.Errorf("expected form with action /forgot-password")
	}
}

func TestProjectCreateValidation(t *testing.T) {
	st, router, _ := setupTestServer(t)

	u, _ := st.CreateUser("creator@example.com", "password123")
	sess, _ := st.CreateSession(u.ID, 1*time.Hour)
	cookie := &http.Cookie{Name: "pilo_session", Value: sess.ID}

	// 1. Invalid repo format
	form := url.Values{
		"repo":   {"invalid-repo-without-slash-or-url"},
		"slug":   {"my-slug"},
		"branch": {"main"},
	}
	req := httptest.NewRequest("POST", "/projects", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if !strings.Contains(rec.Body.String(), "owner/") {
		t.Errorf("expected repo format error for invalid repo format, got: %s", rec.Body.String())
	}

	// 2. Reserved slug (e.g. "projects", "settings", "login")
	formReserved := url.Values{
		"repo":   {"octocat/hello-world"},
		"slug":   {"settings"},
		"branch": {"main"},
	}
	req = httptest.NewRequest("POST", "/projects", strings.NewReader(formReserved.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if !strings.Contains(rec.Body.String(), "is reserved") {
		t.Errorf("expected reserved slug error, got: %s", rec.Body.String())
	}

	// 3. Invalid slug characters (e.g. uppercase, symbols)
	formInvalidSlug := url.Values{
		"repo":   {"octocat/hello-world"},
		"slug":   {"INVALID_SLUG!!!"},
		"branch": {"main"},
	}
	req = httptest.NewRequest("POST", "/projects", strings.NewReader(formInvalidSlug.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if !strings.Contains(rec.Body.String(), "lowercase letters, numbers, and hyphens") {
		t.Errorf("expected invalid slug character error, got: %s", rec.Body.String())
	}
}

func TestProjectRedeployHTMX(t *testing.T) {
	st, router, _ := setupTestServer(t)

	u, _ := st.CreateUser("htmx_user@example.com", "password123")
	sess, _ := st.CreateSession(u.ID, 1*time.Hour)
	cookie := &http.Cookie{Name: "pilo_session", Value: sess.ID}

	p := &store.Project{
		ID:        "htmx-app",
		Slug:      "htmx-app",
		UserID:    u.ID,
		RepoOwner: "user",
		RepoName:  "htmx-app",
		Status:    store.StatusLive,
	}
	_ = st.Put(p)

	// POST /projects/htmx-app/redeploy with HX-Request: true
	req := httptest.NewRequest("POST", "/projects/htmx-app/redeploy", nil)
	req.Header.Set("HX-Request", "true")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for HTMX redeploy, got %d", rec.Code)
	}
	if ref := rec.Header().Get("HX-Refresh"); ref != "true" {
		t.Errorf("expected HX-Refresh: true header, got %s", ref)
	}
}

func TestWebhookEdgeCases(t *testing.T) {
	st, router, _ := setupTestServer(t)

	u, _ := st.CreateUser("hookuser@example.com", "password123")
	_ = st.SetUserSetting(u.ID, "webhook_secret", "hooksecret123")

	p := &store.Project{
		ID:        "webhook-app",
		Slug:      "webhook-app",
		UserID:    u.ID,
		RepoOwner: "octocat",
		RepoName:  "webhook-app",
		Branch:    "main",
		Status:    store.StatusLive,
	}
	_ = st.Put(p)

	// 1. Missing repository details in payload -> ignored 200 OK
	badPayload := []byte(`{"ref": "refs/heads/main"}`)
	req := httptest.NewRequest("POST", "/webhooks/github", bytes.NewReader(badPayload))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "ignored") {
		t.Errorf("expected ignored 200 OK for incomplete payload, got %d %q", rec.Code, rec.Body.String())
	}

	// 2. Webhook for matching repo with valid per-user secret
	goodPayload := []byte(`{
		"ref": "refs/heads/main",
		"repository": {
			"name": "webhook-app",
			"owner": {
				"login": "octocat"
			}
		}
	}`)
	mac := hmac.New(sha256.New, []byte("hooksecret123"))
	mac.Write(goodPayload)
	goodSig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	reqGood := httptest.NewRequest("POST", "/webhooks/github", bytes.NewReader(goodPayload))
	reqGood.Header.Set("X-Hub-Signature-256", goodSig)
	recGood := httptest.NewRecorder()
	router.ServeHTTP(recGood, reqGood)

	if recGood.Code != http.StatusOK {
		t.Errorf("expected 200 OK for valid webhook push, got %d", recGood.Code)
	}
}
