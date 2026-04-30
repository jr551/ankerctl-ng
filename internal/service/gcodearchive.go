// Package service — GCode archive helpers.
//
// ArchiveGCode writes a GCode payload to the archive directory and returns
// the relative filename used to store it. The archive dir lives under the
// config directory so it is co-located with the DB and survives restarts.
//
// Python reference: web/service/history.py PrintHistory.archive_upload /
// _archive_abspath / _default_archive_dir
package service

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/django1982/ankerctl/internal/gcode"
	"github.com/google/uuid"
)

const gcodeArchiveDirName = "gcode_archive"

// archiveSafeRe replaces characters that are unsafe in filenames.
var archiveSafeRe = regexp.MustCompile(`[^A-Za-z0-9._ -]`)

// sanitizeArchiveFilename produces a filesystem-safe basename.
// (Python: PrintHistory._sanitize_archive_filename)
func sanitizeArchiveFilename(filename string) string {
	base := filepath.Base(strings.TrimSpace(filename))
	if base == "" || base == "." {
		base = "upload.gcode"
	}
	safe := strings.TrimSpace(strings.Trim(archiveSafeRe.ReplaceAllString(base, "_"), ". "))
	if safe == "" {
		return "upload.gcode"
	}
	return safe
}

// GCodeArchiver stores GCode bytes on disk and returns the relative path.
type GCodeArchiver struct {
	archiveDir string
}

// NewGCodeArchiver creates an archiver rooted at configDir/gcode_archive.
func NewGCodeArchiver(configDir string) *GCodeArchiver {
	return &GCodeArchiver{
		archiveDir: filepath.Join(configDir, gcodeArchiveDirName),
	}
}

// Archive writes data to a uniquely-named file in the archive directory.
// Returns (relpath, size, nil) on success.
//
// (Python: PrintHistory.archive_upload)
func (a *GCodeArchiver) Archive(filename string, data []byte) (relpath string, size int64, err error) {
	if len(data) == 0 {
		return "", 0, fmt.Errorf("gcode archive: refusing to store empty payload")
	}
	if err := os.MkdirAll(a.archiveDir, 0o700); err != nil {
		return "", 0, fmt.Errorf("gcode archive: mkdir %s: %w", a.archiveDir, err)
	}

	safeName := sanitizeArchiveFilename(filename)
	stored := fmt.Sprintf("%s_%s_%s",
		time.Now().UTC().Format("20060102_150405"),
		uuid.NewString()[:8],
		safeName,
	)
	absPath := filepath.Join(a.archiveDir, stored)

	if err := os.WriteFile(absPath, data, 0o600); err != nil {
		return "", 0, fmt.Errorf("gcode archive: write %s: %w", absPath, err)
	}

	// Extract and persist the embedded thumbnail (PrusaSlicer / BambuStudio / Cura).
	// A missing or undecodable thumbnail is non-fatal — archive write already succeeded.
	// Python reference: web/service/history.py PrintHistory.archive_upload
	if thumbBytes, _ := gcode.ExtractThumbnail(data); len(thumbBytes) > 0 {
		thumbPath := absPath + ".thumbnail.png"
		_ = os.WriteFile(thumbPath, thumbBytes, 0o600) // non-fatal
	}

	return stored, int64(len(data)), nil
}

// AbsPath resolves a relative archive path to its absolute location.
// Returns empty string when relpath is empty or would escape the archive dir.
//
// (Python: PrintHistory._archive_abspath)
func (a *GCodeArchiver) AbsPath(relpath string) string {
	if relpath == "" || a.archiveDir == "" {
		return ""
	}
	abs := filepath.Clean(filepath.Join(a.archiveDir, relpath))
	if !strings.HasPrefix(abs, a.archiveDir+string(filepath.Separator)) {
		return "" // path traversal guard
	}
	return abs
}

// ReadArchive returns the bytes stored at relpath.
// Returns nil, ErrNotExist-wrapped error when the file is absent.
func (a *GCodeArchiver) ReadArchive(relpath string) ([]byte, error) {
	abs := a.AbsPath(relpath)
	if abs == "" {
		return nil, fmt.Errorf("gcode archive: invalid relpath %q", relpath)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("gcode archive: read %s: %w", abs, err)
	}
	return data, nil
}

// Exists reports whether the archive file at relpath is present.
func (a *GCodeArchiver) Exists(relpath string) bool {
	abs := a.AbsPath(relpath)
	if abs == "" {
		return false
	}
	_, err := os.Stat(abs)
	return err == nil
}

// ThumbnailRelpath returns the conventional relative path of the thumbnail for a
// given archive relpath: "<relpath>.thumbnail.png".
//
// Python reference: web/service/history.py PrintHistory._thumbnail_relpath
func ThumbnailRelpath(archiveRelpath string) string {
	if archiveRelpath == "" {
		return ""
	}
	return archiveRelpath + ".thumbnail.png"
}

// ThumbnailPath resolves the absolute path of the thumbnail file for the given
// archive relpath. Returns empty string when archiveRelpath is empty or escapes
// the archive directory.
func (a *GCodeArchiver) ThumbnailPath(archiveRelpath string) string {
	return a.AbsPath(ThumbnailRelpath(archiveRelpath))
}

// ReadThumbnail returns the PNG bytes for the thumbnail associated with the
// given archive relpath. Returns nil, nil when the thumbnail does not exist.
func (a *GCodeArchiver) ReadThumbnail(archiveRelpath string) ([]byte, error) {
	abs := a.ThumbnailPath(archiveRelpath)
	if abs == "" {
		return nil, nil
	}
	data, err := os.ReadFile(abs)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("gcode archive: read thumbnail %s: %w", abs, err)
	}
	return data, nil
}
