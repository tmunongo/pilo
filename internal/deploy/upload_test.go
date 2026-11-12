package deploy

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type zipEntry struct {
	name, body string
	mode       os.FileMode
}

func makeZIP(t *testing.T, entries ...zipEntry) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Store}
		if e.mode != 0 {
			h.SetMode(e.mode)
		}
		f, err := w.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = io.WriteString(f, e.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func TestUploadedReleaseLifecycle(t *testing.T) {
	d := New(t.TempDir())
	key := "owner/site"
	prepare := func(filename, subdir string, content []byte) *Upload {
		t.Helper()
		u, err := d.PrepareUpload(context.Background(), key, filename, subdir, bytes.NewReader(content), int64(len(content)))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(u.Close)
		return u
	}
	first := prepare("hello.html", "", []byte("<h1>old</h1>"))
	if _, err := os.Readlink(d.PathFor(key)); !os.IsNotExist(err) {
		t.Fatal("preparation activated a release")
	}
	digest, err := first.Publish()
	if err != nil || len(digest) != 64 {
		t.Fatal(digest, err)
	}
	old := d.Commits(key, 0)[0]
	second := prepare("new.zip", "", makeZIP(t, zipEntry{"site/index.html", "<h1>new</h1>", 0}, zipEntry{"site/a.css", "css", 0}))
	if second.Subdir() != "site" {
		t.Fatal(second.Subdir())
	}
	rec := httptest.NewRecorder()
	d.SiteHandler(key).ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Body.String() != "<h1>old</h1>" {
		t.Fatal("preparation changed live site")
	}
	if _, err = second.Publish(); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	d.SiteHandler(key).ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Body.String() != "<h1>new</h1>" {
		t.Fatal(rec.Body.String())
	}
	if _, err := d.Rollback(key, old.ID); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	d.SiteHandler(key).ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Body.String() != "<h1>old</h1>" {
		t.Fatal("rollback failed")
	}
	if _, err := os.Stat(filepath.Join(d.releaseDir(key), old.ID, "index.html")); err != nil {
		t.Fatal("old snapshot was mutated", err)
	}
}

func TestZIPValidationAndCleanup(t *testing.T) {
	good := zipEntry{"index.html", "<h1>hello</h1>", 0}
	cases := []struct {
		name    string
		entries []zipEntry
		want    string
	}{
		{"traversal", []zipEntry{good, {"../escape", "bad", 0}}, "unsafe"},
		{"absolute", []zipEntry{good, {"/escape", "bad", 0}}, "unsafe"},
		{"windows", []zipEntry{good, {"C:/escape", "bad", 0}}, "unsafe"},
		{"backslash", []zipEntry{good, {`dir\escape`, "bad", 0}}, "unsafe"},
		{"unclean", []zipEntry{good, {"a/../escape", "bad", 0}}, "unsafe"},
		{"symlink", []zipEntry{good, {"link", "/etc/passwd", os.ModeSymlink | 0777}}, "symlink"},
		{"special", []zipEntry{good, {"pipe", "", os.ModeNamedPipe | 0600}}, "special"},
		{"duplicate", []zipEntry{good, good}, "duplicate"},
		{"case duplicate", []zipEntry{good, {"INDEX.html", "bad", 0}}, "duplicate"},
		{"collision", []zipEntry{good, {"file", "a", 0}, {"file/nested", "b", 0}}, "collision"},
		{"reverse collision", []zipEntry{good, {"dir/nested", "b", 0}, {"dir", "a", 0}}, "conflicting"},
		{"metadata", []zipEntry{good, {".git/config", "secret", 0}}, "metadata"},
		{"no entry page", []zipEntry{{"readme.txt", "hi", 0}}, "no index.html"},
		{"suggest publish directory", []zipEntry{{"app/dist/index.html", "hi", 0}, {"README", "docs", 0}}, "app/dist"},
		{"empty", nil, "empty"},
		{"empty entry page", []zipEntry{{"index.html", "", 0}}, "empty"},
		{"case prefix", []zipEntry{good, {"A/one", "1", 0}, {"a/two", "2", 0}}, "ambiguous case"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := New(t.TempDir())
			key := "owner/site"
			raw := makeZIP(t, tc.entries...)
			u, err := d.PrepareUpload(context.Background(), key, "site.zip", "", bytes.NewReader(raw), int64(len(raw)))
			if u != nil {
				u.Close()
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
			entries, _ := os.ReadDir(d.releaseDir(key))
			if len(entries) != 0 {
				t.Fatal("failed upload left staged files", entries)
			}
			if _, err := os.Readlink(d.PathFor(key)); !os.IsNotExist(err) {
				t.Fatal("invalid upload was activated")
			}
		})
	}
}

func TestZIPCorruptionAndForgedMetadata(t *testing.T) {
	good := makeZIP(t, zipEntry{"index.html", "unique content", 0})
	central := bytes.Index(good, []byte{'P', 'K', 1, 2})
	cases := []struct {
		name, want string
		modify     func([]byte) []byte
	}{
		{"checksum", "checksum", func(b []byte) []byte { b[bytes.Index(b, []byte("unique content"))] ^= 1; return b }},
		{"truncated", "invalid", func(b []byte) []byte { return b[:len(b)-1] }},
		{"encrypted", "encrypted", func(b []byte) []byte { b[central+8] |= 1; return b }},
		{"unsupported compression", "unsupported", func(b []byte) []byte { binary.LittleEndian.PutUint16(b[central+10:], 99); return b }},
		{"oversized", "limits", func(b []byte) []byte { binary.LittleEndian.PutUint32(b[central+24:], uint32(MaxFileSize+1)); return b }},
		{"forged size", "invalid", func(b []byte) []byte { binary.LittleEndian.PutUint32(b[central+24:], 1); return b }},
		{"forged count", "count", func(b []byte) []byte {
			binary.LittleEndian.PutUint16(b[len(b)-12:], 2)
			binary.LittleEndian.PutUint16(b[len(b)-14:], 2)
			return b
		}},
		{"ZIP64", "ZIP64", func(b []byte) []byte { binary.LittleEndian.PutUint32(b[len(b)-6:], 0xffffffff); return b }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := tc.modify(bytes.Clone(good))
			d := New(t.TempDir())
			_, err := d.PrepareUpload(context.Background(), "owner/site", "site.zip", "", bytes.NewReader(b), int64(len(b)))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %s", err, tc.want)
			}
		})
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }
func TestZIPBombAndEntryLimit(t *testing.T) {
	// A tiny compressed upload that expands beyond the per-file limit.
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	f, err := w.Create("index.html")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.CopyN(f, zeroReader{}, MaxFileSize+1); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	if int64(b.Len()) >= MaxUploadSize {
		t.Fatal("fixture did not compress")
	}
	d := New(t.TempDir())
	_, err = d.PrepareUpload(context.Background(), "owner/site", "bomb.zip", "", bytes.NewReader(b.Bytes()), int64(b.Len()))
	if err == nil || !strings.Contains(err.Error(), "limits") {
		t.Fatal(err)
	}
	b.Reset()
	w = zip.NewWriter(&b)
	for i := 0; i <= MaxFiles; i++ {
		if _, err = w.CreateHeader(&zip.FileHeader{Name: "dir/", Method: zip.Store}); err != nil {
			t.Fatal(err)
		}
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = d.PrepareUpload(context.Background(), "owner/site", "entries.zip", "", bytes.NewReader(b.Bytes()), int64(b.Len()))
	if err == nil || !strings.Contains(err.Error(), "entry") {
		t.Fatal(err)
	}
	// Lie about the count: the central-directory scan must still stop at its limit.
	raw := bytes.Clone(b.Bytes())
	binary.LittleEndian.PutUint16(raw[len(raw)-12:], 1)
	binary.LittleEndian.PutUint16(raw[len(raw)-14:], 1)
	_, err = d.PrepareUpload(context.Background(), "owner/site", "entries.zip", "", bytes.NewReader(raw), int64(len(raw)))
	if err == nil || !strings.Contains(err.Error(), "entry") {
		t.Fatal(err)
	}
}
func TestUploadCancellationAndLimits(t *testing.T) {
	d := New(t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := d.PrepareUpload(ctx, "owner/site", "file.html", "", bytes.NewReader([]byte("hello")), 5)
	if err == nil {
		t.Fatal("cancelled upload accepted")
	}
	for _, size := range []int64{0, MaxUploadSize + 1} {
		if _, err = d.PrepareUpload(context.Background(), "owner/site", "file.html", "", bytes.NewReader(nil), size); err == nil {
			t.Fatal("invalid size accepted")
		}
	}
	for _, dir := range []string{"../escape", "/absolute", "dist"} {
		if _, err = d.PrepareUpload(context.Background(), "owner/site", "file.html", dir, bytes.NewReader([]byte("hello")), 5); err == nil {
			t.Fatal("invalid HTML directory accepted")
		}
	}
}

func TestZIPTotalLimitBeforeExtraction(t *testing.T) {
	var files []zipEntry
	for i := 0; i < 11; i++ {
		files = append(files, zipEntry{name: fmt.Sprintf("file%d", i), body: "x"})
	}
	raw := makeZIP(t, files...)
	// Forge sizes without supplying large payloads. Aggregate metadata must be
	// rejected before attempting even the first file's decompression.
	for pos := 0; ; {
		next := bytes.Index(raw[pos:], []byte{'P', 'K', 1, 2})
		if next < 0 {
			break
		}
		pos += next
		binary.LittleEndian.PutUint32(raw[pos+24:], uint32(MaxFileSize))
		pos += 46
	}
	d := New(t.TempDir())
	_, err := d.PrepareUpload(context.Background(), "owner/site", "large.zip", "", bytes.NewReader(raw), int64(len(raw)))
	if err == nil || !strings.Contains(err.Error(), "500 MiB") {
		t.Fatal(err)
	}
}
func TestZIPAmbiguousEndRecord(t *testing.T) {
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	if err := w.SetComment("ambiguous PK\x05\x06 directory comment"); err != nil {
		t.Fatal(err)
	}
	f, err := w.Create("index.html")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.WriteString(f, "hello"); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	d := New(t.TempDir())
	_, err = d.PrepareUpload(context.Background(), "owner/site", "ambiguous.zip", "", bytes.NewReader(b.Bytes()), int64(b.Len()))
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatal(err)
	}
}
