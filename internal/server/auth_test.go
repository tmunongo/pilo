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

func setupTestServer(t *testing.T) (*store.Store, *server.Router, *server.Handlers) {
	t.Helper()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	st, err := store.New(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	dep := deploy.New(filepath.Join(tmpDir, "repos"))
	router := server.NewRouter(nil)
	handlers := server.NewHandlers(st, dep, router, "http://localhost:8080")
	handlers.Port = "8080"
	handlers.Version = "v0.1.0"
	disabled := false
	handlers.SignupsDisabled = &disabled // explicitly enable signups for auth testing
	router.SetAdmin(handlers.Mux())

	return st, router, handlers
}

func TestAuthLoginForm(t *testing.T) {
	st, router, _ := setupTestServer(t)

	// 1. Fresh GET /login
	req := httptest.NewRequest("GET", "/login", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK from GET /login, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `action="/login"`) {
		t.Errorf("expected form with action /login")
	}

	// 2. GET /login with notice param
	req = httptest.NewRequest("GET", "/login?notice=Please+sign+in+first", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if !strings.Contains(rec.Body.String(), "Please sign in first") {
		t.Errorf("expected notice in login HTML")
	}

	// 3. GET /login when already authenticated -> redirect to /
	u, _ := st.CreateUser("logged@example.com", "password123")
	sess, _ := st.CreateSession(u.ID, 1*time.Hour)

	req = httptest.NewRequest("GET", "/login", nil)
	req.AddCookie(&http.Cookie{Name: "pilo_session", Value: sess.ID})
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Errorf("expected 303 redirect to / for logged in user, got %d to %s", rec.Code, rec.Header().Get("Location"))
	}
}

func TestAuthLoginAction(t *testing.T) {
	st, router, _ := setupTestServer(t)
	_, _ = st.CreateUser("valid@example.com", "correctpassword123")

	// 1. Invalid login (wrong password)
	form := url.Values{
		"email":    {"valid@example.com"},
		"password": {"wrongpass"},
	}
	req := httptest.NewRequest("POST", "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK rendering error on invalid login, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Invalid email or password") {
		t.Errorf("expected 'Invalid email or password' message in response")
	}

	// 2. Invalid login (unknown user)
	formUnknown := url.Values{
		"email":    {"ghost@example.com"},
		"password": {"somepassword"},
	}
	req = httptest.NewRequest("POST", "/login", strings.NewReader(formUnknown.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if !strings.Contains(rec.Body.String(), "Invalid email or password") {
		t.Errorf("expected 'Invalid email or password' message for unknown user")
	}

	// 3. Valid login
	formValid := url.Values{
		"email":    {"VALID@example.com"}, // test case insensitivity
		"password": {"correctpassword123"},
	}
	req = httptest.NewRequest("POST", "/login", strings.NewReader(formValid.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Fatalf("expected 303 redirect to / on valid login, got %d", rec.Code)
	}

	var sessionCookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == "pilo_session" {
			sessionCookie = c
			break
		}
	}
	if sessionCookie == nil || sessionCookie.Value == "" {
		t.Fatalf("expected pilo_session cookie to be set")
	}
	if !sessionCookie.HttpOnly {
		t.Errorf("expected session cookie to be HttpOnly")
	}
	if sessionCookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("expected session cookie SameSite to be Lax")
	}

	// Verify session in DB
	sessUser, err := st.GetSessionUser(sessionCookie.Value)
	if err != nil || sessUser.Email != "valid@example.com" {
		t.Errorf("session did not resolve to user in store: %v", err)
	}
}

func TestAuthSignUpAction(t *testing.T) {
	st, router, _ := setupTestServer(t)

	// 1. Password mismatch
	form := url.Values{
		"email":            {"new@example.com"},
		"password":         {"password123"},
		"password_confirm": {"mismatch999"},
	}
	req := httptest.NewRequest("POST", "/signup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if !strings.Contains(rec.Body.String(), "Passwords do not match") {
		t.Errorf("expected password mismatch error in HTML")
	}

	// 2. Password too short (< 8 chars)
	formShort := url.Values{
		"email":            {"new@example.com"},
		"password":         {"1234567"},
		"password_confirm": {"1234567"},
	}
	req = httptest.NewRequest("POST", "/signup", strings.NewReader(formShort.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if !strings.Contains(rec.Body.String(), "at least 8 characters long") {
		t.Errorf("expected password length error in HTML")
	}

	// 3. Valid registration
	formValid := url.Values{
		"email":            {"new@example.com"},
		"password":         {"validpassword123"},
		"password_confirm": {"validpassword123"},
	}
	req = httptest.NewRequest("POST", "/signup", strings.NewReader(formValid.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Fatalf("expected 303 redirect to / after signup, got %d", rec.Code)
	}

	// 4. Duplicate registration attempt
	req = httptest.NewRequest("POST", "/signup", strings.NewReader(formValid.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if !strings.Contains(rec.Body.String(), "An account with that email already exists") {
		t.Errorf("expected duplicate email error on second registration attempt")
	}

	// 5. GET /signup when already authenticated redirects to /
	u, _ := st.GetUserByEmail("new@example.com")
	sess, _ := st.CreateSession(u.ID, 1*time.Hour)
	req = httptest.NewRequest("GET", "/signup", nil)
	req.AddCookie(&http.Cookie{Name: "pilo_session", Value: sess.ID})
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Errorf("expected authenticated GET /signup to redirect to /")
	}
}

func TestSignupsDisabled(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")
	st, err := store.New(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer st.Close()

	router := server.NewRouter(nil)
	handlers := server.NewHandlers(st, nil, router, "http://localhost:8080")
	disabled := true
	handlers.SignupsDisabled = &disabled
	router.SetAdmin(handlers.Mux())

	// 1. GET /signup should redirect to login with notice
	req := httptest.NewRequest("GET", "/signup", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect when signups disabled, got %d", rec.Code)
	}
	if !strings.Contains(rec.Header().Get("Location"), "notice=") {
		t.Errorf("expected notice in redirect URL, got %s", rec.Header().Get("Location"))
	}

	// 2. POST /signup should be rejected and redirect to login
	form := url.Values{
		"email":            {"someone@example.com"},
		"password":         {"password123"},
		"password_confirm": {"password123"},
	}
	reqPost := httptest.NewRequest("POST", "/signup", strings.NewReader(form.Encode()))
	reqPost.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recPost := httptest.NewRecorder()
	router.ServeHTTP(recPost, reqPost)
	if recPost.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 redirect on POST /signup when signups disabled, got %d", recPost.Code)
	}
	if !strings.Contains(recPost.Header().Get("Location"), "notice=") {
		t.Errorf("expected notice in redirect URL, got %s", recPost.Header().Get("Location"))
	}

	// User should not have been created
	count, err := st.CountUsers()
	if err != nil || count != 0 {
		t.Errorf("expected 0 users created when signups disabled, got count=%d err=%v", count, err)
	}

	// 3. GET /login should display notice that signups are disabled instead of create account link
	reqLogin := httptest.NewRequest("GET", "/login", nil)
	recLogin := httptest.NewRecorder()
	router.ServeHTTP(recLogin, reqLogin)
	if recLogin.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on GET /login, got %d", recLogin.Code)
	}
	if strings.Contains(recLogin.Body.String(), `href="/signup"`) {
		t.Errorf("login page should not show signup link when signups are disabled")
	}
	if !strings.Contains(recLogin.Body.String(), "Signups are temporarily disabled") {
		t.Errorf("login page should indicate signups are disabled")
	}

	// 4. Test Auto mode (SignupsDisabled == nil): open when 0 users, auto-closes once user created
	autoHandlers := server.NewHandlers(st, nil, router, "http://localhost:8080")
	if autoHandlers.IsSignupDisabled() {
		t.Errorf("expected signups to be enabled when no accounts exist in auto mode")
	}
	_, err = st.CreateUser("firstuser@example.com", "password123")
	if err != nil {
		t.Fatalf("failed to create user: %v", err)
	}
	if !autoHandlers.IsSignupDisabled() {
		t.Errorf("expected signups to be auto-disabled once an account exists in auto mode")
	}
}

func TestAuthLogout(t *testing.T) {
	st, router, _ := setupTestServer(t)

	u, _ := st.CreateUser("logout_user@example.com", "password123")
	sess, _ := st.CreateSession(u.ID, 1*time.Hour)

	// POST /logout
	req := httptest.NewRequest("POST", "/logout", nil)
	req.AddCookie(&http.Cookie{Name: "pilo_session", Value: sess.ID})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Fatalf("expected 303 redirect to /login after logout, got %d", rec.Code)
	}

	// Verify cookie clearing
	var clearedCookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == "pilo_session" {
			clearedCookie = c
			break
		}
	}
	if clearedCookie == nil || clearedCookie.MaxAge != -1 {
		t.Errorf("expected session cookie to be cleared with MaxAge -1")
	}

	// Verify session deleted in DB
	_, err := st.GetSessionUser(sess.ID)
	if err != store.ErrSessionNotFound {
		t.Errorf("session was not revoked from database after logout")
	}
}

func TestAuthForgotPasswordAndReset(t *testing.T) {
	st, router, handlers := setupTestServer(t)
	_, _ = st.CreateUser("member@example.com", "oldpassword123")

	// 1. Forgot password for unknown user (anti-enumeration check)
	unknownForm := url.Values{"email": {"nobody@example.com"}}
	req := httptest.NewRequest("POST", "/forgot-password", strings.NewReader(unknownForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK from forgot-password, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "If an account exists for that email") {
		t.Errorf("expected enumeration-safe success message")
	}

	// 2. Forgot password for existing user
	knownForm := url.Values{"email": {"member@example.com"}}
	req = httptest.NewRequest("POST", "/forgot-password", strings.NewReader(knownForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if !strings.Contains(rec.Body.String(), "If an account exists for that email") {
		t.Errorf("expected success message for known user")
	}
	if handlers.Mailer.LastResetURL == "" {
		t.Fatalf("expected mailer to capture reset link")
	}

	// 3. GET /reset-password with invalid token
	req = httptest.NewRequest("GET", "/reset-password?token=invalidtoken", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if !strings.Contains(rec.Body.String(), "link is invalid or has expired") {
		t.Errorf("expected invalid token message on GET /reset-password with bogus token")
	}

	// 4. GET /reset-password without token
	req = httptest.NewRequest("GET", "/reset-password", nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if !strings.Contains(rec.Body.String(), "link is invalid or has expired") {
		t.Errorf("expected invalid token message on GET /reset-password without token")
	}

	// 5. GET /reset-password with valid token
	parsedURL, _ := url.Parse(handlers.Mailer.LastResetURL)
	validToken := parsedURL.Query().Get("token")

	req = httptest.NewRequest("GET", "/reset-password?token="+validToken, nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK on valid token reset form, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `name="token" value="`+validToken+`"`) {
		t.Errorf("expected form with valid token hidden input")
	}

	// 6. POST /reset-password with mismatched passwords
	mismatchForm := url.Values{
		"token":            {validToken},
		"password":         {"brandnew123"},
		"password_confirm": {"different123"},
	}
	req = httptest.NewRequest("POST", "/reset-password", strings.NewReader(mismatchForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if !strings.Contains(rec.Body.String(), "Passwords do not match") {
		t.Errorf("expected mismatch error in HTML")
	}

	// 7. POST /reset-password successfully
	successForm := url.Values{
		"token":            {validToken},
		"password":         {"brandnew123"},
		"password_confirm": {"brandnew123"},
	}
	req = httptest.NewRequest("POST", "/reset-password", strings.NewReader(successForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "/login") {
		t.Fatalf("expected redirect to /login after successful password reset, got %d", rec.Code)
	}

	// 8. Re-attempting reset with same token fails
	req = httptest.NewRequest("POST", "/reset-password", strings.NewReader(successForm.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if !strings.Contains(rec.Body.String(), "link is invalid or has expired") {
		t.Errorf("expected token invalidation after reuse attempt")
	}
}
