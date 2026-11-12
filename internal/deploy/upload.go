package deploy

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

const MaxUploadSize int64 = 50 << 20
const UploadTimeout = 2 * time.Minute

// Upload owns a private, validated snapshot until Publish or Close is called.
// The caller must serialize preparation/publication with other project operations.
type Upload struct {
	d                                    *Deployer
	key, stage, subdir, digest, filename string
}

func (u *Upload) Close() {
	if u.stage != "" {
		_ = os.RemoveAll(u.stage)
	}
}
func (u *Upload) Subdir() string { return u.subdir }
func (u *Upload) Publish() (string, error) {
	id := "r_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	dest := filepath.Join(u.d.releaseDir(u.key), id)
	if err := os.Rename(u.stage, dest); err != nil {
		return "", err
	}
	u.stage = dest
	if err := u.d.finishRelease(u.key, id, releaseMeta{Commit: u.digest, Message: "Upload: " + u.filename, Created: time.Now().UTC(), Subdir: u.subdir}); err != nil {
		return "", err
	}
	u.stage = ""
	return u.digest, nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, fmt.Errorf("upload validation interrupted: %w", err)
	}
	return r.r.Read(p)
}

type contextReaderAt struct {
	ctx context.Context
	r   io.ReaderAt
}

func (r contextReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, fmt.Errorf("upload validation interrupted: %w", err)
	}
	return r.r.ReadAt(p, off)
}

// PrepareUpload never touches the live pointer. All output, including actual
// decompressed bytes, is bounded before a release can be activated.
func (d *Deployer) PrepareUpload(ctx context.Context, key, filename, subdir string, r io.ReaderAt, size int64) (_ *Upload, err error) {
	ctx, cancel := context.WithTimeout(ctx, UploadTimeout)
	defer cancel()
	if err = ValidateSubdir(subdir); err != nil {
		return nil, err
	}
	if size <= 0 || size > MaxUploadSize {
		return nil, fmt.Errorf("upload must be nonempty and at most 50 MiB")
	}
	ext := strings.ToLower(filepath.Ext(filename))
	if ext != ".zip" && ext != ".html" && ext != ".htm" {
		return nil, fmt.Errorf("choose one HTML file or ZIP archive")
	}
	root := d.releaseDir(key)
	if err = os.MkdirAll(root, 0755); err != nil {
		return nil, err
	}
	stage, err := os.MkdirTemp(root, ".stage-upload-")
	if err != nil {
		return nil, err
	}
	u := &Upload{d: d, key: key, stage: stage, subdir: subdir, filename: filepath.Base(filename)}
	defer func() {
		if err != nil {
			u.Close()
		}
	}()
	r = contextReaderAt{ctx, r}
	if ext == ".zip" {
		if err = extractZIP(ctx, r, size, stage); err != nil {
			return nil, err
		}
		// A ZIP commonly wraps the site in one enclosing directory. Keep it in the
		// snapshot and record the detected publish root rather than rewriting paths.
		if subdir == "" {
			entries, e := os.ReadDir(stage)
			if e != nil {
				return nil, e
			}
			if len(entries) == 1 && entries[0].IsDir() {
				u.subdir = entries[0].Name()
			}
		}
	} else {
		if subdir != "" {
			return nil, fmt.Errorf("leave publish directory empty for a single HTML file")
		}
		if err = writeUploadFile(ctx, filepath.Join(stage, "index.html"), io.NewSectionReader(r, 0, size), size); err != nil {
			return nil, err
		}
	}
	index := filepath.Join(stage, u.subdir, "index.html")
	info, statErr := os.Stat(index)
	if statErr != nil || !info.Mode().IsRegular() {
		// Provide a useful correction without guessing between multiple entry pages.
		var found []string
		_ = filepath.WalkDir(stage, func(p string, e os.DirEntry, eerr error) error {
			if eerr == nil && !e.IsDir() && e.Name() == "index.html" && len(found) < 3 {
				rel, _ := filepath.Rel(stage, filepath.Dir(p))
				if rel == "." {
					rel = "project root (leave publish directory blank)"
				}
				found = append(found, filepath.ToSlash(rel))
			}
			return eerr
		})
		hint := " Include your built site's index.html."
		if len(found) > 0 {
			hint = fmt.Sprintf(" Found index.html in: %s. Select that publish directory.", strings.Join(found, ", "))
		}
		return nil, fmt.Errorf("no index.html found in publish directory %q.%s", u.subdir, hint)
	}
	if info.Size() == 0 {
		return nil, fmt.Errorf("index.html in publish directory %q is empty; include your built site entry page", u.subdir)
	}
	u.digest, err = snapshotDigest(ctx, stage)
	if err != nil {
		return nil, err
	}
	return u, nil
}

// Bound central-directory entries before archive/zip allocates File objects.
// Accept ordinary single-volume ZIPs; ZIP64 and self-extracting layouts are
// unnecessary for the upload limits and are rejected explicitly.
func checkZIPDirectory(ctx context.Context, r io.ReaderAt, size int64) error {
	n := min(size, int64(65557))
	tail := make([]byte, n)
	if _, err := r.ReadAt(tail, size-n); err != nil {
		return fmt.Errorf("invalid ZIP: %w", err)
	}
	for i := len(tail) - 22; i >= 0; i-- {
		if binary.LittleEndian.Uint32(tail[i:]) != 0x06054b50 || i+22+int(binary.LittleEndian.Uint16(tail[i+20:])) != len(tail) {
			continue
		}
		end := tail[i:]
		// archive/zip tolerates trailing bytes after an end record. Reject
		// ambiguous signatures in comments so it cannot select a different
		// central directory from the one bounded by this preflight.
		if bytes.Contains(end[22:], []byte{'P', 'K', 5, 6}) {
			return fmt.Errorf("ambiguous ZIP end records in archive comment")
		}
		count := int(binary.LittleEndian.Uint16(end[10:]))
		if binary.LittleEndian.Uint16(end[4:]) != 0 || binary.LittleEndian.Uint16(end[6:]) != 0 || int(binary.LittleEndian.Uint16(end[8:])) != count {
			return fmt.Errorf("multi-volume ZIP archives are unsupported")
		}
		length := int64(binary.LittleEndian.Uint32(end[12:]))
		offset := int64(binary.LittleEndian.Uint32(end[16:]))
		if count == 65535 || length == 0xffffffff || offset == 0xffffffff {
			return fmt.Errorf("ZIP64 archives are unsupported; upload only your built site files")
		}
		if count > MaxFiles {
			return fmt.Errorf("ZIP exceeds the %d-entry limit", MaxFiles)
		}
		if offset+length != size-n+int64(i) {
			return fmt.Errorf("invalid ZIP central directory")
		}
		// Verify every central entry, including its variable fields, rather than
		// trusting the declared entry count (which ZIP readers may ignore).
		pos := offset
		for entries := 0; pos < offset+length; entries++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			if entries >= MaxFiles {
				return fmt.Errorf("ZIP exceeds the %d-entry limit", MaxFiles)
			}
			var h [46]byte
			if _, err := r.ReadAt(h[:], pos); err != nil || binary.LittleEndian.Uint32(h[:]) != 0x02014b50 {
				return fmt.Errorf("invalid ZIP central directory entry")
			}
			pos += 46 + int64(binary.LittleEndian.Uint16(h[28:])) + int64(binary.LittleEndian.Uint16(h[30:])) + int64(binary.LittleEndian.Uint16(h[32:]))
			if pos > offset+length {
				return fmt.Errorf("truncated ZIP central directory")
			}
			if pos == offset+length && entries+1 != count {
				return fmt.Errorf("invalid ZIP entry count")
			}
		}
		if count == 0 {
			return fmt.Errorf("ZIP archive is empty")
		}
		return nil
	}
	return fmt.Errorf("invalid or truncated ZIP archive")
}

func extractZIP(ctx context.Context, r io.ReaderAt, size int64, dest string) error {
	if err := checkZIPDirectory(ctx, r, size); err != nil {
		return err
	}
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return fmt.Errorf("invalid ZIP: %w", err)
	}
	if len(zr.File) > MaxFiles {
		return fmt.Errorf("ZIP exceeds the %d-entry limit", MaxFiles)
	}
	var declared uint64
	for _, f := range zr.File {
		if f.UncompressedSize64 > uint64(MaxFileSize) || f.UncompressedSize64 > uint64(MaxTotalSize)-declared {
			return fmt.Errorf("ZIP exceeds limits: 50 MiB per file, 500 MiB extracted total (file %q)", f.Name)
		}
		declared += f.UncompressedSize64
	}
	seen := make(map[string]bool)
	spellings := make(map[string]string)
	var total int64
	for _, f := range zr.File {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := strings.TrimSuffix(f.Name, "/")
		if name == "" || path.IsAbs(name) || path.Clean(name) != name || name == ".." || strings.HasPrefix(name, "../") || strings.ContainsAny(name, "\\:\x00") || len(name) > 4096 {
			return fmt.Errorf("unsafe ZIP path %q", f.Name)
		}
		// Case folding also rejects ambiguous paths on case-insensitive filesystems.
		canonical := strings.ToLower(name)
		if seen[canonical] {
			return fmt.Errorf("duplicate ZIP path %q", name)
		}
		seen[canonical] = true
		parts := strings.Split(name, "/")
		for i := range parts {
			prefix := strings.Join(parts[:i+1], "/")
			folded := strings.ToLower(prefix)
			if previous, ok := spellings[folded]; ok && previous != prefix {
				return fmt.Errorf("ambiguous case in ZIP path %q", name)
			}
			spellings[folded] = prefix
		}
		for _, part := range strings.Split(canonical, "/") {
			if part == ".git" || part == ".hg" || part == ".svn" {
				return fmt.Errorf("ZIP contains repository metadata %q; upload only your built site files", name)
			}
		}
		if f.Flags&1 != 0 {
			return fmt.Errorf("encrypted ZIP entries are unsupported: %q", name)
		}
		if f.Method != zip.Store && f.Method != zip.Deflate {
			return fmt.Errorf("unsupported ZIP compression for %q", name)
		}
		mode := f.Mode()
		if mode.Type() != 0 && mode.Type() != os.ModeDir {
			return fmt.Errorf("ZIP contains symlink or special file %q", name)
		}
		target := filepath.Join(dest, filepath.FromSlash(name))
		if mode.IsDir() {
			if f.UncompressedSize64 != 0 {
				return fmt.Errorf("invalid ZIP directory %q", name)
			}
			if err := os.MkdirAll(target, 0755); err != nil {
				return fmt.Errorf("ZIP path collision at %q", name)
			}
			continue
		}
		if f.UncompressedSize64 > uint64(MaxFileSize) || f.UncompressedSize64 > uint64(MaxTotalSize-total) {
			return fmt.Errorf("ZIP exceeds limits: 50 MiB per file, 500 MiB extracted total (file %q)", name)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return fmt.Errorf("ZIP path collision at %q", name)
		}
		src, err := f.Open()
		if err != nil {
			return fmt.Errorf("cannot read ZIP entry %q: %w", name, err)
		}
		err = writeUploadFile(ctx, target, src, int64(f.UncompressedSize64))
		closeErr := src.Close()
		if err != nil {
			return fmt.Errorf("invalid ZIP entry %q: %w", name, err)
		}
		if closeErr != nil {
			return fmt.Errorf("invalid ZIP entry %q: %w", name, closeErr)
		}
		total += int64(f.UncompressedSize64)
	}
	return nil
}

func writeUploadFile(ctx context.Context, target string, src io.Reader, expected int64) error {
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return fmt.Errorf("duplicate or conflicting file path")
	}
	// Reading one byte beyond the claimed size catches forged size metadata;
	// reading to EOF also makes archive/zip verify the entry's checksum.
	n, copyErr := io.Copy(f, io.LimitReader(contextReader{ctx, src}, expected+1))
	closeErr := f.Close()
	if copyErr != nil {
		return copyErr
	}
	if n != expected {
		return fmt.Errorf("file size does not match archive metadata")
	}
	return closeErr
}

func snapshotDigest(ctx context.Context, root string) (string, error) {
	var names []string
	err := filepath.WalkDir(root, func(p string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !e.IsDir() {
			names = append(names, p)
		}
		return ctx.Err()
	})
	if err != nil {
		return "", err
	}
	sort.Strings(names)
	h := sha256.New()
	for _, p := range names {
		rel, _ := filepath.Rel(root, p)
		f, err := os.Open(p)
		if err != nil {
			return "", err
		}
		info, err := f.Stat()
		if err != nil {
			f.Close()
			return "", err
		}
		fmt.Fprintf(h, "%s\x00%d\x00", filepath.ToSlash(rel), info.Size())
		_, err = io.Copy(h, contextReader{ctx, f})
		f.Close()
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
