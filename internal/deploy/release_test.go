package deploy

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExtractionLimitsAndLinks(t *testing.T) {
	for _, tc := range []struct {
		name   string
		header *tar.Header
		want   string
	}{
		{"large", &tar.Header{Name: "huge", Mode: 0644, Size: MaxFileSize + 1, Typeflag: tar.TypeReg}, "limits:"},
		{"symlink", &tar.Header{Name: "leak", Linkname: "/etc/passwd", Typeflag: tar.TypeSymlink}, "symlinks"},
		{"traversal", &tar.Header{Name: "../outside", Typeflag: tar.TypeReg}, "unsafe"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			w := tar.NewWriter(&buf)
			if err := w.WriteHeader(tc.header); err != nil {
				t.Fatal(err)
			}
			err := extract(&buf, t.TempDir())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestPrunePreservesHistoryAndActiveSnapshot(t *testing.T) {
	d := New(t.TempDir())
	const key = "owner/history"
	var oldest string
	for i := 0; i < 12; i++ {
		id := fmt.Sprintf("r_%032x", i)
		if i == 0 {
			oldest = id
		}
		if err := os.MkdirAll(filepath.Join(d.releaseDir(key), id), 0755); err != nil {
			t.Fatal(err)
		}
		meta, err := json.Marshal(releaseMeta{Commit: fmt.Sprintf("%040x", i), Created: time.Unix(int64(i), 0)})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d.releaseDir(key), id+".json"), meta, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.activate(key, oldest); err != nil {
		t.Fatal(err)
	}
	d.prune(key)
	history := d.Commits(key, 0)
	if len(history) != 12 {
		t.Fatalf("expected all 12 history entries after pruning, got %d", len(history))
	}
	for i, entry := range history {
		wantAvailable := i < retainedReleases || entry.ID == oldest
		if entry.CanRollback != wantAvailable {
			t.Errorf("history[%d] availability=%v, want %v", i, entry.CanRollback, wantAvailable)
		}
	}
	before, err := os.Readlink(d.PathFor(key))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Rollback(key, history[6].ID); err == nil {
		t.Fatal("rollback to a pruned snapshot must fail")
	}
	after, err := os.Readlink(d.PathFor(key))
	if err != nil || before != after {
		t.Fatalf("failed rollback changed active release: before=%s after=%s err=%v", before, after, err)
	}
	if _, err := d.Rollback(key, history[0].ID); err != nil {
		t.Fatalf("retained snapshot must remain available for rollback: %v", err)
	}
}

func TestPublishWarningUsesActiveReleaseDirectory(t *testing.T) {
	d := New(t.TempDir())
	const key = "owner/site"
	if warning := d.PublishWarning(key); warning != "" {
		t.Fatalf("undeployed project should have no warning: %s", warning)
	}
	for _, tc := range []struct {
		name, id, subdir string
		index            bool
	}{
		{"root entry page", "r_00000000000000000000000000000001", "", true},
		{"missing publish entry page", "r_00000000000000000000000000000002", "dist", false},
		{"nested entry page", "r_00000000000000000000000000000003", "dist", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := filepath.Join(d.releaseDir(key), tc.id)
			if err := os.MkdirAll(filepath.Join(root, tc.subdir), 0755); err != nil {
				t.Fatal(err)
			}
			// A repository-root entry page must not hide a missing publish entry page.
			if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("root page"), 0644); err != nil {
				t.Fatal(err)
			}
			if tc.index {
				if err := os.WriteFile(filepath.Join(root, tc.subdir, "index.html"), []byte(tc.name), 0644); err != nil {
					t.Fatal(err)
				}
			}
			meta, err := json.Marshal(releaseMeta{Commit: tc.id, Subdir: tc.subdir})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(d.releaseDir(key), tc.id+".json"), meta, 0600); err != nil {
				t.Fatal(err)
			}
			if err := d.activate(key, tc.id); err != nil {
				t.Fatal(err)
			}
			if warning := d.PublishWarning(key); (warning == "") != tc.index {
				t.Fatalf("entry page present=%v, warning=%q", tc.index, warning)
			}
			if tc.index {
				rec := httptest.NewRecorder()
				d.SiteHandler(key).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
				if rec.Code != http.StatusOK || rec.Body.String() != tc.name {
					t.Fatalf("served wrong publish directory: %d %q", rec.Code, rec.Body.String())
				}
			}
		})
	}
	if _, err := d.Rollback(key, "r_00000000000000000000000000000002"); err != nil {
		t.Fatal(err)
	}
	if warning := d.PublishWarning(key); warning == "" {
		t.Fatal("warning should follow the release selected by rollback")
	}
}
