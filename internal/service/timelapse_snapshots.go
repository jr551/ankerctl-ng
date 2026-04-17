package service

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const snapshotArchiveSubdir = "snapshots"

// SnapshotFrame describes one JPEG frame within a collection.
type SnapshotFrame struct {
	Filename  string `json:"filename"`
	SizeBytes int64  `json:"size_bytes"`
	CreatedAt string `json:"created_at"`
}

// SnapshotCollection represents a group of JPEGs from one capture session.
type SnapshotCollection struct {
	ID            string          `json:"id"`
	Label         string          `json:"label"`
	VideoFilename *string         `json:"video_filename"`
	FrameCount    int             `json:"frame_count"`
	CreatedAt     string          `json:"created_at"`
	State         string          `json:"state"` // "archived" | "manual" | "capturing" | "resume_pending"
	SourceLabel   *string         `json:"source_label"`
	AllowDelete   bool            `json:"allow_delete"`
	Frames        []SnapshotFrame `json:"frames"`
}

type snapshotMeta struct {
	Filename      string  `json:"filename,omitempty"`
	VideoFilename string  `json:"video_filename,omitempty"`
	ArchivedAt    string  `json:"archived_at,omitempty"`
	Status        string  `json:"status,omitempty"` // "archived" | "manual"
	SourceLabel   string  `json:"source_label,omitempty"`
	FrameCount    int     `json:"frame_count,omitempty"`
}

func (s *TimelapseService) snapshotArchiveBase() string {
	return filepath.Join(s.capturesDir, snapshotArchiveSubdir)
}

func readSnapshotMeta(dir string) *snapshotMeta {
	data, err := os.ReadFile(filepath.Join(dir, ".meta"))
	if err != nil {
		return nil
	}
	var m snapshotMeta
	if err := json.Unmarshal(data, &m); err != nil {
		return nil
	}
	return &m
}

func writeSnapshotMeta(dir string, m *snapshotMeta) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, ".meta"), append(data, '\n'), 0o644)
}

func snapshotFrameNames(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		n := e.Name()
		if e.Type().IsRegular() && strings.HasSuffix(strings.ToLower(n), ".jpg") && !strings.HasPrefix(n, ".") {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names
}

func snapshotCollectionRecord(dir string, state string, allowDelete bool) *SnapshotCollection {
	if dir == "" {
		return nil
	}
	if _, err := os.Stat(dir); err != nil {
		return nil
	}

	meta := readSnapshotMeta(dir)
	recordState := state
	if state == "archived" && meta != nil {
		switch meta.Status {
		case "archived", "manual":
			recordState = meta.Status
		}
	}

	frameNames := snapshotFrameNames(dir)
	if len(frameNames) == 0 {
		return nil
	}

	dirStat, err := os.Stat(dir)
	if err != nil {
		return nil
	}

	frames := make([]SnapshotFrame, 0, len(frameNames))
	for _, name := range frameNames {
		fp := filepath.Join(dir, name)
		st, err := os.Stat(fp)
		if err != nil {
			continue
		}
		frames = append(frames, SnapshotFrame{
			Filename:  name,
			SizeBytes: st.Size(),
			CreatedAt: st.ModTime().Format(time.RFC3339),
		})
	}
	if len(frames) == 0 {
		return nil
	}

	label := filepath.Base(dir)
	if meta != nil && meta.Filename != "" {
		label = meta.Filename
	}

	createdAt := dirStat.ModTime().Format(time.RFC3339)
	if meta != nil && meta.ArchivedAt != "" {
		createdAt = meta.ArchivedAt
	}

	var videoFilename *string
	if meta != nil && meta.VideoFilename != "" {
		v := meta.VideoFilename
		videoFilename = &v
	}

	var sourceLabel *string
	if meta != nil && meta.SourceLabel != "" {
		s := meta.SourceLabel
		sourceLabel = &s
	}

	return &SnapshotCollection{
		ID:            filepath.Base(dir),
		Label:         label,
		VideoFilename: videoFilename,
		FrameCount:    len(frames),
		CreatedAt:     createdAt,
		State:         recordState,
		SourceLabel:   sourceLabel,
		AllowDelete:   allowDelete,
		Frames:        frames,
	}
}

// ListSnapshots returns all snapshot collections: in-progress, resumable, and archived.
func (s *TimelapseService) ListSnapshots() []SnapshotCollection {
	s.mu.Lock()
	activeDir := ""
	resumeDir := ""
	if s.active != nil {
		activeDir = s.active.Dir
	}
	if s.resume != nil {
		resumeDir = s.resume.Dir
	}
	s.mu.Unlock()

	var collections []SnapshotCollection
	seen := map[string]bool{}

	// In-progress: active capture (read-only — cannot delete while capturing).
	if activeDir != "" {
		id := filepath.Base(activeDir)
		if rec := snapshotCollectionRecord(activeDir, "capturing", false); rec != nil {
			collections = append(collections, *rec)
			seen[id] = true
		}
	}

	// Resumable paused capture (allow delete so user can discard it).
	if resumeDir != "" {
		id := filepath.Base(resumeDir)
		if !seen[id] {
			if rec := snapshotCollectionRecord(resumeDir, "resume_pending", true); rec != nil {
				collections = append(collections, *rec)
				seen[id] = true
			}
		}
	}

	// Archived snapshot collections.
	archiveBase := s.snapshotArchiveBase()
	entries, err := os.ReadDir(archiveBase)
	if err == nil {
		type archEntry struct {
			path string
			mtime time.Time
		}
		var arches []archEntry
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			p := filepath.Join(archiveBase, e.Name())
			info, err := e.Info()
			if err != nil {
				continue
			}
			arches = append(arches, archEntry{path: p, mtime: info.ModTime()})
		}
		sort.Slice(arches, func(i, j int) bool {
			return arches[i].mtime.After(arches[j].mtime)
		})
		for _, a := range arches {
			id := filepath.Base(a.path)
			if seen[id] {
				continue
			}
			if rec := snapshotCollectionRecord(a.path, "archived", true); rec != nil {
				collections = append(collections, *rec)
				seen[id] = true
			}
		}
	}

	return collections
}

// GetSnapshotPath resolves collection_id + filename to an absolute path.
// Returns ("", false) if not found or path would escape the captures dir.
func (s *TimelapseService) GetSnapshotPath(collectionID, filename string) (string, bool) {
	dir, ok := s.resolveSnapshotCollectionDir(collectionID)
	if !ok {
		return "", false
	}
	if !strings.HasSuffix(strings.ToLower(filename), ".jpg") {
		return "", false
	}
	clean := filepath.Base(filename)
	if clean == "" || clean == "." || strings.Contains(clean, "..") {
		return "", false
	}
	p := filepath.Join(dir, clean)
	if _, err := os.Stat(p); err != nil {
		return "", false
	}
	return p, true
}

// DeleteSnapshot removes a single archived snapshot JPG.
// Returns an error if the collection is active (in-progress) or resumable.
func (s *TimelapseService) DeleteSnapshot(collectionID, filename string) (bool, error) {
	dir, ok := s.resolveSnapshotCollectionDir(collectionID)
	if !ok {
		return false, nil
	}
	if s.isProtectedCollectionDir(dir) {
		return false, fmt.Errorf("cannot delete snapshots from an active or resumable timelapse capture")
	}

	p, ok := s.GetSnapshotPath(collectionID, filename)
	if !ok {
		return false, nil
	}
	if err := os.Remove(p); err != nil {
		return false, nil
	}
	remaining := snapshotFrameNames(dir)
	if len(remaining) > 0 {
		if m := readSnapshotMeta(dir); m != nil {
			m.FrameCount = len(remaining)
			_ = writeSnapshotMeta(dir, m)
		}
	} else {
		_ = os.RemoveAll(dir)
	}
	return true, nil
}

// DeleteSnapshotCollection removes an entire snapshot collection directory.
// Active in-progress captures are protected; resumable ones may be discarded.
func (s *TimelapseService) DeleteSnapshotCollection(collectionID string) (bool, error) {
	dir, ok := s.resolveSnapshotCollectionDir(collectionID)
	if !ok {
		return false, nil
	}

	s.mu.Lock()
	isActive := s.active != nil && s.active.Dir == dir
	isResume := s.resume != nil && s.resume.Dir == dir
	if isResume {
		s.resume = nil
	}
	s.mu.Unlock()

	if isActive {
		return false, fmt.Errorf("cannot delete an active timelapse capture")
	}
	if err := os.RemoveAll(dir); err != nil {
		return false, err
	}
	return true, nil
}

// SaveManualSnapshot archives a snapshot JPEG taken outside a timelapse session.
// Returns the collection ID and frame filename for the API response.
func (s *TimelapseService) SaveManualSnapshot(sourcePath string, takenAt time.Time) (collectionID string, frameName string, err error) {
	if _, err := os.Stat(sourcePath); err != nil {
		return "", "", fmt.Errorf("manual snapshot source not found: %w", err)
	}

	ts := takenAt.Format("20060102_150405")
	collectionID = fmt.Sprintf("manual_snapshot_%s", takenAt.Format("20060102_150405_000000"))
	archiveDir := filepath.Join(s.snapshotArchiveBase(), collectionID)
	frameName = fmt.Sprintf("ankerctl_snapshot_%s.jpg", ts)
	dstPath := filepath.Join(archiveDir, frameName)

	if err := os.MkdirAll(archiveDir, 0o755); err != nil {
		return "", "", fmt.Errorf("manual snapshot: mkdir: %w", err)
	}
	if err := copyFile(sourcePath, dstPath); err != nil {
		_ = os.RemoveAll(archiveDir)
		return "", "", fmt.Errorf("manual snapshot: copy: %w", err)
	}
	m := &snapshotMeta{
		Filename:   fmt.Sprintf("Manual snapshot %s", takenAt.Format("2006-01-02 15:04:05")),
		ArchivedAt: takenAt.Format(time.RFC3339),
		Status:     "manual",
		FrameCount: 1,
	}
	if err := writeSnapshotMeta(archiveDir, m); err != nil {
		// Non-fatal — collection is still usable without meta.
	}
	return collectionID, frameName, nil
}

// resolveSnapshotCollectionDir finds the on-disk directory for a given collection ID.
// It checks: active capture dir, resume dir, and the archive base.
func (s *TimelapseService) resolveSnapshotCollectionDir(collectionID string) (string, bool) {
	if collectionID == "" || strings.Contains(collectionID, "..") || strings.ContainsAny(collectionID, `/\\`) {
		return "", false
	}
	clean := filepath.Base(collectionID)
	if clean == "" || clean == "." {
		return "", false
	}

	s.mu.Lock()
	activeDir := ""
	resumeDir := ""
	if s.active != nil {
		activeDir = s.active.Dir
	}
	if s.resume != nil {
		resumeDir = s.resume.Dir
	}
	s.mu.Unlock()

	if activeDir != "" && filepath.Base(activeDir) == clean {
		return activeDir, true
	}
	if resumeDir != "" && filepath.Base(resumeDir) == clean {
		return resumeDir, true
	}

	archivePath := filepath.Join(s.snapshotArchiveBase(), clean)
	if st, err := os.Stat(archivePath); err == nil && st.IsDir() {
		// Path traversal guard: resolved path must be inside snapshotArchiveBase.
		absArchive, _ := filepath.Abs(s.snapshotArchiveBase())
		absPath, _ := filepath.Abs(archivePath)
		if strings.HasPrefix(absPath, absArchive+string(os.PathSeparator)) {
			return archivePath, true
		}
	}
	return "", false
}

func (s *TimelapseService) isProtectedCollectionDir(dir string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active != nil && s.active.Dir == dir {
		return true
	}
	// Resume dir can be deleted (discarded) — not protected.
	return false
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}
