package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestLegacyNamespaceMigration(t *testing.T) {
	conn, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx := context.Background()
	if _, err := conn.Exec(`CREATE TABLE schema_migrations (version TEXT PRIMARY KEY, applied_at DATETIME NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	for _, version := range []string{"001_initial", "002_auth", "003_webhook_logs"} {
		content, err := migrationsFS.ReadFile("migrations/" + version + ".sql")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(string(content)); err != nil {
			t.Fatal(err)
		}
		if version == "002_auth" {
			tx, err := conn.Begin()
			if err != nil {
				t.Fatal(err)
			}
			if err := ensureProjectsUserIDColumn(ctx, tx); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := conn.Exec(`INSERT INTO schema_migrations VALUES (?, ?)`, version, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	if _, err := conn.Exec(`INSERT INTO users VALUES ('owner', 'legacy@example.com', 'hash', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO projects (id,slug,user_id,repo_owner,last_commit,created_at,updated_at) VALUES ('legacy-id','app','owner','octocat','abc123',?,?)`, now, now); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := Migrate(ctx, conn); err != nil {
			t.Fatal(err)
		}
	}
	var id, owner, repo, commit, source string
	if err := conn.QueryRow(`SELECT id,user_id,repo_owner,last_commit,source_type FROM projects WHERE slug='app'`).Scan(&id, &owner, &repo, &commit, &source); err != nil {
		t.Fatal(err)
	}
	if source != "git" {
		t.Fatal("legacy project did not default to Git")
	}
	if id != "legacy-id" || owner != "owner" || repo != "octocat" || commit != "abc123" {
		t.Fatal("legacy data changed")
	}
	if _, err := conn.Exec(`INSERT INTO projects (id,slug,user_id,created_at,updated_at) VALUES ('new-id','app','other',?,?)`, now, now); err != nil {
		t.Fatal("cross-user slug rejected:", err)
	}
	if _, err := conn.Exec(`INSERT INTO projects (id,slug,user_id,created_at,updated_at) VALUES ('duplicate','app','owner',?,?)`, now, now); err == nil {
		t.Fatal("same-user duplicate accepted")
	}
}
