package deploy

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"pilo/internal/store"
)

// MigrateReleases converts existing checkouts while the service is stopped.
func (d *Deployer) MigrateReleases(projects []*store.Project) error {
	for _, p := range projects {
		live := d.PathFor(p.CheckoutKey())
		root := d.releaseDir(p.CheckoutKey())
		entries, err := os.ReadDir(root)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".stage-") {
				if err := os.RemoveAll(filepath.Join(root, entry.Name())); err != nil {
					return err
				}
			}
		}
		backup := filepath.Join(root, ".legacy-checkout")
		if _, err := os.Lstat(live); os.IsNotExist(err) {
			if _, e := os.Lstat(backup); e == nil {
				if e = os.Rename(backup, live); e != nil {
					return e
				}
			}
		}
		info, err := os.Lstat(live)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if err := os.RemoveAll(backup); err != nil {
				return err
			}
			continue
		}
		if !info.IsDir() {
			return fmt.Errorf("invalid checkout %s", live)
		}
		if err := ValidateSubdir(p.Subdir); err != nil {
			return fmt.Errorf("project %s: %w", p.Slug, err)
		}
		if err := os.MkdirAll(root, 0755); err != nil {
			return err
		}
		if _, err := os.Lstat(backup); err == nil {
			return fmt.Errorf("legacy backup exists: %s", backup)
		} else if !os.IsNotExist(err) {
			return err
		}
		if err := os.Rename(live, backup); err != nil {
			return err
		}
		if _, err := d.publish(p.CheckoutKey(), backup, "HEAD", p.Subdir); err != nil {
			if restoreErr := os.Rename(backup, live); restoreErr != nil {
				return fmt.Errorf("migration failed: %v; restore failed: %w", err, restoreErr)
			}
			return fmt.Errorf("migrate project %s: %w", p.Slug, err)
		}
		if err := os.RemoveAll(backup); err != nil {
			return err
		}
	}
	return nil
}
