package deploy

import (
	"archive/tar"
	"bytes"
	"strings"
	"testing"
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
