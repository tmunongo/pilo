package server_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pilo/internal/store"
)

func TestCrossTenantProjectIsolation(t *testing.T) {
	st, router, _ := setupTestServer(t)

	u1, _ := st.CreateUser("tenant1@example.com", "password123")
	s1, _ := st.CreateSession(u1.ID, 1*time.Hour)
	cookie1 := &http.Cookie{Name: "pilo_session", Value: s1.ID}

	u2, _ := st.CreateUser("tenant2@example.com", "password123")
	s2, _ := st.CreateSession(u2.ID, 1*time.Hour)
	cookie2 := &http.Cookie{Name: "pilo_session", Value: s2.ID}

	p1 := &store.Project{
		ID:        "app-one",
		Slug:      "app-one",
		UserID:    u1.ID,
		RepoOwner: "tenant1",
		RepoName:  "app-one",
		Status:    store.StatusLive,
	}
	p2 := &store.Project{
		ID:        "app-two",
		Slug:      "app-two",
		UserID:    u2.ID,
		RepoOwner: "tenant2",
		RepoName:  "app-two",
		Status:    store.StatusLive,
	}
	_ = st.Put(p1)
	_ = st.Put(p2)

	// 1. User 1 views app-one (allowed)
	req := httptest.NewRequest("GET", "/projects/app-one", nil)
	req.AddCookie(cookie1)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 viewing own project, got %d", rec.Code)
	}

	// 2. User 1 views app-two (forbidden/isolated -> 404)
	req = httptest.NewRequest("GET", "/projects/app-two", nil)
	req.AddCookie(cookie1)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 accessing other tenant's project, got %d", rec.Code)
	}

	// 3. User 2 views app-one (forbidden/isolated -> 404)
	req = httptest.NewRequest("GET", "/projects/app-one", nil)
	req.AddCookie(cookie2)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 for tenant 2 accessing app-one, got %d", rec.Code)
	}

	// 4. User 1 attempts to redeploy app-two -> 404
	req = httptest.NewRequest("POST", "/projects/app-two/redeploy", nil)
	req.AddCookie(cookie1)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 redeploying another tenant's project, got %d", rec.Code)
	}

	// 5. User 1 attempts to delete app-two -> 404, app-two must remain untouched
	req = httptest.NewRequest("DELETE", "/projects/app-two", nil)
	req.AddCookie(cookie1)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 deleting another tenant's project, got %d", rec.Code)
	}
	if st.Get("app-two") == nil {
		t.Fatalf("app-two was deleted by unauthorized tenant!")
	}

	// 6. User 1 deletes their own project app-one -> succeeds
	req = httptest.NewRequest("DELETE", "/projects/app-one", nil)
	req.AddCookie(cookie1)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if loc := rec.Header().Get("HX-Redirect"); loc != "/" {
		t.Errorf("expected HX-Redirect to / on successful delete, got %s", loc)
	}
	if st.Get("app-one") != nil {
		t.Errorf("app-one was not deleted by its owner")
	}
}

func TestProjectSlugCollisionAcrossUsers(t *testing.T) {
	st, router, _ := setupTestServer(t)

	u1, _ := st.CreateUser("user1@example.com", "password123")
	u2, _ := st.CreateUser("user2@example.com", "password123")
	s2, _ := st.CreateSession(u2.ID, 1*time.Hour)
	cookie2 := &http.Cookie{Name: "pilo_session", Value: s2.ID}

	// User 1 already owns "popular-tool"
	p1 := &store.Project{
		ID:        "popular-tool",
		Slug:      "popular-tool",
		UserID:    u1.ID,
		RepoOwner: "user1",
		RepoName:  "popular-tool",
		Status:    store.StatusLive,
	}
	_ = st.Put(p1)

	// User 2 attempts to claim the same slug "popular-tool"
	form := url.Values{
		"repo":   {"user2/popular-tool"},
		"slug":   {"popular-tool"},
		"branch": {"main"},
	}
	req := httptest.NewRequest("POST", "/projects", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie2)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	// Should reject creation with error notice
	if !strings.Contains(rec.Body.String(), "popular-tool") || !strings.Contains(rec.Body.String(), "already exists") {
		t.Errorf("expected slug conflict error in HTML response, got: %s", rec.Body.String())
	}

	// Ensure User 1 still owns the project
	existing := st.Get("popular-tool")
	if existing == nil || existing.UserID != u1.ID {
		t.Fatalf("existing project owner was altered during conflict")
	}
}

func TestHTMXUnauthorizedHandling(t *testing.T) {
	_, router, _ := setupTestServer(t)

	// Unauthenticated HTMX request to protected endpoint
	req := httptest.NewRequest("GET", "/projects/new", nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 OK for HTMX redirect trigger, got %d", rec.Code)
	}
	if loc := rec.Header().Get("HX-Redirect"); loc != "/login" {
		t.Errorf("expected HX-Redirect header to /login, got %s", loc)
	}
}

func TestPublicStaticRouteServing(t *testing.T) {
	_, router, _ := setupTestServer(t)

	// Create a dummy static site folder
	siteDir := t.TempDir()
	indexHTML := "<html><body><h1>Hello from static tool</h1></body></html>"
	if err := os.WriteFile(filepath.Join(siteDir, "index.html"), []byte(indexHTML), 0o644); err != nil {
		t.Fatalf("failed to write test index.html: %v", err)
	}

	// Register static project route
	router.RegisterProject("my-public-app", http.FileServer(http.Dir(siteDir)))

	// Make an unauthenticated request to the static site
	req := httptest.NewRequest("GET", "/my-public-app/", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for public static site, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Hello from static tool") {
		t.Errorf("expected static site content, got: %s", rec.Body.String())
	}

	// Unregister project and verify it returns to admin 404 (or login redirect)
	router.UnregisterProject("my-public-app")
	req2 := httptest.NewRequest("GET", "/my-public-app/", nil)
	rec2 := httptest.NewRecorder()
	router.ServeHTTP(rec2, req2)
	// Without route, admin mux receives it and unauthenticated access redirects to /login
	if rec2.Code != http.StatusSeeOther && rec2.Code != http.StatusNotFound {
		t.Errorf("expected redirect or 404 after unregister, got %d", rec2.Code)
	}
}

func TestUserSettingsIsolation(t *testing.T) {
	st, router, _ := setupTestServer(t)

	_ = st.SetGitHubToken("global_default_pat")
	_ = st.SetWebhookSecret("global_default_sec")

	u1, _ := st.CreateUser("user_a@example.com", "password123")
	s1, _ := st.CreateSession(u1.ID, 1*time.Hour)
	cookie1 := &http.Cookie{Name: "pilo_session", Value: s1.ID}

	u2, _ := st.CreateUser("user_b@example.com", "password123")
	s2, _ := st.CreateSession(u2.ID, 1*time.Hour)
	cookie2 := &http.Cookie{Name: "pilo_session", Value: s2.ID}

	// User 1 saves their personal GitHub token
	form := url.Values{"github_token": {"ghp_personal_a"}}
	req := httptest.NewRequest("POST", "/settings", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie1)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Settings saved") {
		t.Fatalf("failed to save settings for user 1: status %d", rec.Code)
	}

	// User 1's effective token is their personal token
	if tok := st.EffectiveGitHubToken(u1.ID); tok != "ghp_personal_a" {
		t.Errorf("expected user 1 effective token to be ghp_personal_a, got %s", tok)
	}

	// User 2's effective token falls back to global default
	if tok := st.EffectiveGitHubToken(u2.ID); tok != "global_default_pat" {
		t.Errorf("expected user 2 effective token to fall back to global, got %s", tok)
	}

	// User 2 visits settings, should not show HasToken = true for personal token
	req = httptest.NewRequest("GET", "/settings", nil)
	req.AddCookie(cookie2)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if strings.Contains(rec.Body.String(), "currently set") {
		t.Errorf("user 2 settings page should not reflect user 1's personal token")
	}
}
