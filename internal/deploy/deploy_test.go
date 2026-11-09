package deploy_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"pilo/internal/deploy"
)

func TestDeployerBasics(t *testing.T) {
	d := deploy.New("/data/repos")
	if d.PathFor("my-app") != filepath.Join("/data/repos", "my-app") {
		t.Errorf("unexpected path for slug: %s", d.PathFor("my-app"))
	}
}

func TestDeployerGitOperations(t *testing.T) {
	tmpDir := t.TempDir()
	originDir := filepath.Join(tmpDir, "origin")
	reposRoot := filepath.Join(tmpDir, "repos")

	// 1. Initialize a real local git repository
	if err := os.MkdirAll(originDir, 0o755); err != nil {
		t.Fatalf("failed to create origin dir: %v", err)
	}

	runCmd := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}

	runCmd(originDir, "init", "-b", "main")
	runCmd(originDir, "config", "user.name", "Tester")
	runCmd(originDir, "config", "user.email", "test@example.com")

	// First commit
	if err := os.WriteFile(filepath.Join(originDir, "index.html"), []byte("<h1>Hello V1</h1>"), 0o644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}
	runCmd(originDir, "add", "index.html")
	runCmd(originDir, "commit", "-m", "first commit")

	// 2. Clone using Deployer
	d := deploy.New(reposRoot)
	commit1, err := d.Clone("my-app", originDir, "main", "")
	if err != nil {
		t.Fatalf("Deployer.Clone failed: %v", err)
	}
	if commit1 == "" {
		t.Errorf("expected non-empty commit hash")
	}

	// Verify cloned file exists
	content, err := os.ReadFile(filepath.Join(d.PathFor("my-app"), "index.html"))
	if err != nil || string(content) != "<h1>Hello V1</h1>" {
		t.Errorf("cloned content mismatch: %v, %s", err, string(content))
	}

	// 3. Make second commit in origin
	if err := os.WriteFile(filepath.Join(originDir, "index.html"), []byte("<h1>Hello V2</h1>"), 0o644); err != nil {
		t.Fatalf("failed to update file: %v", err)
	}
	runCmd(originDir, "add", "index.html")
	runCmd(originDir, "commit", "-m", "second commit")

	// 4. Pull using Deployer
	commit2, err := d.Pull("my-app", "main", "")
	if err != nil {
		t.Fatalf("Deployer.Pull failed: %v", err)
	}
	if commit2 == commit1 {
		t.Errorf("expected commit to update on pull")
	}

	// Verify updated content
	content2, err := os.ReadFile(filepath.Join(d.PathFor("my-app"), "index.html"))
	if err != nil || string(content2) != "<h1>Hello V2</h1>" {
		t.Errorf("pulled content mismatch: %v, %s", err, string(content2))
	}

	// 5. Commits history log
	commits := d.Commits("my-app", 5)
	if len(commits) != 2 || commits[0].Message != "second commit" || commits[1].Message != "first commit" {
		t.Fatalf("unexpected deploy history: %+v", commits)
	}

	// 6. Non-existent slug commits returns nil
	if d.Commits("non-existent-app", 5) != nil {
		t.Errorf("expected nil commits for missing slug")
	}

	// 7. Pull non-existent slug returns error
	if _, err := d.Pull("non-existent-app", "main", ""); err == nil {
		t.Errorf("expected error pulling non-existent repo")
	}

	// 8. Remove checkout
	if err := d.Remove("my-app"); err != nil {
		t.Fatalf("Deployer.Remove failed: %v", err)
	}
	if _, err := os.Stat(d.PathFor("my-app")); !os.IsNotExist(err) {
		t.Errorf("expected checkout dir to be removed")
	}
}

func TestRejectSymlinkPreservesLiveReleaseAndRollback(t *testing.T) {
	root := t.TempDir()
	origin := filepath.Join(root, "origin")
	if err := os.Mkdir(origin, 0755); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = origin
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git: %v: %s", err, out)
		}
	}
	git("init", "-b", "main")
	git("config", "user.name", "Test")
	git("config", "user.email", "test@example.com")
	file := filepath.Join(origin, "index.html")
	if err := os.WriteFile(file, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-m", "old")
	d := deploy.New(filepath.Join(root, "repos"))
	if _, err := d.Clone("site", origin, "main", ""); err != nil {
		t.Fatal(err)
	}
	releases := d.Commits("site", 5)
	if len(releases) != 1 {
		t.Fatal(releases)
	}
	if _, err := os.Stat(filepath.Join(d.PathFor("site"), ".git")); !os.IsNotExist(err) {
		t.Fatal("git metadata is served")
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(origin, "leak")); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-m", "unsafe")
	if _, err := d.Clone("site", origin, "main", ""); err == nil {
		t.Fatal("symlink release accepted")
	}
	b, err := os.ReadFile(filepath.Join(d.PathFor("site"), "index.html"))
	if err != nil || string(b) != "old" {
		t.Fatalf("live release changed: %q %v", b, err)
	}
	if err := os.Remove(filepath.Join(origin, "leak")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("new"), 0644); err != nil {
		t.Fatal(err)
	}
	git("add", "-A")
	git("commit", "-m", "new")
	if _, err := d.Clone("site", origin, "main", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Rollback("site", releases[0].ID); err != nil {
		t.Fatal(err)
	}
	b, err = os.ReadFile(filepath.Join(d.PathFor("site"), "index.html"))
	if err != nil || string(b) != "old" {
		t.Fatalf("rollback failed: %q %v", b, err)
	}
	if _, err := d.Rollback("site", "../../origin"); err == nil {
		t.Fatal("invalid release ID accepted")
	}
}
