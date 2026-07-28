// Package store provides a tiny JSON-file-backed persistence layer for
// Pilo's project metadata. No database dependency — a home lab doesn't
// need one, and it keeps the binary dependency-free and easy to
// cross-compile for ARM.
package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
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

// Store is a simple in-memory map of projects, persisted to a JSON file
// on every write. Safe for concurrent use.
type Store struct {
	mu       sync.RWMutex
	path     string
	Projects map[string]*Project `json:"projects"` // keyed by slug

	// GitHubToken is a single, shared PAT used for cloning/pulling private
	// repos. Kept separate from individual projects since one token
	// typically covers everything for a personal homelab setup.
	GitHubToken string `json:"github_token"`

	// WebhookSecret is used to verify incoming GitHub webhook signatures.
	WebhookSecret string `json:"webhook_secret"`
}

// New creates a Store backed by the JSON file at path, loading existing
// data if present.
func New(path string) (*Store, error) {
	s := &Store{
		path:     path,
		Projects: make(map[string]*Project),
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) load() error {
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return nil // fresh store, nothing to load
	}
	if err != nil {
		return fmt.Errorf("reading store file: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return json.Unmarshal(data, s)
}

// save must be called with s.mu held (read or write lock does NOT protect
// the marshal itself against concurrent writers, so callers use saveLocked
// from within a write-locked section).
func (s *Store) saveLocked() error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling store: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("creating store dir: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("writing store file: %w", err)
	}
	return os.Rename(tmp, s.path)
}

// All returns a snapshot slice of all projects.
func (s *Store) All() []*Project {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Project, 0, len(s.Projects))
	for _, p := range s.Projects {
		out = append(out, p)
	}
	return out
}

// Get returns the project with the given slug, or nil.
func (s *Store) Get(slug string) *Project {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Projects[slug]
}

// Put inserts or updates a project and persists the store.
func (s *Store) Put(p *Project) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p.UpdatedAt = time.Now()
	s.Projects[p.Slug] = p
	return s.saveLocked()
}

// Delete removes a project by slug and persists the store.
func (s *Store) Delete(slug string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.Projects, slug)
	return s.saveLocked()
}

// SetGitHubToken updates and persists the shared GitHub PAT.
func (s *Store) SetGitHubToken(token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.GitHubToken = token
	return s.saveLocked()
}

// SetWebhookSecret updates and persists the shared webhook secret.
func (s *Store) SetWebhookSecret(secret string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.WebhookSecret = secret
	return s.saveLocked()
}
