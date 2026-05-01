package service

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSanitizeArchiveFilename(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"cube.gcode", "cube.gcode"},
		{"my model.gcode", "my model.gcode"},
		{"../etc/passwd", "passwd"},            // only basename is kept (filepath.Base)
		{"foo:bar?.gcode", "foo_bar_.gcode"},   // forbidden chars replaced
		{"", "upload.gcode"},                   // empty → fallback
		{"   ", "upload.gcode"},                // whitespace-only → fallback
		{"../../../etc/passwd", "passwd"},      // deep traversal: only basename
		{"/absolute/path.gcode", "path.gcode"}, // only basename is kept
	}
	for _, tc := range cases {
		got := sanitizeArchiveFilename(tc.input)
		if got != tc.want {
			t.Errorf("sanitizeArchiveFilename(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestGCodeArchiver_ArchiveAndRead(t *testing.T) {
	configDir := t.TempDir()
	a := NewGCodeArchiver(configDir)

	data := []byte("G28\nM104 S200\nG1 X10\n")
	relpath, size, err := a.Archive("test.gcode", data)
	if err != nil {
		t.Fatalf("Archive: %v", err)
	}
	if relpath == "" {
		t.Fatal("expected non-empty relpath")
	}
	if size != int64(len(data)) {
		t.Errorf("size=%d want=%d", size, len(data))
	}
	if !strings.HasSuffix(relpath, "test.gcode") {
		t.Errorf("relpath %q should end with 'test.gcode'", relpath)
	}
	if !a.Exists(relpath) {
		t.Error("Exists() = false, want true")
	}

	got, err := a.ReadArchive(relpath)
	if err != nil {
		t.Fatalf("ReadArchive: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Errorf("ReadArchive returned %q, want %q", got, data)
	}

	// Verify file is inside the archive dir
	absPath := filepath.Join(configDir, gcodeArchiveDirName, relpath)
	if _, err := os.Stat(absPath); err != nil {
		t.Errorf("archive file not found at expected path %s: %v", absPath, err)
	}
}

func TestGCodeArchiver_EmptyDataRejected(t *testing.T) {
	a := NewGCodeArchiver(t.TempDir())
	_, _, err := a.Archive("empty.gcode", []byte{})
	if err == nil {
		t.Fatal("expected error for empty payload, got nil")
	}
}

func TestGCodeArchiver_AbsPath_TraversalGuard(t *testing.T) {
	a := NewGCodeArchiver(t.TempDir())

	// Path traversal attempt — AbsPath must return ""
	if got := a.AbsPath("../etc/passwd"); got != "" {
		t.Errorf("AbsPath traversal should return empty, got %q", got)
	}
	if got := a.AbsPath("../../shadow"); got != "" {
		t.Errorf("AbsPath traversal should return empty, got %q", got)
	}
}

func TestGCodeArchiver_Exists_Missing(t *testing.T) {
	a := NewGCodeArchiver(t.TempDir())
	if a.Exists("nonexistent.gcode") {
		t.Error("Exists() = true for non-existent file")
	}
}

func TestGCodeArchiver_Archive_SavesThumbnail(t *testing.T) {
	// A minimal 1×1 PNG encoded as a PrusaSlicer-style thumbnail comment block.
	// Same test vector used in gcode/thumbnail_test.go.
	png64 := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+a5ZQAAAAASUVORK5CYII="
	gcodeInput := "; thumbnail begin 64x64 68\n; " + png64 + "\n; thumbnail end\nG28\n"

	a := NewGCodeArchiver(t.TempDir())
	relpath, _, err := a.Archive("model.gcode", []byte(gcodeInput))
	if err != nil {
		t.Fatalf("Archive: %v", err)
	}

	thumbBytes, err := a.ReadThumbnail(relpath)
	if err != nil {
		t.Fatalf("ReadThumbnail: %v", err)
	}
	if len(thumbBytes) == 0 {
		t.Fatal("expected thumbnail bytes, got nil/empty")
	}
	// Verify PNG magic bytes.
	pngMagic := []byte("\x89PNG\r\n\x1a\n")
	if !bytes.HasPrefix(thumbBytes, pngMagic) {
		n := len(thumbBytes)
		if n > 8 {
			n = 8
		}
		t.Errorf("thumbnail does not start with PNG magic bytes; got: %x", thumbBytes[:n])
	}

	// ThumbnailRelpath must follow the .thumbnail.png convention.
	if got := ThumbnailRelpath(relpath); got != relpath+".thumbnail.png" {
		t.Errorf("ThumbnailRelpath: got %q, want %q", got, relpath+".thumbnail.png")
	}
}

func TestGCodeArchiver_ReadThumbnail_Missing(t *testing.T) {
	a := NewGCodeArchiver(t.TempDir())
	// Archive without a thumbnail block.
	relpath, _, err := a.Archive("nothumbnail.gcode", []byte("G28\nM104 S200\n"))
	if err != nil {
		t.Fatalf("Archive: %v", err)
	}
	thumb, err := a.ReadThumbnail(relpath)
	if err != nil {
		t.Fatalf("ReadThumbnail unexpected error: %v", err)
	}
	if thumb != nil {
		t.Errorf("expected nil thumbnail for GCode without thumbnail block")
	}
}

func TestGCodeArchiver_ReadThumbnail_InvalidRelpath(t *testing.T) {
	a := NewGCodeArchiver(t.TempDir())
	// Traversal attempt must return nil, nil (not an error that leaks paths).
	thumb, err := a.ReadThumbnail("../../etc/passwd")
	if err != nil {
		t.Fatalf("unexpected error for invalid relpath: %v", err)
	}
	if thumb != nil {
		t.Errorf("expected nil for traversal relpath")
	}
}
