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
	if len(commits) == 0 {
		t.Errorf("expected commits list from deployer log")
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
