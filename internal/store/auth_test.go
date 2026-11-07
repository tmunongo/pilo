package store_test

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pilo/internal/store"
)

func TestUserManagementEdgeCases(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_users_edge.db")

	st, err := store.New(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer st.Close()

	// 1. Empty email validation
	_, err = st.CreateUser("", "password123")
	if err == nil || !strings.Contains(err.Error(), "email cannot be empty") {
		t.Errorf("expected error for empty email, got %v", err)
	}

	// 2. Whitespace email validation
	_, err = st.CreateUser("   ", "password123")
	if err == nil || !strings.Contains(err.Error(), "email cannot be empty") {
		t.Errorf("expected error for whitespace email, got %v", err)
	}

	// 3. Short password validation (< 8 chars)
	_, err = st.CreateUser("user@example.com", "1234567")
	if err == nil || !strings.Contains(err.Error(), "at least 8 characters") {
		t.Errorf("expected error for password < 8 chars, got %v", err)
	}

	// 4. Case-insensitive email normalization
	u1, err := st.CreateUser("Alice@Example.COM", "password123")
	if err != nil {
		t.Fatalf("failed to create user with mixed case email: %v", err)
	}
	if u1.Email != "alice@example.com" {
		t.Errorf("expected normalized email alice@example.com, got %s", u1.Email)
	}

	// 5. Query user by ID
	byID, err := st.GetUserByID(u1.ID)
	if err != nil {
		t.Fatalf("failed to get user by ID: %v", err)
	}
	if byID.Email != "alice@example.com" {
		t.Errorf("expected email alice@example.com, got %s", byID.Email)
	}

	// 6. Query non-existent user by ID
	_, err = st.GetUserByID("non-existent-id")
	if err != store.ErrUserNotFound {
		t.Errorf("expected ErrUserNotFound for unknown ID, got %v", err)
	}

	// 7. Query non-existent user by Email
	_, err = st.GetUserByEmail("nobody@example.com")
	if err != store.ErrUserNotFound {
		t.Errorf("expected ErrUserNotFound for unknown email, got %v", err)
	}

	// 8. Update password with short password
	err = st.UpdateUserPassword(u1.ID, "short")
	if err == nil {
		t.Error("expected error updating password to < 8 chars")
	}

	// 9. Update password successfully
	err = st.UpdateUserPassword(u1.ID, "brandnewpassword")
	if err != nil {
		t.Fatalf("failed to update user password: %v", err)
	}

	// Authenticate with new password
	authU, err := st.AuthenticateUser("ALICE@example.com", "brandnewpassword")
	if err != nil {
		t.Fatalf("failed to authenticate with updated password and mixed-case email: %v", err)
	}
	if authU.ID != u1.ID {
		t.Errorf("user ID mismatch after password update")
	}

	// 10. Count users
	count, err := st.CountUsers()
	if err != nil {
		t.Fatalf("failed to count users: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 user, got %d", count)
	}
}

func TestSessionEdgeCases(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_sessions_edge.db")

	st, err := store.New(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer st.Close()

	u, err := st.CreateUser("session_user@example.com", "password123")
	if err != nil {
		t.Fatalf("failed to create user: %v", err)
	}

	// 1. Empty session ID lookup
	_, err = st.GetSessionUser("")
	if err != store.ErrSessionNotFound {
		t.Errorf("expected ErrSessionNotFound for empty session ID, got %v", err)
	}

	// 2. Non-existent session ID lookup
	_, err = st.GetSessionUser("completely_random_token_that_does_not_exist")
	if err != store.ErrSessionNotFound {
		t.Errorf("expected ErrSessionNotFound for non-existent session ID, got %v", err)
	}

	// 3. Multiple sessions for the same user
	s1, err := st.CreateSession(u.ID, 1*time.Hour)
	if err != nil {
		t.Fatalf("failed to create session 1: %v", err)
	}
	s2, err := st.CreateSession(u.ID, 2*time.Hour)
	if err != nil {
		t.Fatalf("failed to create session 2: %v", err)
	}
	sExpired, err := st.CreateSession(u.ID, -10*time.Minute)
	if err != nil {
		t.Fatalf("failed to create expired session: %v", err)
	}

	// Both active sessions should resolve
	uFrom1, err := st.GetSessionUser(s1.ID)
	if err != nil || uFrom1.ID != u.ID {
		t.Errorf("failed to resolve session 1: %v", err)
	}
	uFrom2, err := st.GetSessionUser(s2.ID)
	if err != nil || uFrom2.ID != u.ID {
		t.Errorf("failed to resolve session 2: %v", err)
	}

	// Expired session should not resolve
	_, err = st.GetSessionUser(sExpired.ID)
	if err != store.ErrSessionNotFound {
		t.Errorf("expected ErrSessionNotFound for expired session, got %v", err)
	}

	// 4. Cleanup expired sessions
	if err := st.CleanupExpiredSessions(); err != nil {
		t.Fatalf("failed to cleanup expired sessions: %v", err)
	}

	// 5. Delete all user sessions (e.g. on password reset or account revocation)
	if err := st.DeleteUserSessions(u.ID); err != nil {
		t.Fatalf("failed to delete all user sessions: %v", err)
	}
	_, err = st.GetSessionUser(s1.ID)
	if err != store.ErrSessionNotFound {
		t.Errorf("session 1 still valid after DeleteUserSessions")
	}
	_, err = st.GetSessionUser(s2.ID)
	if err != store.ErrSessionNotFound {
		t.Errorf("session 2 still valid after DeleteUserSessions")
	}
}

func TestPasswordResetEdgeCases(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_resets_edge.db")

	st, err := store.New(dbPath)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}
	defer st.Close()

	_, err = st.CreateUser("target@example.com", "initialpassword")
	if err != nil {
		t.Fatalf("failed to create user: %v", err)
	}

	// 1. Password reset for non-existent user
	_, _, err = st.CreatePasswordReset("unknown@example.com", 1*time.Hour)
	if err != store.ErrUserNotFound {
		t.Errorf("expected ErrUserNotFound, got %v", err)
	}

	// 2. Password reset verification with completely invalid token
	_, err = st.VerifyPasswordReset("invalid_token_string")
	if err != store.ErrInvalidResetToken {
		t.Errorf("expected ErrInvalidResetToken for invalid token, got %v", err)
	}

	// 3. Password reset with expired duration
	expiredToken, _, err := st.CreatePasswordReset("target@example.com", -5*time.Minute)
	if err != nil {
		t.Fatalf("failed to create expired reset token: %v", err)
	}
	_, err = st.VerifyPasswordReset(expiredToken)
	if err != store.ErrInvalidResetToken {
		t.Errorf("expected ErrInvalidResetToken for expired token, got %v", err)
	}

	// 4. Reset execution with short password
	validToken, _, err := st.CreatePasswordReset("target@example.com", 1*time.Hour)
	if err != nil {
		t.Fatalf("failed to create valid reset token: %v", err)
	}
	err = st.ResetPasswordWithToken(validToken, "short")
	if err == nil || !strings.Contains(err.Error(), "at least 8 characters") {
		t.Errorf("expected error for short new password, got %v", err)
	}

	// 5. Reset execution with invalid token
	err = st.ResetPasswordWithToken("bogus_token", "brandnewpass123")
	if err != store.ErrInvalidResetToken {
		t.Errorf("expected ErrInvalidResetToken for bogus token in reset, got %v", err)
	}
}

func TestLegacyMigrationAndUnownedAdoption(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "legacy_paas.db")

	// Step 1: Manually create an older schema DB without users or user_id on projects
	rawDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("failed to open raw sqlite: %v", err)
	}
	_, err = rawDB.Exec(`
		CREATE TABLE projects (
			id          TEXT PRIMARY KEY,
			slug        TEXT UNIQUE NOT NULL,
			repo_owner  TEXT NOT NULL DEFAULT '',
			repo_name   TEXT NOT NULL DEFAULT '',
			branch      TEXT NOT NULL DEFAULT 'main',
			subdir      TEXT NOT NULL DEFAULT '',
			status      TEXT NOT NULL DEFAULT 'live',
			last_commit TEXT NOT NULL DEFAULT '',
			last_error  TEXT NOT NULL DEFAULT '',
			created_at  DATETIME NOT NULL,
			updated_at  DATETIME NOT NULL
		);
		INSERT INTO projects (id, slug, repo_owner, repo_name, branch, status, created_at, updated_at)
		VALUES ('legacy-site', 'legacy-site', 'octocat', 'hello-world', 'main', 'live', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP);
	`)
	if err != nil {
		t.Fatalf("failed to create legacy schema: %v", err)
	}
	_ = rawDB.Close()

	// Step 2: Open with store.New to trigger embedded migrations and column check
	st, err := store.New(dbPath)
	if err != nil {
		t.Fatalf("store.New failed on legacy database: %v", err)
	}
	defer st.Close()

	// Verify legacy project exists and has empty user_id
	p := st.Get("legacy-site")
	if p == nil {
		t.Fatalf("legacy project missing after migration")
	}
	if p.UserID != "" {
		t.Errorf("expected empty user_id before any user registers, got %s", p.UserID)
	}

	// Step 3: Register the first user ever
	admin, err := st.CreateUser("admin@example.com", "adminpassword123")
	if err != nil {
		t.Fatalf("failed to create initial user: %v", err)
	}

	// Verify the legacy project was adopted by the first user!
	pAdopted := st.Get("legacy-site")
	if pAdopted.UserID != admin.ID {
		t.Errorf("expected legacy project to be adopted by first user %s, got %s", admin.ID, pAdopted.UserID)
	}

	// Step 4: Register a second user
	secondUser, err := st.CreateUser("second@example.com", "secondpassword123")
	if err != nil {
		t.Fatalf("failed to create second user: %v", err)
	}

	// Verify second user did not steal the project
	pCheck := st.Get("legacy-site")
	if pCheck.UserID != admin.ID {
		t.Errorf("project owner changed unexpectedly after second user registration")
	}

	// Legacy project should show up in admin's list, not secondUser's
	if len(st.AllForUser(admin.ID)) != 1 {
		t.Errorf("expected 1 project for admin, got %d", len(st.AllForUser(admin.ID)))
	}
	if len(st.AllForUser(secondUser.ID)) != 0 {
		t.Errorf("expected 0 projects for second user, got %d", len(st.AllForUser(secondUser.ID)))
	}
}
