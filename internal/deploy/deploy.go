// Package deploy publishes validated, immutable snapshots of Git repositories.
package deploy

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const MaxFileSize int64 = 50 << 20
const MaxTotalSize int64 = 500 << 20
const MaxFiles = 10000
const retainedReleases = 5

type Deployer struct {
	ReposRoot string
	mu        sync.Mutex
	locks     map[string]*sync.Mutex
}

func New(root string) *Deployer {
	return &Deployer{ReposRoot: root, locks: make(map[string]*sync.Mutex)}
}
func (d *Deployer) PathFor(key string) string    { return filepath.Join(d.ReposRoot, key) }
func (d *Deployer) releaseDir(key string) string { return filepath.Join(d.ReposRoot, ".releases", key) }
func (d *Deployer) Lock(key string) func() {
	d.mu.Lock()
	l := d.locks[key]
	if l == nil {
		l = &sync.Mutex{}
		d.locks[key] = l
	}
	d.mu.Unlock()
	l.Lock()
	return l.Unlock
}
func ValidateSubdir(s string) error {
	if s == "" {
		return nil
	}
	if strings.Contains(s, "\\") || path.IsAbs(s) || path.Clean(s) != s || s == ".." || strings.HasPrefix(s, "../") || s == "." {
		return fmt.Errorf("subdirectory must be a relative path inside the project")
	}
	return nil
}
func authURL(u, token string) string {
	if token == "" {
		return u
	}
	parsed, err := url.Parse(u)
	if err != nil || parsed.Scheme != "https" {
		return u
	}
	parsed.User = url.UserPassword("oauth2", token)
	return parsed.String()
}
func runGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var b bytes.Buffer
	cmd.Stdout = &b
	cmd.Stderr = &b
	err := cmd.Run()
	return b.String(), err
}

type CommitInfo struct{ SHA, Message, When, ID string }

var releaseID = regexp.MustCompile(`^r_[0-9a-f]{32}$`)

type releaseMeta struct {
	Commit  string
	Created time.Time
	Message string
	Subdir  string
}

// Clone prepares and activates a fresh release. Callers serialize related project operations.
func (d *Deployer) Clone(key, repoURL, branch, token string) (string, error) {
	return d.CloneWithSubdir(key, repoURL, branch, token, "")
}

func (d *Deployer) CloneWithSubdir(key, repoURL, branch, token, subdir string) (string, error) {
	if err := ValidateSubdir(subdir); err != nil {
		return "", err
	}
	root := d.releaseDir(key)
	if err := os.MkdirAll(root, 0755); err != nil {
		return "", err
	}
	stage, err := os.MkdirTemp(root, ".stage-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)
	args := []string{"clone", "--no-checkout", "--depth", "1"}
	if branch != "" {
		args = append(args, "--branch", branch)
	}
	args = append(args, authURL(repoURL, token), filepath.Join(stage, "git"))
	out, err := runGit("", args...)
	if err != nil {
		if token != "" {
			out = strings.ReplaceAll(out, token, "[redacted]")
			out = strings.ReplaceAll(out, url.QueryEscape(token), "[redacted]")
			out = strings.ReplaceAll(out, url.UserPassword("oauth2", token).String(), "[redacted]")
		}
		return "", fmt.Errorf("git clone failed: %w: %s", err, out)
	}
	commit, err := d.publish(key, filepath.Join(stage, "git"), "HEAD", subdir)
	if err == nil {
		_ = os.WriteFile(filepath.Join(root, "origin"), []byte(repoURL), 0600)
	}
	return commit, err
}

func (d *Deployer) publish(key, repo, ref, subdir string) (string, error) {
	commit, err := runGit(repo, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("resolve commit: %w", err)
	}
	commit = strings.TrimSpace(commit)
	message, err := runGit(repo, "log", "-1", "--format=%s", commit)
	if err != nil {
		return "", fmt.Errorf("read commit message: %w", err)
	}
	message = strings.TrimSpace(message)
	id := "r_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	root := d.releaseDir(key)
	dest := filepath.Join(root, id)
	if err = os.Mkdir(dest, 0755); err != nil {
		return "", err
	}
	success := false
	defer func() {
		if !success {
			os.RemoveAll(dest)
		}
	}()
	cmd := exec.Command("git", "archive", "--format=tar", commit)
	cmd.Dir = repo
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err = cmd.Start(); err != nil {
		return "", err
	}
	err = extract(pipe, dest)
	if err != nil {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if err != nil {
		return "", err
	}
	if waitErr != nil {
		return "", fmt.Errorf("git archive: %w: %s", waitErr, stderr.String())
	}
	info, err := os.Stat(filepath.Join(dest, subdir))
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("site subdirectory %q does not exist in release", subdir)
	}
	meta := releaseMeta{Commit: commit, Message: message, Created: time.Now().UTC(), Subdir: subdir}
	b, _ := json.Marshal(meta)
	if err = os.WriteFile(filepath.Join(root, id+".json"), b, 0600); err != nil {
		return "", err
	}
	if err = d.activate(key, id); err != nil {
		os.Remove(filepath.Join(root, id+".json"))
		return "", err
	}
	success = true
	d.prune(key)
	return commit, nil
}
func extract(r io.Reader, dest string) error {
	tr := tar.NewReader(r)
	var total int64
	count := 0
	limits := fmt.Sprintf("limits: %d bytes per file, %d bytes total, %d files", MaxFileSize, MaxTotalSize, MaxFiles)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("invalid archive: %w", err)
		}
		name := strings.TrimSuffix(h.Name, "/")
		if name == "" || path.IsAbs(name) || path.Clean(name) != name || name == ".." || strings.HasPrefix(name, "../") || strings.Contains(name, "\\") {
			return fmt.Errorf("unsafe archive path %q", h.Name)
		}
		target := filepath.Join(dest, filepath.FromSlash(name))
		switch h.Typeflag {
		case tar.TypeXGlobalHeader:
			continue
		case tar.TypeDir:
			if err = os.MkdirAll(target, 0755); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			count++
			if h.Size < 0 || h.Size > MaxFileSize || count > MaxFiles || h.Size > MaxTotalSize-total {
				return fmt.Errorf("release exceeds %s (file %q: %d bytes; accumulated: %d bytes, %d files)", limits, name, h.Size, total, count)
			}
			total += h.Size
			if err = os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			f, e := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
			if e != nil {
				return e
			}
			_, e = io.CopyN(f, tr, h.Size)
			closeErr := f.Close()
			if e != nil {
				return e
			}
			if closeErr != nil {
				return closeErr
			}
		default:
			return fmt.Errorf("release contains unsupported entry %q (symlinks and special files are prohibited)", name)
		}
	}
}
func (d *Deployer) activate(key, id string) error {
	if !releaseID.MatchString(id) {
		return fmt.Errorf("invalid release ID")
	}
	if _, err := os.Stat(filepath.Join(d.releaseDir(key), id+".json")); err != nil {
		return fmt.Errorf("release not found: %w", err)
	}
	live := d.PathFor(key)
	if err := os.MkdirAll(filepath.Dir(live), 0755); err != nil {
		return err
	}
	target, err := filepath.Rel(filepath.Dir(live), filepath.Join(d.releaseDir(key), id))
	if err != nil {
		return err
	}
	temp := live + ".tmp-" + uuid.NewString()
	if err = os.Symlink(target, temp); err != nil {
		return err
	}
	defer os.Remove(temp)
	return os.Rename(temp, live)
}
func (d *Deployer) Rollback(key, id string) (string, error) {
	if !releaseID.MatchString(id) {
		return "", fmt.Errorf("invalid release ID")
	}
	b, err := os.ReadFile(filepath.Join(d.releaseDir(key), id+".json"))
	if err != nil {
		return "", err
	}
	var m releaseMeta
	if err = json.Unmarshal(b, &m); err != nil {
		return "", err
	}
	if err = d.activate(key, id); err != nil {
		return "", err
	}
	return m.Commit, nil
}
func (d *Deployer) Commits(key string, limit int) []CommitInfo {
	entries, err := os.ReadDir(d.releaseDir(key))
	if err != nil {
		return nil
	}
	var list []struct {
		c CommitInfo
		t time.Time
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), "r_") || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(d.releaseDir(key), e.Name()))
		if err != nil {
			continue
		}
		var m releaseMeta
		if json.Unmarshal(b, &m) != nil {
			continue
		}
		sha := m.Commit
		if len(sha) > 7 {
			sha = sha[:7]
		}
		list = append(list, struct {
			c CommitInfo
			t time.Time
		}{CommitInfo{SHA: sha, Message: m.Message, When: m.Created.Format(time.RFC3339), ID: strings.TrimSuffix(e.Name(), ".json")}, m.Created})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].t.After(list[j].t) })
	var out []CommitInfo
	for i, v := range list {
		if i >= limit {
			break
		}
		out = append(out, v.c)
	}
	return out
}
func (d *Deployer) prune(key string) {
	entries := d.Commits(key, 1000000)
	active, _ := os.Readlink(d.PathFor(key))
	for i, e := range entries {
		if i < retainedReleases || filepath.Base(active) == e.ID {
			continue
		}
		os.RemoveAll(filepath.Join(d.releaseDir(key), e.ID))
		os.Remove(filepath.Join(d.releaseDir(key), e.ID+".json"))
	}
}
func (d *Deployer) Remove(key string) error {
	if err := os.RemoveAll(d.PathFor(key)); err != nil {
		return err
	}
	return os.RemoveAll(d.releaseDir(key))
}

// Pull is retained for legacy local callers; server deployments use Clone with the configured URL.
func (d *Deployer) Pull(key, branch, token string) (string, error) {
	b, err := os.ReadFile(filepath.Join(d.releaseDir(key), "origin"))
	if err != nil {
		return "", err
	}
	return d.Clone(key, string(b), branch, token)
}

// SiteHandler resolves the active snapshot and its associated subdirectory per request.
func (d *Deployer) SiteHandler(key string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target, err := os.Readlink(d.PathFor(key))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		id := filepath.Base(target)
		if !releaseID.MatchString(id) {
			http.NotFound(w, r)
			return
		}
		b, err := os.ReadFile(filepath.Join(d.releaseDir(key), id+".json"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		var m releaseMeta
		if json.Unmarshal(b, &m) != nil || ValidateSubdir(m.Subdir) != nil {
			http.NotFound(w, r)
			return
		}
		http.FileServer(http.Dir(filepath.Join(d.releaseDir(key), id, m.Subdir))).ServeHTTP(w, r)
	})
}
