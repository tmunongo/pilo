package store_test

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"pilo/internal/store"
)

func TestUsernameNamespaces(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	st, err := store.New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	u1, err := st.CreateUser("one@example.com", "password123")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := uuid.Parse(st.Namespace(u1.ID)); err != nil {
		t.Fatal("default namespace is not a UUID")
	}
	u2, err := st.CreateUser("two@example.com", "password123", "  ALICE  ")
	if err != nil {
		t.Fatal(err)
	}
	if u2.Username != "alice" {
		t.Fatal("username not normalized")
	}
	if err := st.SetUsername(u1.ID, "Alice"); !errors.Is(err, store.ErrUsernameTaken) {
		t.Fatalf("expected username conflict, got %v", err)
	}
	if _, err := st.CreateUser("three@example.com", "password123", "alice"); !errors.Is(err, store.ErrUsernameTaken) {
		t.Fatalf("expected signup conflict, got %v", err)
	}
	if _, err := st.GetUserByEmail("three@example.com"); !errors.Is(err, store.ErrUserNotFound) {
		t.Fatal("failed signup left an account")
	}
	for _, name := range []string{"../alice", "a/b", "-alice", "alice-", "a_b", u1.ID} {
		if err := st.SetUsername(u1.ID, name); err == nil {
			t.Fatalf("accepted invalid username %q", name)
		}
	}
	for _, u := range []*store.User{u1, u2} {
		if err := st.Put(&store.Project{UserID: u.ID, Slug: "app"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Put(&store.Project{UserID: u1.ID, Slug: "app"}); err == nil {
		t.Fatal("same-user duplicate accepted")
	}
	p1, p2 := st.GetForUser("app", u1.ID), st.GetForUser("app", u2.ID)
	if p1.ID == p2.ID || p1.CheckoutKey() == p2.CheckoutKey() {
		t.Fatal("projects collided")
	}
	if p2.PublicPath() != "/projects/alice/app/" {
		t.Fatalf("wrong public path %s", p2.PublicPath())
	}
	if err := st.SetUsername(u2.ID, "bob"); err != nil {
		t.Fatal(err)
	}
	if st.UserIDForNamespace("alice") != "" || st.UserIDForNamespace("bob") != u2.ID {
		t.Fatal("username routing not updated")
	}
	if st.GetForUser("app", u2.ID).CheckoutKey() != p2.CheckoutKey() {
		t.Fatal("username changed checkout key")
	}
	if err := st.SetUsername(u2.ID, ""); err != nil {
		t.Fatal(err)
	}
	if st.Namespace(u2.ID) != u2.ID || st.UserIDForNamespace(u2.ID) != u2.ID {
		t.Fatal("clearing username did not restore UUID")
	}
	if err := st.DeleteForUser("app", u1.ID); err != nil {
		t.Fatal(err)
	}
	if st.GetForUser("app", u2.ID) == nil {
		t.Fatal("delete affected another user")
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.GetForUser("app", u2.ID) == nil {
		t.Fatal("project not persisted")
	}
}
