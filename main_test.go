package main

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	"pilo/internal/deploy"
	"pilo/internal/store"
)

func TestRecoverInterruptedUploadPublication(t *testing.T) {
	dir := t.TempDir()
	st, err := store.New(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	dep := deploy.New(filepath.Join(dir, "repos"))
	user, err := st.CreateUser("recover@example.com", "password123")
	if err != nil {
		t.Fatal(err)
	}
	p := &store.Project{Slug: "recover", UserID: user.ID, SourceType: "upload", Status: store.StatusCloning}
	if err = st.Put(p); err != nil {
		t.Fatal(err)
	}
	raw := []byte("<h1>published</h1>")
	upload, err := dep.PrepareUpload(context.Background(), p.CheckoutKey(), "page.html", "", bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	defer upload.Close()
	digest, err := upload.Publish()
	if err != nil {
		t.Fatal(err)
	}
	// Simulate stopping after pointer activation but before the database update.
	recoverUploadProjects(st, dep)
	recovered := st.GetForUser(p.Slug, user.ID)
	if recovered.LastCommit != digest || recovered.Status != store.StatusLive || recovered.ID != p.ID {
		t.Fatalf("incorrect recovery: %+v", recovered)
	}
	recoverUploadProjects(st, dep)
	if !st.GetForUser(p.Slug, user.ID).UpdatedAt.Equal(recovered.UpdatedAt) {
		t.Fatal("recovery should be idempotent")
	}
	pending := &store.Project{Slug: "interrupted", UserID: user.ID, SourceType: "upload", Status: store.StatusCloning}
	if err = st.Put(pending); err != nil {
		t.Fatal(err)
	}
	recoverUploadProjects(st, dep)
	recovered = st.GetForUser(pending.Slug, user.ID)
	if recovered.Status != store.StatusError || recovered.LastError == "" {
		t.Fatalf("interrupted upload left pending: %+v", recovered)
	}
}
