package server_test

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pilo/internal/deploy"
	"pilo/internal/server"
	"pilo/internal/store"
)

func TestPagesRender(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_pages.db")

	st, err := store.New(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer st.Close()

	dep := deploy.New(filepath.Join(tmpDir, "repos"))
	router := server.NewRouter(nil)
	handlers := server.NewHandlers(st, dep, router, "http://localhost:8080")
	handlers.Port = "8080"
	handlers.Version = "v0.1.0"
	router.SetAdmin(handlers.Mux())

	// 1. Index page (empty state)
	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("index page returned status %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Nothing deployed yet") {
		t.Errorf("expected empty state message, got: %s", body)
	}
	if !strings.Contains(body, "pilo · v0.1.0 · :8080") {
		t.Errorf("expected footer with version and port, got: %s", body)
	}

	// 2. Add projects: one live (fresh), one idle, one building, one failed
	p1 := &store.Project{
		ID:         "fresh-app",
		Slug:       "fresh-app",
		RepoOwner:  "user",
		RepoName:   "fresh-app",
		Branch:     "main",
		Status:     store.StatusLive,
		LastCommit: "abc1234567",
		UpdatedAt:  time.Now(),
		CreatedAt:  time.Now(),
	}
	p2 := &store.Project{
		ID:         "idle-app",
		Slug:       "idle-app",
		RepoOwner:  "user",
		RepoName:   "idle-app",
		Branch:     "main",
		Status:     store.StatusLive,
		LastCommit: "def4567890",
		UpdatedAt:  time.Now().Add(-2 * time.Hour),
		CreatedAt:  time.Now().Add(-24 * time.Hour),
	}
	p3 := &store.Project{
		ID:         "building-app",
		Slug:       "building-app",
		RepoOwner:  "user",
		RepoName:   "building-app",
		Branch:     "main",
		Status:     store.StatusCloning,
		UpdatedAt:  time.Now(),
		CreatedAt:  time.Now(),
	}
	p4 := &store.Project{
		ID:         "failed-app",
		Slug:       "failed-app",
		RepoOwner:  "user",
		RepoName:   "failed-app",
		Branch:     "main",
		Status:     store.StatusError,
		LastError:  "git clone authentication failed",
		UpdatedAt:  time.Now().Add(-10 * time.Minute),
		CreatedAt:  time.Now(),
	}
	for _, p := range []*store.Project{p1, p2, p3, p4} {
		if err := st.Put(p); err != nil {
			t.Fatalf("failed to insert project %s: %v", p.Slug, err)
		}
	}

	// 3. Index page (with projects)
	req = httptest.NewRequest("GET", "/", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("index page with projects returned status %d", rec.Code)
	}
	body = rec.Body.String()
	if !strings.Contains(body, "4 sites served from this machine") {
		t.Errorf("expected projects count in lead, got: %s", body)
	}
	if !strings.Contains(body, "One is building right now.") {
		t.Errorf("expected building mention in lead, got: %s", body)
	}
	if !strings.Contains(body, "fresh-app") || !strings.Contains(body, "idle-app") {
		t.Errorf("expected projects in table, got: %s", body)
	}
	// Check lamp mark states
	if !strings.Contains(body, "lamp-mark--solid") {
		t.Errorf("expected solid lamp mark for fresh project")
	}
	if !strings.Contains(body, "lamp-mark--idle") {
		t.Errorf("expected idle lamp mark for idle project")
	}
	if !strings.Contains(body, "lamp-mark--building") {
		t.Errorf("expected building lamp mark for building project")
	}
	if !strings.Contains(body, "× failed") {
		t.Errorf("expected × failed for error project")
	}

	// 4. New project page
	req = httptest.NewRequest("GET", "/projects/new", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("new project page returned status %d", rec.Code)
	}
	body = rec.Body.String()
	if !strings.Contains(body, "Clone a GitHub repository") {
		t.Errorf("expected new project page lead, got: %s", body)
	}

	// 5. Project detail page
	req = httptest.NewRequest("GET", "/projects/fresh-app", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("project detail page returned status %d", rec.Code)
	}
	body = rec.Body.String()
	if !strings.Contains(body, "fresh-app") {
		t.Errorf("expected slug on project page")
	}
	if !strings.Contains(body, "Serving.") {
		t.Errorf("expected 'Serving.' in status line")
	}
	if !strings.Contains(body, "redeploy") {
		t.Errorf("expected redeploy button")
	}
	if !strings.Contains(body, "last deploy") {
		t.Errorf("expected last deploy log section")
	}

	// 6. Settings page
	req = httptest.NewRequest("GET", "/settings", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("settings page returned status %d", rec.Code)
	}
	body = rec.Body.String()
	if !strings.Contains(body, "GitHub credentials and webhook configuration.") {
		t.Errorf("expected settings page lead")
	}
	if !strings.Contains(body, "nav-link--active") {
		t.Errorf("expected active settings nav link")
	}
}

