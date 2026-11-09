package deploy

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/google/uuid"
	"pilo/internal/store"
)

var safeSlug = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// MigrateProjects moves legacy repos/<slug> checkouts to repos/<user UUID>/<slug>.
// Run with the server stopped. Renames are atomic, and completed moves are skipped
// on rerun. Conflicting source/destination directories are never overwritten.
func (d *Deployer) MigrateProjects(projects []*store.Project) error {
	type move struct{ source, dest string }
	marker := filepath.Join(d.ReposRoot, ".project-namespaces-v1")
	if _, err := os.Stat(marker); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	var moves []move
	sources := make(map[string]bool)
	for _, p := range projects {
		if _, err := uuid.Parse(p.UserID); err != nil {
			return fmt.Errorf("project %q has no valid owner UUID; assign legacy projects to an account before migration", p.Slug)
		}
		if !safeSlug.MatchString(p.Slug) {
			return fmt.Errorf("unsafe legacy project slug %q", p.Slug)
		}
		source, dest := d.PathFor(p.Slug), d.PathFor(p.CheckoutKey())
		sourceInfo, sourceErr := os.Lstat(source)
		destInfo, destErr := os.Lstat(dest)
		if sourceErr != nil && !os.IsNotExist(sourceErr) {
			return sourceErr
		}
		if destErr != nil && !os.IsNotExist(destErr) {
			return destErr
		}
		if sourceErr == nil && (!sourceInfo.IsDir() || sourceInfo.Mode()&os.ModeSymlink != 0) {
			return fmt.Errorf("legacy checkout is not a directory: %s", source)
		}
		if destErr == nil && (!destInfo.IsDir() || destInfo.Mode()&os.ModeSymlink != 0) {
			return fmt.Errorf("checkout is not a directory: %s", dest)
		}
		if sourceErr == nil && destErr == nil {
			return fmt.Errorf("both legacy and scoped checkout exist for %q; resolve before migration", p.Slug)
		}
		if sourceErr == nil {
			if sources[source] {
				return fmt.Errorf("legacy checkout %s has multiple owners; resolve before migration", source)
			}
			sources[source] = true
			moves = append(moves, move{source, dest})
		}
	}
	for _, m := range moves {
		if err := os.MkdirAll(filepath.Dir(m.dest), 0o755); err != nil {
			return err
		}
		if err := os.Rename(m.source, m.dest); err != nil {
			return fmt.Errorf("moving %s to %s: %w", m.source, m.dest, err)
		}
	}
	if err := os.MkdirAll(d.ReposRoot, 0o755); err != nil {
		return err
	}
	return os.WriteFile(marker, []byte("1\n"), 0o644)
}
