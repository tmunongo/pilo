package server_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
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
	req := httptest.NewRequest("GET", "/dashboard", nil)
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
	req = httptest.NewRequest("GET", "/dashboard", nil)
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
	req = httptest.NewRequest("GET", "/dashboard", nil)
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
	if !strings.Contains(body, "Clone a Git repository") {
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

func TestDeploymentHistoryPagination(t *testing.T) {
	for _, count := range []int{0, 10, 11, 21} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			st, router, handlers := setupTestServer(t)
			u, err := st.CreateUser("history@example.com", "password123")
			if err != nil {
				t.Fatal(err)
			}
			sess, err := st.CreateSession(u.ID, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			p := &store.Project{Slug: "history", UserID: u.ID, RepoOwner: "owner", RepoName: "site", Status: store.StatusCloning}
			if err := st.Put(p); err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(handlers.Deployer.ReposRoot, ".releases", p.CheckoutKey())
			if err := os.MkdirAll(root, 0755); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < count; i++ {
				id := fmt.Sprintf("r_%032x", i)
				meta, err := json.Marshal(map[string]any{"Commit": fmt.Sprintf("%040x", i), "Created": time.Unix(int64(i), 0), "Message": fmt.Sprintf("deploy-message-%02d", i)})
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, id+".json"), meta, 0600); err != nil {
					t.Fatal(err)
				}
				if i >= count-5 {
					if err := os.Mkdir(filepath.Join(root, id), 0755); err != nil {
						t.Fatal(err)
					}
				}
			}
			totalPages := max(1, (count+9)/10)
			for _, tc := range []struct {
				query string
				page  int
			}{
				{"", 1}, {"abc", 1}, {"-1", 1}, {"0", 1},
				{"2", min(2, totalPages)}, {"3", min(3, totalPages)}, {"999", totalPages},
				{"999999999999999999999999999999", 1},
			} {
				req := httptest.NewRequest(http.MethodGet, "/projects/history?deploy_page="+tc.query, nil)
				req.AddCookie(&http.Cookie{Name: "pilo_session", Value: sess.ID})
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)
				body := rec.Body.String()
				if rec.Code != http.StatusOK {
					t.Fatalf("query=%q returned %d: %s", tc.query, rec.Code, body)
				}
				start := (tc.page - 1) * 10
				end := min(start+10, count)
				if got := strings.Count(body, "deploy-message-"); got != end-start {
					t.Errorf("query=%q: expected %d rows, got %d", tc.query, end-start, got)
				}
				for i := 0; i < count; i++ {
					position := count - 1 - i
					want := position >= start && position < end
					if strings.Contains(body, fmt.Sprintf("deploy-message-%02d", i)) != want {
						t.Errorf("query=%q: deployment %d on wrong page", tc.query, i)
					}
				}
				if strings.Contains(body, `aria-label="Deployment pages"`) != (totalPages > 1) {
					t.Errorf("query=%q: pagination should only appear above 10 deployments", tc.query)
				}
				if !strings.Contains(body, fmt.Sprintf(`hx-get="/projects/history?deploy_page=%d"`, tc.page)) {
					t.Errorf("query=%q: polling must preserve the current page", tc.query)
				}
				if totalPages > 1 && !strings.Contains(body, fmt.Sprintf("page %d of %d", tc.page, totalPages)) {
					t.Errorf("query=%q: wrong page indicator", tc.query)
				}
				if tc.page > 1 && !strings.Contains(body, fmt.Sprintf(`href="/projects/history?deploy_page=%d#deploys"`, tc.page-1)) {
					t.Errorf("query=%q: missing previous link", tc.query)
				}
				if tc.page < totalPages && !strings.Contains(body, fmt.Sprintf(`href="/projects/history?deploy_page=%d#deploys"`, tc.page+1)) {
					t.Errorf("query=%q: missing next link", tc.query)
				}
				wantRollback := 0
				if tc.page == 1 {
					wantRollback = min(count, 5)
				}
				if got := strings.Count(body, `action="/projects/history/rollback"`); got != wantRollback {
					t.Errorf("query=%q: expected %d available rollback actions, got %d", tc.query, wantRollback, got)
				}
			}
		})
	}
}

func TestProjectWebhookGuidance(t *testing.T) {
	st, router, _ := setupTestServer(t)
	defer st.Close()
	u, err := st.CreateUser("guidance@example.com", "password123")
	if err != nil {
		t.Fatal(err)
	}
	sess, err := st.CreateSession(u.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetUserSetting(u.ID, "webhook_secret", "configured-secret"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		slug, host, logStatus, want string
	}{
		{"legacy", "", "", "Awaiting first delivery."},
		{"github", "github.com", "success", "Latest delivery succeeded."},
		{"failed-hook", "github.com", "error", "Latest delivery failed."},
		{"ignored-hook", "github.com", "ignored", "Latest delivery was ignored."},
		{"gitlab", "gitlab.com", "", "For this gitlab.com repository, use redeploy"},
		{"codeberg", "codeberg.org", "", "For this codeberg.org repository, use redeploy"},
	} {
		t.Run(tc.slug, func(t *testing.T) {
			p := &store.Project{Slug: tc.slug, UserID: u.ID, RepoHost: tc.host, RepoOwner: "owner", RepoName: "site", Branch: "main", Status: store.StatusLive}
			if err := st.Put(p); err != nil {
				t.Fatal(err)
			}
			if tc.logStatus != "" {
				if err := st.CreateWebhookLog(&store.WebhookLog{ProjectSlug: tc.slug, UserID: u.ID, Event: "ping", Status: tc.logStatus}); err != nil {
					t.Fatal(err)
				}
			}
			req := httptest.NewRequest(http.MethodGet, "/projects/"+tc.slug, nil)
			req.AddCookie(&http.Cookie{Name: "pilo_session", Value: sess.ID})
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			body := rec.Body.String()
			if rec.Code != http.StatusOK || !strings.Contains(body, tc.want) {
				t.Fatalf("expected guidance %q, got status %d, body %s", tc.want, rec.Code, body)
			}
			isGitHub := tc.host == "" || tc.host == "github.com"
			if strings.Contains(body, "/webhooks/github") != isGitHub {
				t.Error("GitHub webhook endpoint should only appear for GitHub projects")
			}
			if !isGitHub && strings.Contains(body, "webhook logs") {
				t.Error("account webhook secret should not enable webhook logs for other providers")
			}
		})
	}
}

func TestLandingAndNotFound(t *testing.T) {
	st, router, _ := setupTestServer(t)
	defer st.Close()

	for _, tc := range []struct {
		path     string
		status   int
		contains string
	}{
		{"/", http.StatusOK, "A little home for your websites."},
		{"/missing/page", http.StatusNotFound, "Nothing here."},
	} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest("GET", tc.path, nil))
		if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.contains) {
			t.Errorf("GET %s: got status %d, expected %d and %q", tc.path, rec.Code, tc.status, tc.contains)
		}
	}
}

func TestDashboardPagination(t *testing.T) {
	st, router, _ := setupTestServer(t)
	user, err := st.CreateUser("pages@example.com", "password123")
	if err != nil {
		t.Fatal(err)
	}
	session, err := st.CreateSession(user.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 12; i++ {
		slug := fmt.Sprintf("project-%02d", i)
		status := store.StatusLive
		if i == 0 {
			status = store.StatusCloning
		}
		if err := st.Put(&store.Project{ID: slug, Slug: slug, UserID: user.ID, RepoOwner: "owner", RepoName: slug, Branch: "main", Status: status, CreatedAt: time.Now().Add(time.Duration(i) * time.Second)}); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct{ query, page, present, absent string }{
		{"", "page 1 of 2", "project-00", "project-11"},
		{"?page=2", "page 2 of 2", "project-11", "project-00"},
		{"?page=999", "page 2 of 2", "project-11", "project-00"},
		{"?page=bad", "page 1 of 2", "project-00", "project-11"},
	} {
		req := httptest.NewRequest("GET", "/dashboard"+tc.query, nil)
		req.AddCookie(&http.Cookie{Name: "pilo_session", Value: session.ID})
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		body := rec.Body.String()
		if rec.Code != http.StatusOK || !strings.Contains(body, tc.page) || !strings.Contains(body, tc.present) || strings.Contains(body, tc.absent) || !strings.Contains(body, "12 sites served") {
			t.Errorf("dashboard%s: unexpected status or content: %d", tc.query, rec.Code)
		}
		if tc.query == "?page=2" && !strings.Contains(body, `hx-get="/dashboard?page=2"`) {
			t.Error("polling should retain the current page")
		}
	}
}
