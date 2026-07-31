package store_test

import (
	"path/filepath"
	"testing"

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
