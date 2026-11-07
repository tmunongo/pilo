package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"sort"
	"strings"
	"time"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Migrate runs embedded database migrations in sorted order.
func Migrate(ctx context.Context, sqlDB *sql.DB) error {
	_, err := sqlDB.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    TEXT PRIMARY KEY,
			applied_at DATETIME NOT NULL
		);
	`)
	if err != nil {
		return fmt.Errorf("creating schema_migrations table: %w", err)
	}

	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("reading embedded migrations: %w", err)
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		version := strings.TrimSuffix(entry.Name(), ".sql")

		var count int
		err := sqlDB.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations WHERE version = ?`, version).Scan(&count)
		if err != nil {
			return fmt.Errorf("checking migration %s: %w", version, err)
		}
		if count > 0 {
			continue
		}

		content, err := migrationsFS.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return fmt.Errorf("reading migration file %s: %w", entry.Name(), err)
		}

		tx, err := sqlDB.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("beginning tx for migration %s: %w", version, err)
		}

		if _, err := tx.ExecContext(ctx, string(content)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("applying migration %s: %w", version, err)
		}

		// Post-migration hooks for specific versions (e.g. column alteration)
		if version == "002_auth" {
			if err := ensureProjectsUserIDColumn(ctx, tx); err != nil {
				_ = tx.Rollback()
				return fmt.Errorf("post-migration for %s: %w", version, err)
			}
		}

		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`, version, time.Now().UTC()); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("recording migration %s: %w", version, err)
		}

		if err := tx.Commit(); err != nil {
			return fmt.Errorf("committing migration %s: %w", version, err)
		}
	}

	return nil
}

func ensureProjectsUserIDColumn(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `PRAGMA table_info(projects)`)
	if err != nil {
		return err
	}
	defer rows.Close()

	hasUserID := false
	for rows.Next() {
		var cid int
		var name, colType string
		var notNull, pk int
		var dfltValue any
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &pk); err != nil {
			return err
		}
		if strings.EqualFold(name, "user_id") {
			hasUserID = true
			break
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	if !hasUserID {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE projects ADD COLUMN user_id TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_projects_user_id ON projects(user_id)`)
	return err
}

