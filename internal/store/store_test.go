package store_test

import (
	"path/filepath"
	"testing"
	"time"

	"pilo/internal/store"
)

func TestSQLiteStore(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_paas.db")

	st, err := store.New(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer st.Close()

	// Test settings
	if err := st.SetGitHubToken("ghp_test123"); err != nil {
		t.Fatalf("failed to set github token: %v", err)
	}
	if st.GitHubToken != "ghp_test123" {
		t.Errorf("expected token ghp_test123, got %s", st.GitHubToken)
	}

	if err := st.SetWebhookSecret("secret456"); err != nil {
		t.Fatalf("failed to set webhook secret: %v", err)
	}
	if st.WebhookSecret != "secret456" {
		t.Errorf("expected secret secret456, got %s", st.WebhookSecret)
	}

	// Test CRUD projects
	p1 := &store.Project{
		ID:        "my-app",
		Slug:      "my-app",
		RepoOwner: "octocat",
		RepoName:  "my-app",
		Branch:    "main",
		Subdir:    "web",
		Status:    store.StatusLive,
	}

	if err := st.Put(p1); err != nil {
		t.Fatalf("failed to put project: %v", err)
	}

	got := st.Get("my-app")
	if got == nil {
		t.Fatalf("expected project 'my-app', got nil")
	}
	if got.RepoOwner != "octocat" || got.RepoName != "my-app" || got.Status != store.StatusLive {
		t.Errorf("unexpected project fields: %+v", got)
	}

	all := st.All()
	if len(all) != 1 {
		t.Errorf("expected 1 project, got %d", len(all))
	}

	if err := st.Delete("my-app"); err != nil {
		t.Fatalf("failed to delete project: %v", err)
	}

	if st.Get("my-app") != nil {
		t.Errorf("expected project to be deleted")
	}

	// Test reload from same DB file
	st.Close()
	st2, err := store.New(dbPath)
	if err != nil {
		t.Fatalf("failed to reload store: %v", err)
	}
	defer st2.Close()

	if st2.GitHubToken != "ghp_test123" || st2.WebhookSecret != "secret456" {
		t.Errorf("reloaded settings mismatched: token=%s secret=%s", st2.GitHubToken, st2.WebhookSecret)
	}
}

func TestUserManagement(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_user.db")

	st, err := store.New(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer st.Close()

	// 1. Create User
	u, err := st.CreateUser("user@example.com", "password123")
	if err != nil {
		t.Fatalf("failed to create user: %v", err)
	}
	if u.Email != "user@example.com" {
		t.Errorf("expected email user@example.com, got %s", u.Email)
	}

	// 2. Duplicate user creation should fail
	_, err = st.CreateUser("USER@example.com", "password123")
	if err != store.ErrEmailTaken {
		t.Errorf("expected ErrEmailTaken, got %v", err)
	}

	// 3. Short password validation
	_, err = st.CreateUser("other@example.com", "short")
	if err == nil {
		t.Error("expected error for password < 8 chars")
	}

	// 4. Authenticate User
	authU, err := st.AuthenticateUser("user@example.com", "password123")
	if err != nil {
		t.Fatalf("failed to authenticate valid user: %v", err)
	}
	if authU.ID != u.ID {
		t.Errorf("authenticated user ID mismatch: got %s, expected %s", authU.ID, u.ID)
	}

	// 5. Wrong password
	_, err = st.AuthenticateUser("user@example.com", "wrongpass")
	if err != store.ErrInvalidCredentials {
		t.Errorf("expected ErrInvalidCredentials for wrong password, got %v", err)
	}

	// 6. Unknown user
	_, err = st.AuthenticateUser("unknown@example.com", "password123")
	if err != store.ErrInvalidCredentials {
		t.Errorf("expected ErrInvalidCredentials for unknown email, got %v", err)
	}
}

func TestSessionManagement(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_session.db")

	st, err := store.New(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer st.Close()

	u, err := st.CreateUser("session@example.com", "password123")
	if err != nil {
		t.Fatalf("failed to create user: %v", err)
	}

	// Create active session
	sess, err := st.CreateSession(u.ID, 1*time.Hour)
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	// Fetch active session user
	gotU, err := st.GetSessionUser(sess.ID)
	if err != nil {
		t.Fatalf("failed to get session user: %v", err)
	}
	if gotU.ID != u.ID || gotU.Email != u.Email {
		t.Errorf("session user mismatch: %+v vs %+v", gotU, u)
	}

	// Create expired session
	expSess, err := st.CreateSession(u.ID, -1*time.Minute)
	if err != nil {
		t.Fatalf("failed to create expired session: %v", err)
	}
	_, err = st.GetSessionUser(expSess.ID)
	if err != store.ErrSessionNotFound {
		t.Errorf("expected ErrSessionNotFound for expired session, got %v", err)
	}

	// Delete session (logout)
	if err := st.DeleteSession(sess.ID); err != nil {
		t.Fatalf("failed to delete session: %v", err)
	}
	_, err = st.GetSessionUser(sess.ID)
	if err != store.ErrSessionNotFound {
		t.Errorf("expected ErrSessionNotFound after session deletion, got %v", err)
	}
}

func TestPasswordReset(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_reset.db")

	st, err := store.New(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer st.Close()

	u, err := st.CreateUser("reset@example.com", "originalpassword")
	if err != nil {
		t.Fatalf("failed to create user: %v", err)
	}

	// Create session to test revocation on password reset
	sess, err := st.CreateSession(u.ID, 1*time.Hour)
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	// Request password reset
	rawToken, resetU, err := st.CreatePasswordReset("reset@example.com", 1*time.Hour)
	if err != nil {
		t.Fatalf("failed to create password reset: %v", err)
	}
	if resetU.ID != u.ID {
		t.Errorf("user mismatch in reset request")
	}

	// Verify token
	verifiedU, err := st.VerifyPasswordReset(rawToken)
	if err != nil {
		t.Fatalf("failed to verify valid reset token: %v", err)
	}
	if verifiedU.Email != u.Email {
		t.Errorf("verified user email mismatch")
	}

	// Reset password
	if err := st.ResetPasswordWithToken(rawToken, "newpassword123"); err != nil {
		t.Fatalf("failed to reset password: %v", err)
	}

	// Verify token cannot be reused
	_, err = st.VerifyPasswordReset(rawToken)
	if err != store.ErrInvalidResetToken {
		t.Errorf("expected ErrInvalidResetToken after use, got %v", err)
	}

	// Authenticate with old password (should fail)
	_, err = st.AuthenticateUser("reset@example.com", "originalpassword")
	if err != store.ErrInvalidCredentials {
		t.Errorf("old password still authenticated after reset")
	}

	// Authenticate with new password (should succeed)
	_, err = st.AuthenticateUser("reset@example.com", "newpassword123")
	if err != nil {
		t.Errorf("new password failed to authenticate: %v", err)
	}

	// Verify old session was revoked
	_, err = st.GetSessionUser(sess.ID)
	if err != store.ErrSessionNotFound {
		t.Errorf("old session still active after password reset")
	}
}

func TestAccountScopedProjectsAndSettings(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_scoped.db")

	st, err := store.New(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer st.Close()

	u1, _ := st.CreateUser("user1@example.com", "password123")
	u2, _ := st.CreateUser("user2@example.com", "password123")

	// Project for User 1
	p1 := &store.Project{
		ID:        "project-u1",
		Slug:      "project-u1",
		UserID:    u1.ID,
		RepoOwner: "user1",
		RepoName:  "repo1",
		Status:    store.StatusLive,
	}
	// Project for User 2
	p2 := &store.Project{
		ID:        "project-u2",
		Slug:      "project-u2",
		UserID:    u2.ID,
		RepoOwner: "user2",
		RepoName:  "repo2",
		Status:    store.StatusLive,
	}

	_ = st.Put(p1)
	_ = st.Put(p2)

	// User 1 projects
	u1Projects := st.AllForUser(u1.ID)
	if len(u1Projects) != 1 || u1Projects[0].Slug != "project-u1" {
		t.Errorf("expected 1 project for user 1, got %d", len(u1Projects))
	}

	// User 2 cannot access User 1 project via GetForUser
	if p := st.GetForUser("project-u1", u2.ID); p != nil {
		t.Errorf("user 2 should not be able to get user 1 project")
	}
	if p := st.GetForUser("project-u1", u1.ID); p == nil {
		t.Errorf("user 1 should be able to get their own project")
	}

	// Test user-specific settings vs global
	_ = st.SetGitHubToken("global_token")
	if tok := st.EffectiveGitHubToken(u1.ID); tok != "global_token" {
		t.Errorf("expected fallback to global token, got %s", tok)
	}

	_ = st.SetUserSetting(u1.ID, "github_token", "u1_personal_token")
	if tok := st.EffectiveGitHubToken(u1.ID); tok != "u1_personal_token" {
		t.Errorf("expected user 1 custom token, got %s", tok)
	}
	if tok := st.EffectiveGitHubToken(u2.ID); tok != "global_token" {
		t.Errorf("expected user 2 to still have global token, got %s", tok)
	}
}

func TestWebhookLogsStore(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_webhook_logs.db")

	st, err := store.New(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer st.Close()

	u, err := st.CreateUser("webhooker@example.com", "password123")
	if err != nil {
		t.Fatalf("failed to create user: %v", err)
	}

	p := &store.Project{
		ID:        "app-webhooks",
		Slug:      "app-webhooks",
		UserID:    u.ID,
		RepoOwner: "octocat",
		RepoName:  "app-webhooks",
		Branch:    "main",
		Status:    store.StatusLive,
	}
	if err := st.Put(p); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	// Initially no webhook logs
	logs, err := st.WebhookLogsForProject(p.Slug, 10)
	if err != nil {
		t.Fatalf("failed to query webhook logs: %v", err)
	}
	if len(logs) != 0 {
		t.Fatalf("expected 0 webhook logs, got %d", len(logs))
	}

	// Insert a log entry
	log1 := &store.WebhookLog{
		ProjectSlug: p.Slug,
		UserID:      u.ID,
		Event:       "push",
		Delivery:    "deliv-1234",
		Signature:   "sha256=abcdef",
		Status:      "success",
		Message:     "Deployed c0ffee on main",
		CreatedAt:   time.Now().Add(-1 * time.Minute),
	}
	if err := st.CreateWebhookLog(log1); err != nil {
		t.Fatalf("failed to create webhook log: %v", err)
	}

	// Insert another log entry
	log2 := &store.WebhookLog{
		ProjectSlug: p.Slug,
		UserID:      u.ID,
		Event:       "ping",
		Delivery:    "deliv-5678",
		Signature:   "sha256=abcdef",
		Status:      "success",
		Message:     "Webhook ping received",
		CreatedAt:   time.Now(),
	}
	if err := st.CreateWebhookLog(log2); err != nil {
		t.Fatalf("failed to create webhook log: %v", err)
	}

	// List logs for project (should be in descending order of created_at)
	logs, err = st.WebhookLogsForProject(p.Slug, 10)
	if err != nil {
		t.Fatalf("failed to query webhook logs: %v", err)
	}
	if len(logs) != 2 {
		t.Fatalf("expected 2 webhook logs, got %d", len(logs))
	}
	if logs[0].Event != "ping" || logs[1].Event != "push" {
		t.Errorf("logs not in expected order: %v, %v", logs[0].Event, logs[1].Event)
	}

	// Test LatestWebhookLogForUser
	latestUserLog, err := st.LatestWebhookLogForUser(u.ID)
	if err != nil {
		t.Fatalf("failed to get latest user webhook log: %v", err)
	}
	if latestUserLog == nil || latestUserLog.Event != "ping" {
		t.Errorf("expected latest user webhook log to be ping, got %v", latestUserLog)
	}

	// Test LatestWebhookLogForProject
	latestProjLog, err := st.LatestWebhookLogForProject(p.Slug)
	if err != nil {
		t.Fatalf("failed to get latest project webhook log: %v", err)
	}
	if latestProjLog == nil || latestProjLog.Event != "ping" {
		t.Errorf("expected latest project webhook log to be ping, got %v", latestProjLog)
	}
}
