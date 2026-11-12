// Package store provides a SQLite-backed persistence layer for Pilo.
// Uses modernc.org/sqlite — a pure-Go driver that compiles without CGO,
// so cross-compiling for Linux/ARM64 (Raspberry Pi) is seamless.
package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite" // registers the "sqlite" driver

	"pilo/internal/store/db"
)

var (
	ErrUserNotFound       = errors.New("user not found")
	ErrEmailTaken         = errors.New("email is already registered")
	ErrInvalidCredentials = errors.New("invalid email or password")
	ErrSessionNotFound    = errors.New("session not found or expired")
	ErrInvalidResetToken  = errors.New("password reset token is invalid or expired")
	ErrProjectNotFound    = errors.New("project not found")
)

// Status represents the lifecycle state of a deployed project.
type Status string

const (
	StatusCloning Status = "cloning"
	StatusLive    Status = "live"
	StatusError   Status = "error"
)

// User represents an account in Pilo.
type User struct {
	ID           string    `json:"id"`
	Email        string    `json:"email"`
	Username     string    `json:"username"`
	PasswordHash string    `json:"-"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Session represents an authenticated web session.
type Session struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
}

// Project is a static site deployed under its owner namespace.
type Project struct {
	SourceType  string    `json:"source_type"`
	Namespace   string    `json:"namespace"`
	ID          string    `json:"id"`
	Slug        string    `json:"slug"`    // URL path segment, e.g. "my-app"
	UserID      string    `json:"user_id"` // owner user account
	RepoHost    string    `json:"repo_host"`
	AccessToken string    `json:"-"`
	RepoOwner   string    `json:"repo_owner"` // e.g. "octocat"
	RepoName    string    `json:"repo_name"`  // e.g. "my-app"
	Branch      string    `json:"branch"`     // default "main"
	Subdir      string    `json:"subdir"`     // optional, for monorepos
	Status      Status    `json:"status"`
	LastCommit  string    `json:"last_commit"`
	LastError   string    `json:"last_error,omitempty"`
	UpdatedAt   time.Time `json:"updated_at"`
	CreatedAt   time.Time `json:"created_at"`
}

// RepoURL returns the https clone URL for this project's GitHub repo.
func (p *Project) IsUpload() bool { return p.SourceType == "upload" }

func (p *Project) RepoURL() string {
	if p.IsUpload() {
		return ""
	}
	host := p.RepoHost
	if host == "" {
		host = "github.com"
	}
	return fmt.Sprintf("https://%s/%s/%s.git", host, p.RepoOwner, p.RepoName)
}

func (s *Store) ProjectToken(p *Project) string {
	if p.AccessToken != "" {
		return p.AccessToken
	}
	return s.EffectiveGitHubToken(p.UserID)
}

// WebhookLog represents an incoming webhook event delivery log for a deployment.
type WebhookLog struct {
	ID          string    `json:"id"`
	ProjectSlug string    `json:"project_slug"`
	UserID      string    `json:"user_id"`
	Event       string    `json:"event"`
	Delivery    string    `json:"delivery"`
	Signature   string    `json:"signature"`
	Status      string    `json:"status"` // "success", "error", "ignored"
	Message     string    `json:"message"`
	CreatedAt   time.Time `json:"created_at"`
}

// Store holds the SQLite database connection, sqlc queries, and cached settings.
// Safe for concurrent use.
type Store struct {
	mu            sync.RWMutex
	db            *sql.DB
	queries       *db.Queries
	GitHubToken   string
	WebhookSecret string
}

// New opens (or creates) the SQLite database at dbPath, runs schema
// migrations via embedded SQL, and loads settings into memory.
func New(dbPath string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, fmt.Errorf("creating db dir: %w", err)
	}
	sqlDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("opening sqlite db: %w", err)
	}
	// SQLite only supports one writer at a time; serialise writes in the
	// driver to avoid "database is locked" errors under concurrent requests.
	sqlDB.SetMaxOpenConns(1)

	if _, err := sqlDB.Exec("PRAGMA foreign_keys = ON;"); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("enabling foreign keys: %w", err)
	}

	ctx := context.Background()
	if err := db.Migrate(ctx, sqlDB); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("running db migrations: %w", err)
	}

	s := &Store{
		db:      sqlDB,
		queries: db.New(sqlDB),
	}
	if err := s.loadSettings(); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the underlying database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// DB returns the underlying *sql.DB for advanced transactions.
func (s *Store) DB() *sql.DB {
	return s.db
}

// Queries returns the generated sqlc *db.Queries.
func (s *Store) Queries() *db.Queries {
	return s.queries
}

// --- settings ------------------------------------------------------------

func (s *Store) loadSettings() error {
	ctx := context.Background()
	settings, err := s.queries.ListSettings(ctx)
	if err != nil {
		return fmt.Errorf("loading settings: %w", err)
	}
	for _, row := range settings {
		switch row.Key {
		case "github_token":
			s.GitHubToken = row.Value
		case "webhook_secret":
			s.WebhookSecret = row.Value
		}
	}
	return nil
}

// SetGitHubToken persists the shared global GitHub PAT.
func (s *Store) SetGitHubToken(token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx := context.Background()
	if err := s.queries.UpsertSetting(ctx, db.UpsertSettingParams{Key: "github_token", Value: token}); err != nil {
		return fmt.Errorf("saving github_token: %w", err)
	}
	s.GitHubToken = token
	return nil
}

// SetWebhookSecret persists the shared global webhook HMAC secret.
func (s *Store) SetWebhookSecret(secret string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx := context.Background()
	if err := s.queries.UpsertSetting(ctx, db.UpsertSettingParams{Key: "webhook_secret", Value: secret}); err != nil {
		return fmt.Errorf("saving webhook_secret: %w", err)
	}
	s.WebhookSecret = secret
	return nil
}

// UserSetting gets a per-user setting by key.
func (s *Store) UserSetting(userID, key string) (string, error) {
	ctx := context.Background()
	val, err := s.queries.GetUserSetting(ctx, db.GetUserSettingParams{
		UserID: userID,
		Key:    key,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return val, nil
}

// SetUserSetting sets a per-user setting by key.
func (s *Store) SetUserSetting(userID, key, value string) error {
	ctx := context.Background()
	return s.queries.UpsertUserSetting(ctx, db.UpsertUserSettingParams{
		UserID: userID,
		Key:    key,
		Value:  value,
	})
}

// EffectiveGitHubToken returns the user's PAT, or falls back to global PAT.
func (s *Store) EffectiveGitHubToken(userID string) string {
	if userID != "" {
		if tok, err := s.UserSetting(userID, "github_token"); err == nil && tok != "" {
			return tok
		}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.GitHubToken
}

// EffectiveWebhookSecret returns the user's secret, or falls back to global secret.
func (s *Store) EffectiveWebhookSecret(userID string) string {
	if userID != "" {
		if sec, err := s.UserSetting(userID, "webhook_secret"); err == nil && sec != "" {
			return sec
		}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.WebhookSecret
}

// --- users ---------------------------------------------------------------

func (s *Store) CreateUser(email, password string, usernames ...string) (*User, error) {
	username := ""
	if len(usernames) > 0 {
		username = strings.ToLower(strings.TrimSpace(usernames[0]))
	}
	if err := ValidateUsername(username); err != nil {
		return nil, err
	}
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return nil, errors.New("email cannot be empty")
	}
	if len(password) < 8 {
		return nil, errors.New("password must be at least 8 characters")
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("hashing password: %w", err)
	}

	ctx := context.Background()
	now := time.Now().UTC()
	uID := uuid.New().String()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	q := s.queries.WithTx(tx)
	row, err := q.CreateUser(ctx, db.CreateUserParams{
		ID:           uID,
		Email:        email,
		PasswordHash: string(hash),
		CreatedAt:    now,
		UpdatedAt:    now,
	})
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return nil, ErrEmailTaken
		}
		return nil, fmt.Errorf("inserting user: %w", err)
	}

	if err := q.UpdateUsername(ctx, db.UpdateUsernameParams{Username: username, UpdatedAt: now, ID: uID}); err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return nil, ErrUsernameTaken
		}
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	row.Username = username

	// If this is the first user ever registered and there are legacy unowned projects,
	// associate those projects with this initial user.
	_ = s.adoptUnownedProjects(ctx, uID)

	return &User{
		ID:           row.ID,
		Email:        row.Email,
		Username:     row.Username,
		PasswordHash: row.PasswordHash,
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
	}, nil
}

func (s *Store) adoptUnownedProjects(ctx context.Context, userID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE projects SET user_id = ? WHERE user_id = ''`, userID)
	return err
}

func (s *Store) AuthenticateUser(email, password string) (*User, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	ctx := context.Background()
	row, err := s.queries.GetUserByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrInvalidCredentials
		}
		return nil, fmt.Errorf("querying user: %w", err)
	}

	if err := bcrypt.CompareHashAndPassword([]byte(row.PasswordHash), []byte(password)); err != nil {
		return nil, ErrInvalidCredentials
	}

	return &User{
		ID:           row.ID,
		Email:        row.Email,
		Username:     row.Username,
		PasswordHash: row.PasswordHash,
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
	}, nil
}

func (s *Store) GetUserByID(id string) (*User, error) {
	ctx := context.Background()
	row, err := s.queries.GetUserByID(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, err
	}
	return &User{
		ID:           row.ID,
		Email:        row.Email,
		Username:     row.Username,
		PasswordHash: row.PasswordHash,
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
	}, nil
}

func (s *Store) GetUserByEmail(email string) (*User, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	ctx := context.Background()
	row, err := s.queries.GetUserByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, err
	}
	return &User{
		ID:           row.ID,
		Email:        row.Email,
		Username:     row.Username,
		PasswordHash: row.PasswordHash,
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
	}, nil
}

func (s *Store) UpdateUserPassword(userID, newPassword string) error {
	if len(newPassword) < 8 {
		return errors.New("password must be at least 8 characters")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hashing password: %w", err)
	}

	ctx := context.Background()
	now := time.Now().UTC()
	return s.queries.UpdateUserPassword(ctx, db.UpdateUserPasswordParams{
		PasswordHash: string(hash),
		UpdatedAt:    now,
		ID:           userID,
	})
}

func (s *Store) CountUsers() (int64, error) {
	ctx := context.Background()
	return s.queries.CountUsers(ctx)
}

// --- sessions ------------------------------------------------------------

func generateSecureToken(byteLen int) (string, error) {
	b := make([]byte, byteLen)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (s *Store) CreateSession(userID string, duration time.Duration) (*Session, error) {
	token, err := generateSecureToken(32)
	if err != nil {
		return nil, fmt.Errorf("generating session token: %w", err)
	}

	ctx := context.Background()
	now := time.Now().UTC()
	expiresAt := now.Add(duration)

	row, err := s.queries.CreateSession(ctx, db.CreateSessionParams{
		ID:        token,
		UserID:    userID,
		ExpiresAt: expiresAt,
		CreatedAt: now,
	})
	if err != nil {
		return nil, fmt.Errorf("inserting session: %w", err)
	}

	return &Session{
		ID:        row.ID,
		UserID:    row.UserID,
		ExpiresAt: row.ExpiresAt,
		CreatedAt: row.CreatedAt,
	}, nil
}

func (s *Store) GetSessionUser(sessionID string) (*User, error) {
	if sessionID == "" {
		return nil, ErrSessionNotFound
	}
	ctx := context.Background()
	now := time.Now().UTC()
	row, err := s.queries.GetSessionWithUser(ctx, db.GetSessionWithUserParams{
		ID:        sessionID,
		ExpiresAt: now,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrSessionNotFound
		}
		return nil, err
	}

	return &User{
		ID:        row.UserID,
		Email:     row.Email,
		Username:  row.Username,
		CreatedAt: row.UserCreatedAt,
		UpdatedAt: row.UserUpdatedAt,
	}, nil
}

func (s *Store) DeleteSession(sessionID string) error {
	ctx := context.Background()
	return s.queries.DeleteSession(ctx, sessionID)
}

func (s *Store) DeleteUserSessions(userID string) error {
	ctx := context.Background()
	return s.queries.DeleteSessionsByUserID(ctx, userID)
}

func (s *Store) CleanupExpiredSessions() error {
	ctx := context.Background()
	return s.queries.DeleteExpiredSessions(ctx, time.Now().UTC())
}

// --- password reset ------------------------------------------------------

func (s *Store) CreatePasswordReset(email string, duration time.Duration) (rawToken string, user *User, err error) {
	u, err := s.GetUserByEmail(email)
	if err != nil {
		return "", nil, err
	}

	rawToken, err = generateSecureToken(32)
	if err != nil {
		return "", nil, fmt.Errorf("generating reset token: %w", err)
	}

	sum := sha256.Sum256([]byte(rawToken))
	tokenHash := hex.EncodeToString(sum[:])

	ctx := context.Background()
	now := time.Now().UTC()
	expiresAt := now.Add(duration)
	resetID := uuid.New().String()

	_, err = s.queries.CreatePasswordReset(ctx, db.CreatePasswordResetParams{
		ID:        resetID,
		UserID:    u.ID,
		TokenHash: tokenHash,
		ExpiresAt: expiresAt,
		CreatedAt: now,
	})
	if err != nil {
		return "", nil, fmt.Errorf("inserting password reset: %w", err)
	}

	return rawToken, u, nil
}

func (s *Store) VerifyPasswordReset(rawToken string) (*User, error) {
	sum := sha256.Sum256([]byte(rawToken))
	tokenHash := hex.EncodeToString(sum[:])

	ctx := context.Background()
	now := time.Now().UTC()
	row, err := s.queries.GetActivePasswordResetByTokenHash(ctx, db.GetActivePasswordResetByTokenHashParams{
		TokenHash: tokenHash,
		ExpiresAt: now,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrInvalidResetToken
		}
		return nil, err
	}

	return &User{
		ID:        row.UserID,
		Email:     row.Email,
		CreatedAt: row.CreatedAt,
		UpdatedAt: row.CreatedAt,
	}, nil
}

func (s *Store) ResetPasswordWithToken(rawToken, newPassword string) error {
	if len(newPassword) < 8 {
		return errors.New("password must be at least 8 characters")
	}

	sum := sha256.Sum256([]byte(rawToken))
	tokenHash := hex.EncodeToString(sum[:])

	ctx := context.Background()
	now := time.Now().UTC()

	row, err := s.queries.GetActivePasswordResetByTokenHash(ctx, db.GetActivePasswordResetByTokenHashParams{
		TokenHash: tokenHash,
		ExpiresAt: now,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrInvalidResetToken
		}
		return err
	}

	newHash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hashing password: %w", err)
	}

	// Transaction to update password, invalidate reset token, and revoke old sessions
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	qtx := s.queries.WithTx(tx)

	if err := qtx.UpdateUserPassword(ctx, db.UpdateUserPasswordParams{
		PasswordHash: string(newHash),
		UpdatedAt:    now,
		ID:           row.UserID,
	}); err != nil {
		return fmt.Errorf("updating password: %w", err)
	}

	if err := qtx.MarkPasswordResetUsed(ctx, db.MarkPasswordResetUsedParams{
		UsedAt: sql.NullTime{Time: now, Valid: true},
		ID:     row.ID,
	}); err != nil {
		return fmt.Errorf("marking reset token used: %w", err)
	}

	if err := qtx.DeleteSessionsByUserID(ctx, row.UserID); err != nil {
		return fmt.Errorf("invalidating sessions: %w", err)
	}

	return tx.Commit()
}

// --- project queries -----------------------------------------------------

// All returns every project, ordered by creation time ascending.
func (s *Store) All() []*Project {
	ctx := context.Background()
	rows, err := s.queries.ListProjects(ctx)
	if err != nil {
		return nil
	}

	var out []*Project
	for _, r := range rows {
		out = append(out, s.toProjectModel(r))
	}
	return out
}

// AllForUser returns every project owned by a specific user.
func (s *Store) AllForUser(userID string) []*Project {
	ctx := context.Background()
	rows, err := s.queries.ListProjectsByUserID(ctx, userID)
	if err != nil {
		return nil
	}

	var out []*Project
	for _, r := range rows {
		out = append(out, s.toProjectModel(r))
	}
	return out
}

// Get returns the project with the given slug, or nil if not found.
func (s *Store) Get(slug string) *Project {
	ctx := context.Background()
	r, err := s.queries.GetProjectBySlug(ctx, slug)
	if err != nil {
		return nil
	}
	return s.toProjectModel(r)
}

// GetForUser returns the project with the given slug owned by userID, or nil if not found.
func (s *Store) GetForUser(slug, userID string) *Project {
	ctx := context.Background()
	r, err := s.queries.GetProjectBySlugAndUserID(ctx, db.GetProjectBySlugAndUserIDParams{
		Slug:   slug,
		UserID: userID,
	})
	if err != nil {
		return nil
	}
	return s.toProjectModel(r)
}

// Put inserts or updates a project in the database.
func (s *Store) Put(p *Project) error {
	ctx := context.Background()
	now := time.Now().UTC()
	p.UpdatedAt = now
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now
	}
	if p.ID == "" {
		p.ID = uuid.NewString()
	}

	if p.SourceType == "" {
		p.SourceType = "git"
	}
	return s.queries.UpsertProject(ctx, db.UpsertProjectParams{
		SourceType:  p.SourceType,
		ID:          p.ID,
		Slug:        p.Slug,
		UserID:      p.UserID,
		RepoHost:    p.RepoHost,
		AccessToken: p.AccessToken,
		RepoOwner:   p.RepoOwner,
		RepoName:    p.RepoName,
		Branch:      p.Branch,
		Subdir:      p.Subdir,
		Status:      string(p.Status),
		LastCommit:  p.LastCommit,
		LastError:   p.LastError,
		CreatedAt:   p.CreatedAt.UTC(),
		UpdatedAt:   p.UpdatedAt.UTC(),
	})
}

// Delete removes a project by slug.
func (s *Store) Delete(slug string) error {
	ctx := context.Background()
	return s.queries.DeleteProjectBySlug(ctx, slug)
}

// DeleteForUser removes a project by slug and user ID.
func (s *Store) DeleteForUser(slug, userID string) error {
	ctx := context.Background()
	return s.queries.DeleteProjectBySlugAndUserID(ctx, db.DeleteProjectBySlugAndUserIDParams{
		Slug:   slug,
		UserID: userID,
	})
}

func (s *Store) toProjectModel(r db.Project) *Project {
	return &Project{
		SourceType:  r.SourceType,
		Namespace:   s.Namespace(r.UserID),
		ID:          r.ID,
		Slug:        r.Slug,
		UserID:      r.UserID,
		RepoHost:    r.RepoHost,
		AccessToken: r.AccessToken,
		RepoOwner:   r.RepoOwner,
		RepoName:    r.RepoName,
		Branch:      r.Branch,
		Subdir:      r.Subdir,
		Status:      Status(r.Status),
		LastCommit:  r.LastCommit,
		LastError:   r.LastError,
		CreatedAt:   r.CreatedAt,
		UpdatedAt:   r.UpdatedAt,
	}
}

// --- webhook logs queries ------------------------------------------------

// CreateWebhookLog persists a new webhook log entry.
func (s *Store) CreateWebhookLog(l *WebhookLog) error {
	ctx := context.Background()
	if l.ID == "" {
		l.ID = uuid.NewString()
	}
	if l.CreatedAt.IsZero() {
		l.CreatedAt = time.Now().UTC()
	}
	_, err := s.queries.CreateWebhookLog(ctx, db.CreateWebhookLogParams{
		ID:          l.ID,
		ProjectSlug: l.ProjectSlug,
		UserID:      l.UserID,
		Event:       l.Event,
		Delivery:    l.Delivery,
		Signature:   l.Signature,
		Status:      l.Status,
		Message:     l.Message,
		CreatedAt:   l.CreatedAt.UTC(),
	})
	return err
}

// WebhookLogsForProject returns the recent webhook logs for a specific project.
func (s *Store) WebhookLogsForProject(projectSlug string, limit int) ([]*WebhookLog, error) {
	ctx := context.Background()
	if limit <= 0 {
		limit = 10
	}
	rows, err := s.queries.ListWebhookLogsByProjectSlug(ctx, db.ListWebhookLogsByProjectSlugParams{
		ProjectSlug: projectSlug,
		Limit:       int64(limit),
	})
	if err != nil {
		return nil, err
	}

	var out []*WebhookLog
	for _, r := range rows {
		out = append(out, toWebhookLogModel(r))
	}
	return out, nil
}

// WebhookLogsForProjectAndUser returns the recent webhook logs for a project scoped to its owner,
// preventing logs from a deleted project appearing for a new project with the same slug.
func (s *Store) WebhookLogsForProjectAndUser(projectSlug, userID string, limit int) ([]*WebhookLog, error) {
	ctx := context.Background()
	if limit <= 0 {
		limit = 10
	}
	rows, err := s.queries.ListWebhookLogsByProjectSlugAndUserID(ctx, db.ListWebhookLogsByProjectSlugAndUserIDParams{
		ProjectSlug: projectSlug,
		UserID:      userID,
		Limit:       int64(limit),
	})
	if err != nil {
		return nil, err
	}

	var out []*WebhookLog
	for _, r := range rows {
		out = append(out, toWebhookLogModel(r))
	}
	return out, nil
}

// LatestWebhookLogForUser returns the most recent webhook log for any project belonging to a user.
func (s *Store) LatestWebhookLogForUser(userID string) (*WebhookLog, error) {
	ctx := context.Background()
	r, err := s.queries.GetLatestWebhookLogForUser(ctx, userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return toWebhookLogModel(r), nil
}

// LatestWebhookLogForProject returns the most recent webhook log for a project.
func (s *Store) LatestWebhookLogForProject(projectSlug string) (*WebhookLog, error) {
	ctx := context.Background()
	r, err := s.queries.GetLatestWebhookLogForProject(ctx, projectSlug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return toWebhookLogModel(r), nil
}

func toWebhookLogModel(r db.WebhookLog) *WebhookLog {
	return &WebhookLog{
		ID:          r.ID,
		ProjectSlug: r.ProjectSlug,
		UserID:      r.UserID,
		Event:       r.Event,
		Delivery:    r.Delivery,
		Signature:   r.Signature,
		Status:      r.Status,
		Message:     r.Message,
		CreatedAt:   r.CreatedAt,
	}
}

var ErrUsernameTaken = errors.New("username is already taken")
var usernamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// ValidateUsername reserves UUID-shaped names for generated account namespaces.
func ValidateUsername(username string) error {
	if username == "" {
		return nil
	}
	if len(username) > 32 || !usernamePattern.MatchString(username) {
		return errors.New("username must be 1–32 lowercase letters, numbers, or hyphens, starting and ending with a letter or number")
	}
	return nil
}

func (s *Store) SetUsername(userID, username string) error {
	username = strings.ToLower(strings.TrimSpace(username))
	if err := ValidateUsername(username); err != nil {
		return err
	}
	err := s.queries.UpdateUsername(context.Background(), db.UpdateUsernameParams{Username: username, UpdatedAt: time.Now().UTC(), ID: userID})
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed") {
		return ErrUsernameTaken
	}
	return err
}

func (s *Store) Namespace(userID string) string {
	u, err := s.GetUserByID(userID)
	if err == nil && u.Username != "" {
		return u.Username
	}
	return userID
}

// CheckoutKey stays stable when a username changes.
func (p *Project) CheckoutKey() string { return p.UserID + "/" + p.Slug }
func (p *Project) PublicPath() string  { return "/projects/" + p.Namespace + "/" + p.Slug + "/" }

// UserIDForNamespace resolves the current public namespace. Old usernames stop resolving.
func (s *Store) UserIDForNamespace(namespace string) string {
	var id string
	err := s.db.QueryRow(`SELECT id FROM users WHERE (username = ? AND username != '') OR (id = ? AND username = '') LIMIT 1`, namespace, namespace).Scan(&id)
	if err != nil {
		return ""
	}
	return id
}
