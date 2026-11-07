// Package deploy handles cloning and pulling GitHub repos onto disk.
// We shell out to the real git binary rather than pulling in a Go git
// library — it's one less dependency, and git is already a dependency
// of any homelab box worth its salt.
package deploy

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Deployer clones and updates repos under a root directory on disk.
type Deployer struct {
	ReposRoot string // e.g. /data/repos
}

func New(reposRoot string) *Deployer {
	return &Deployer{ReposRoot: reposRoot}
}

// PathFor returns the local checkout directory for a given project slug.
func (d *Deployer) PathFor(slug string) string {
	return filepath.Join(d.ReposRoot, slug)
}

// authURL injects a GitHub token into an https clone URL for auth,
// e.g. https://github.com/owner/repo.git -> https://<token>@github.com/owner/repo.git
func authURL(repoURL, token string) string {
	if token == "" {
		return repoURL
	}
	return strings.Replace(repoURL, "https://", fmt.Sprintf("https://%s@", token), 1)
}

// Clone clones repoURL at the given branch into PathFor(slug). If the
// directory already exists, it's removed first for a clean clone.
func (d *Deployer) Clone(slug, repoURL, branch, token string) (commit string, err error) {
	dest := d.PathFor(slug)
	if err := os.RemoveAll(dest); err != nil {
		return "", fmt.Errorf("clearing existing checkout: %w", err)
	}
	if err := os.MkdirAll(d.ReposRoot, 0o755); err != nil {
		return "", fmt.Errorf("creating repos root: %w", err)
	}

	args := []string{"clone", "--depth", "1"}
	if branch != "" {
		args = append(args, "--branch", branch)
	}
	args = append(args, authURL(repoURL, token), dest)

	if out, err := runGit("", args...); err != nil {
		return "", fmt.Errorf("git clone failed: %w\n%s", err, out)
	}

	return d.headCommit(dest)
}

// Pull fetches and hard-resets the checkout at PathFor(slug) to the
// latest commit on its tracked branch. Used both for manual "Redeploy"
// and for webhook-triggered updates.
func (d *Deployer) Pull(slug, branch, token string) (commit string, err error) {
	dest := d.PathFor(slug)
	if _, err := os.Stat(dest); err != nil {
		return "", fmt.Errorf("checkout does not exist, clone first: %w", err)
	}

	if out, err := runGit(dest, "fetch", "--depth", "1", "origin", branch); err != nil {
		return "", fmt.Errorf("git fetch failed: %w\n%s", err, out)
	}
	if out, err := runGit(dest, "reset", "--hard", "origin/"+branch); err != nil {
		return "", fmt.Errorf("git reset failed: %w\n%s", err, out)
	}

	return d.headCommit(dest)
}

// Remove deletes a project's local checkout entirely.
func (d *Deployer) Remove(slug string) error {
	return os.RemoveAll(d.PathFor(slug))
}

// CommitInfo represents a single git commit from a project checkout.
type CommitInfo struct {
	SHA     string
	Message string
	When    string
}

// Commits returns the most recent commits for a project checkout.
func (d *Deployer) Commits(slug string, limit int) []CommitInfo {
	dest := d.PathFor(slug)
	if _, err := os.Stat(dest); err != nil {
		return nil
	}
	out, err := runGit(dest, "log", fmt.Sprintf("-n%d", limit), "--format=%h\x1f%s\x1f%cr")
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	var commits []CommitInfo
	for _, l := range lines {
		parts := strings.Split(l, "\x1f")
		if len(parts) >= 3 {
			commits = append(commits, CommitInfo{
				SHA:     parts[0],
				Message: parts[1],
				When:    parts[2],
			})
		}
	}
	return commits
}

func (d *Deployer) headCommit(dir string) (string, error) {
	out, err := runGit(dir, "rev-parse", "--short", "HEAD")
	if err != nil {
		return "", fmt.Errorf("git rev-parse failed: %w", err)
	}
	return strings.TrimSpace(out), nil
}

func runGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}
