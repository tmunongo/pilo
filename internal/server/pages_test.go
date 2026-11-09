package server_test

import (
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

	// 1. Unauthenticated access to index should redirect to /login
	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect for unauthenticated index, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/login" {
		t.Errorf("expected redirect location /login, got %s", loc)
	}

	// 2. Unauthenticated access to /login and /signup
	req = httptest.NewRequest("GET", "/login", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login page returned status %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Sign in") {
		t.Errorf("expected sign in heading on login page")
	}

	req = httptest.NewRequest("GET", "/signup", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("signup page returned status %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Sign up") {
		t.Errorf("expected sign up heading on signup page")
	}

	// 3. Register user via POST /signup
	form := url.Values{
		"email":            {"owner@example.com"},
		"password":         {"secret1234"},
		"password_confirm": {"secret1234"},
	}
	req = httptest.NewRequest("POST", "/signup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect after signup, got %d", rec.Code)
	}

	var sessionCookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == "pilo_session" {
			sessionCookie = c
			break
		}
	}
	if sessionCookie == nil || sessionCookie.Value == "" {
		t.Fatalf("expected pilo_session cookie after signup")
	}

	user, err := st.GetUserByEmail("owner@example.com")
	if err != nil {
		t.Fatalf("failed to query created user: %v", err)
	}

	// 4. Authenticated Index page (empty state)
	req = httptest.NewRequest("GET", "/", nil)
	req.AddCookie(sessionCookie)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("authenticated index page returned status %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Nothing deployed yet") {
		t.Errorf("expected empty state message, got: %s", body)
	}
	if !strings.Contains(body, "pilo · v0.1.0 · :8080") {
		t.Errorf("expected footer with version and port, got: %s", body)
	}
	if !strings.Contains(body, "sign out") {
		t.Errorf("expected sign out button in header")
	}

	// 5. Add projects for this user
	p1 := &store.Project{
		ID:         "fresh-app",
		Slug:       "fresh-app",
		UserID:     user.ID,
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
		UserID:     user.ID,
		RepoOwner:  "user",
		RepoName:   "idle-app",
		Branch:     "main",
		Status:     store.StatusLive,
		LastCommit: "def4567890",
		UpdatedAt:  time.Now().Add(-2 * time.Hour),
		CreatedAt:  time.Now().Add(-24 * time.Hour),
	}
	p3 := &store.Project{
		ID:        "building-app",
		Slug:      "building-app",
		UserID:    user.ID,
		RepoOwner: "user",
		RepoName:  "building-app",
		Branch:    "main",
		Status:    store.StatusCloning,
		UpdatedAt: time.Now(),
		CreatedAt: time.Now(),
	}
	p4 := &store.Project{
		ID:        "failed-app",
		Slug:      "failed-app",
		UserID:    user.ID,
		RepoOwner: "user",
		RepoName:  "failed-app",
		Branch:    "main",
		Status:    store.StatusError,
		LastError: "git clone authentication failed",
		UpdatedAt: time.Now().Add(-10 * time.Minute),
		CreatedAt: time.Now(),
	}
	// And a project owned by a different user
	otherUser, _ := st.CreateUser("other@example.com", "othersecret")
	pOther := &store.Project{
		ID:        "other-app",
		Slug:      "other-app",
		UserID:    otherUser.ID,
		RepoOwner: "other",
		RepoName:  "other-app",
		Branch:    "main",
		Status:    store.StatusLive,
	}

	for _, p := range []*store.Project{p1, p2, p3, p4, pOther} {
		if err := st.Put(p); err != nil {
			t.Fatalf("failed to insert project %s: %v", p.Slug, err)
		}
	}

	// 6. Index page (with projects)
	req = httptest.NewRequest("GET", "/", nil)
	req.AddCookie(sessionCookie)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("index page with projects returned status %d", rec.Code)
	}
	body = rec.Body.String()
	if !strings.Contains(body, "4 sites served from this machine") {
		t.Errorf("expected 4 sites (not counting other user's), got: %s", body)
	}
	if !strings.Contains(body, "One is building right now.") {
		t.Errorf("expected building mention in lead, got: %s", body)
	}
	if !strings.Contains(body, "fresh-app") || !strings.Contains(body, "idle-app") {
		t.Errorf("expected projects in table, got: %s", body)
	}
	if strings.Contains(body, "other-app") {
		t.Errorf("other user's project should NOT appear in dashboard")
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
	if !strings.Contains(body, `hx-trigger="every 1.5s"`) {
		t.Errorf("expected index page to poll when a project is building")
	}

	// 7. New project page
	req = httptest.NewRequest("GET", "/projects/new", nil)
	req.AddCookie(sessionCookie)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("new project page returned status %d", rec.Code)
	}
	body = rec.Body.String()
	if !strings.Contains(body, "Clone a GitHub repository") {
		t.Errorf("expected new project page lead, got: %s", body)
	}

	// 8. Project detail page for owner
	req = httptest.NewRequest("GET", "/projects/fresh-app", nil)
	req.AddCookie(sessionCookie)
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
	if strings.Contains(body, `hx-trigger="every 1.5s"`) {
		t.Errorf("live project should not have polling trigger")
	}

	// 8b. Project detail page for building project should poll
	req = httptest.NewRequest("GET", "/projects/building-app", nil)
	req.AddCookie(sessionCookie)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("building project detail page returned status %d", rec.Code)
	}
	body = rec.Body.String()
	if !strings.Contains(body, `hx-trigger="every 1.5s"`) {
		t.Errorf("expected building project detail page to poll with hx-trigger")
	}

	// 9. Cross-tenant isolation: Owner cannot access other-app
	req = httptest.NewRequest("GET", "/projects/other-app", nil)
	req.AddCookie(sessionCookie)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 Not Found accessing other user's project, got %d", rec.Code)
	}

	// 10. Settings page
	req = httptest.NewRequest("GET", "/settings", nil)
	req.AddCookie(sessionCookie)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("settings page returned status %d", rec.Code)
	}
	body = rec.Body.String()
	if !strings.Contains(body, "Your public username, GitHub credentials, and webhook configuration.") {
		t.Errorf("expected settings page lead")
	}
	if !strings.Contains(body, "nav-link--active") {
		t.Errorf("expected active settings nav link")
	}

	// 11. Password reset flow
	forgotForm := url.Values{"email": {"owner@example.com"}}
	req = httptest.NewRequest("POST", "/forgot-password", strings.NewReader(forgotForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 from forgot password, got %d", rec.Code)
	}
	if handlers.Mailer.LastResetURL == "" {
		t.Fatalf("expected reset URL to be generated by mailer")
	}

	// Extract token from reset URL
	resetParsed, err := url.Parse(handlers.Mailer.LastResetURL)
	if err != nil {
		t.Fatalf("invalid reset URL: %v", err)
	}
	token := resetParsed.Query().Get("token")
	if token == "" {
		t.Fatalf("empty token in reset URL")
	}

	// Reset password via POST /reset-password
	resetForm := url.Values{
		"token":            {token},
		"password":         {"brandnewpass123"},
		"password_confirm": {"brandnewpass123"},
	}
	req = httptest.NewRequest("POST", "/reset-password", strings.NewReader(resetForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect after password reset, got %d", rec.Code)
	}

	// Sign in with new password
	loginForm := url.Values{
		"email":    {"owner@example.com"},
		"password": {"brandnewpass123"},
	}
	req = httptest.NewRequest("POST", "/login", strings.NewReader(loginForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect after successful login, got %d", rec.Code)
	}

	// 12. Sign out
	req = httptest.NewRequest("POST", "/logout", nil)
	req.AddCookie(sessionCookie)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect after logout, got %d", rec.Code)
	}
}
