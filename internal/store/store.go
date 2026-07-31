// Package store provides a SQLite-backed persistence layer for Pilo.
// Uses modernc.org/sqlite — a pure-Go driver that compiles without CGO,
// so cross-compiling for Linux/ARM64 (Raspberry Pi) is seamless.
package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite" // registers the "sqlite" driver
)

// Status represents the lifecycle state of a deployed project.
type Status string

const (
	StatusCloning Status = "cloning"
	StatusLive    Status = "live"
	StatusError   Status = "error"
)

// Project is one GitHub repo deployed as a static site at /<Slug>/.
type Project struct {
	ID         string    `json:"id"`
	Slug       string    `json:"slug"`       // URL path segment, e.g. "my-app"
	RepoOwner  string    `json:"repo_owner"` // e.g. "octocat"
	RepoName   string    `json:"repo_name"`  // e.g. "my-app"
	Branch     string    `json:"branch"`     // default "main"
	Subdir     string    `json:"subdir"`     // optional, for monorepos
	Status     Status    `json:"status"`
	LastCommit string    `json:"last_commit"`
	LastError  string    `json:"last_error,omitempty"`
	UpdatedAt  time.Time `json:"updated_at"`
	CreatedAt  time.Time `json:"created_at"`
}

// RepoURL returns the https clone URL for this project's GitHub repo.
func (p *Project) RepoURL() string {
	return fmt.Sprintf("https://github.com/%s/%s.git", p.RepoOwner, p.RepoName)
}

// Store holds the SQLite database connection and in-memory cached settings.
// Safe for concurrent use.
type Store struct {
	mu            sync.RWMutex
	db            *sql.DB
	GitHubToken   string
	WebhookSecret string
}

// New opens (or creates) the SQLite database at dbPath, runs schema
// migrations, and loads settings into memory.
func New(dbPath string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, fmt.Errorf("creating db dir: %w", err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("opening sqlite db: %w", err)
	}
	// SQLite only supports one writer at a time; serialise writes in the
	// driver to avoid "database is locked" errors under concurrent requests.
	db.SetMaxOpenConns(1)

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := s.loadSettings(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the underlying database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// --- schema --------------------------------------------------------------

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS projects (
			id          TEXT PRIMARY KEY,
			slug        TEXT UNIQUE NOT NULL,
			repo_owner  TEXT NOT NULL DEFAULT '',
			repo_name   TEXT NOT NULL DEFAULT '',
			branch      TEXT NOT NULL DEFAULT 'main',
			subdir      TEXT NOT NULL DEFAULT '',
			status      TEXT NOT NULL DEFAULT 'cloning',
			last_commit TEXT NOT NULL DEFAULT '',
			last_error  TEXT NOT NULL DEFAULT '',
			created_at  DATETIME NOT NULL,
			updated_at  DATETIME NOT NULL
		);

		CREATE TABLE IF NOT EXISTS settings (
			key   TEXT PRIMARY KEY,
			value TEXT NOT NULL DEFAULT ''
		);
	`)
	if err != nil {
		return fmt.Errorf("running db migrations: %w", err)
	}
	return nil
}

// --- settings ------------------------------------------------------------

// loadSettings reads all key/value settings from the DB into memory.
// Called once at startup; after that the in-memory fields are the source of
// truth (writes go to both).
func (s *Store) loadSettings() error {
	rows, err := s.db.Query(`SELECT key, value FROM settings`)
	if err != nil {
		return fmt.Errorf("loading settings: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return fmt.Errorf("scanning setting row: %w", err)
		}
		switch k {
		case "github_token":
			s.GitHubToken = v
		case "webhook_secret":
			s.WebhookSecret = v
		}
	}
	return rows.Err()
}

func (s *Store) upsertSetting(key, value string) error {
	_, err := s.db.Exec(`
		INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value
	`, key, value)
	return err
}

// SetGitHubToken persists the shared GitHub PAT.
func (s *Store) SetGitHubToken(token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.upsertSetting("github_token", token); err != nil {
		return fmt.Errorf("saving github_token: %w", err)
	}
	s.GitHubToken = token
	return nil
}

// SetWebhookSecret persists the shared webhook HMAC secret.
func (s *Store) SetWebhookSecret(secret string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.upsertSetting("webhook_secret", secret); err != nil {
		return fmt.Errorf("saving webhook_secret: %w", err)
	}
	s.WebhookSecret = secret
	return nil
}

// --- project queries -----------------------------------------------------

// All returns every project, ordered by creation time ascending.
func (s *Store) All() []*Project {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows, err := s.db.Query(`
		SELECT id, slug, repo_owner, repo_name, branch, subdir,
		       status, last_commit, last_error, created_at, updated_at
		FROM projects
		ORDER BY created_at ASC
	`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var out []*Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			continue
		}
		out = append(out, p)
	}
	return out
}

// Get returns the project with the given slug, or nil if not found.
func (s *Store) Get(slug string) *Project {
	s.mu.RLock()
	defer s.mu.RUnlock()

	row := s.db.QueryRow(`
		SELECT id, slug, repo_owner, repo_name, branch, subdir,
		       status, last_commit, last_error, created_at, updated_at
		FROM projects WHERE slug = ?
	`, slug)
	p, err := scanProject(row)
	if err != nil {
		return nil
	}
	return p
}

// Put inserts or updates a project in the database.
func (s *Store) Put(p *Project) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	p.UpdatedAt = now
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now
	}

	_, err := s.db.Exec(`
		INSERT INTO projects
			(id, slug, repo_owner, repo_name, branch, subdir,
			 status, last_commit, last_error, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			slug        = excluded.slug,
			repo_owner  = excluded.repo_owner,
			repo_name   = excluded.repo_name,
			branch      = excluded.branch,
			subdir      = excluded.subdir,
			status      = excluded.status,
			last_commit = excluded.last_commit,
			last_error  = excluded.last_error,
			updated_at  = excluded.updated_at
	`,
		p.ID, p.Slug, p.RepoOwner, p.RepoName, p.Branch, p.Subdir,
		string(p.Status), p.LastCommit, p.LastError,
		p.CreatedAt.UTC(), p.UpdatedAt.UTC(),
	)
	if err != nil {
		return fmt.Errorf("upserting project %s: %w", p.Slug, err)
	}
	return nil
}

// Delete removes a project by slug.
func (s *Store) Delete(slug string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`DELETE FROM projects WHERE slug = ?`, slug)
	if err != nil {
		return fmt.Errorf("deleting project %s: %w", slug, err)
	}
	return nil
}

// --- helpers -------------------------------------------------------------

// scanner is the common interface satisfied by both *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

func scanProject(s scanner) (*Project, error) {
	var p Project
	var status string
	var createdAt, updatedAt time.Time
	err := s.Scan(
		&p.ID, &p.Slug, &p.RepoOwner, &p.RepoName, &p.Branch, &p.Subdir,
		&status, &p.LastCommit, &p.LastError, &createdAt, &updatedAt,
	)
	if err != nil {
		return nil, err
	}
	p.Status = Status(status)
	p.CreatedAt = createdAt
	p.UpdatedAt = updatedAt
	return &p, nil
}
