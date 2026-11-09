package store_test

import (
	"path/filepath"
	"pilo/internal/store"
	"testing"
)

func TestProjectCredentialsPersistAndFallback(t *testing.T) {
	st, err := store.New(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetGitHubToken("default-token"); err != nil {
		t.Fatal(err)
	}
	p := &store.Project{Slug: "site", RepoHost: "codeberg.org", RepoOwner: "owner", RepoName: "site", AccessToken: "project-token"}
	if err := st.Put(p); err != nil {
		t.Fatal(err)
	}
	got := st.Get("site")
	if got == nil || got.RepoURL() != "https://codeberg.org/owner/site.git" || st.ProjectToken(got) != "project-token" {
		t.Fatalf("project credentials did not persist: %#v", got)
	}
	got.AccessToken = ""
	if err := st.Put(got); err != nil {
		t.Fatal(err)
	}
	if got = st.Get("site"); st.ProjectToken(got) != "default-token" {
		t.Fatal("default token was not used")
	}
}
