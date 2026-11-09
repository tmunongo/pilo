package deploy_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"pilo/internal/deploy"
	"pilo/internal/store"
)

func TestMigrateProjects(t *testing.T) {
	d := deploy.New(t.TempDir())
	p := &store.Project{ID: "legacy-id", UserID: uuid.NewString(), Slug: "app"}
	source := d.PathFor(p.Slug)
	if err := os.MkdirAll(filepath.Join(source, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "index.html"), []byte("legacy content"), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := d.MigrateProjects([]*store.Project{p}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Fatal("legacy checkout still present")
	}
	content, err := os.ReadFile(filepath.Join(d.PathFor(p.CheckoutKey()), "index.html"))
	if err != nil || string(content) != "legacy content" {
		t.Fatalf("content not preserved: %s %v", content, err)
	}
	if _, err := os.Stat(filepath.Join(d.PathFor(p.CheckoutKey()), ".git")); err != nil {
		t.Fatal("git directory not preserved")
	}
	// A fresh migration must reject ambiguous source/destination pairs.
	d = deploy.New(t.TempDir())
	source = d.PathFor(p.Slug)
	if err := os.MkdirAll(d.PathFor(p.CheckoutKey()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(source, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := d.MigrateProjects([]*store.Project{p}); err == nil {
		t.Fatal("conflicting checkout overwritten")
	}
}

func TestMigrateProjectsPreflight(t *testing.T) {
	d := deploy.New(t.TempDir())
	valid := &store.Project{UserID: uuid.NewString(), Slug: "valid"}
	if err := os.Mkdir(d.PathFor(valid.Slug), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []*store.Project{
		{Slug: "unowned"},
		{UserID: uuid.NewString(), Slug: "../escape"},
	} {
		if err := d.MigrateProjects([]*store.Project{valid, bad}); err == nil {
			t.Fatal("invalid migration accepted")
		}
		if _, err := os.Stat(d.PathFor(valid.Slug)); err != nil {
			t.Fatal("preflight failure moved a checkout")
		}
	}
}
