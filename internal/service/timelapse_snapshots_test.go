package service

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSaveManualSnapshot(t *testing.T) {
	svc := newTestTimelapseService(t)

	// Create a fake source JPEG.
	src, err := os.CreateTemp(t.TempDir(), "snap*.jpg")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = src.WriteString("fake jpeg bytes")
	_ = src.Close()

	collID, frameName, err := svc.SaveManualSnapshot(src.Name(), time.Now())
	if err != nil {
		t.Fatalf("SaveManualSnapshot failed: %v", err)
	}
	if collID == "" {
		t.Error("expected non-empty collectionID")
	}
	if frameName == "" {
		t.Error("expected non-empty frameName")
	}

	// The archived file should exist.
	archivePath := filepath.Join(svc.snapshotArchiveBase(), collID, frameName)
	if _, err := os.Stat(archivePath); err != nil {
		t.Errorf("archived snapshot not found at %q: %v", archivePath, err)
	}
}

func TestListSnapshots_IncludesArchived(t *testing.T) {
	svc := newTestTimelapseService(t)
	src, _ := os.CreateTemp(t.TempDir(), "snap*.jpg")
	_, _ = src.WriteString("fake")
	_ = src.Close()

	_, _, err := svc.SaveManualSnapshot(src.Name(), time.Now())
	if err != nil {
		t.Fatal(err)
	}

	collections := svc.ListSnapshots()
	if len(collections) == 0 {
		t.Fatal("expected at least one snapshot collection")
	}
	if collections[0].State != "manual" {
		t.Errorf("expected state=manual, got %q", collections[0].State)
	}
}

func TestGetSnapshotPath_NotFound(t *testing.T) {
	svc := newTestTimelapseService(t)
	_, ok := svc.GetSnapshotPath("nonexistent_collection", "frame.jpg")
	if ok {
		t.Error("expected false for non-existent collection")
	}
}

func TestGetSnapshotPath_PathTraversalBlocked(t *testing.T) {
	svc := newTestTimelapseService(t)
	_, ok := svc.GetSnapshotPath("../escape", "frame.jpg")
	if ok {
		t.Error("expected false for path traversal attempt in collection_id")
	}
	_, ok = svc.GetSnapshotPath("valid_coll", "../escape.jpg")
	if ok {
		t.Error("expected false for path traversal attempt in filename")
	}
}

func TestDeleteSnapshotCollection_NotFound(t *testing.T) {
	svc := newTestTimelapseService(t)
	deleted, err := svc.DeleteSnapshotCollection("no_such_collection")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if deleted {
		t.Error("expected deleted=false for non-existent collection")
	}
}

func TestDeleteSnapshotCollection_ProtectedActive(t *testing.T) {
	svc := newTestTimelapseService(t)
	// Create an active capture dir inside capturesDir/in_progress so resolveSnapshotCollectionDir finds it.
	activeDir := filepath.Join(svc.inProgressBase(), "active_cap")
	if err := os.MkdirAll(activeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	svc.mu.Lock()
	svc.active = &captureState{Dir: activeDir}
	svc.mu.Unlock()

	_, err := svc.DeleteSnapshotCollection("active_cap")
	if err == nil {
		t.Error("expected error when trying to delete an active capture")
	}
}

func TestDeleteSnapshot_ArchivedSnapshot(t *testing.T) {
	svc := newTestTimelapseService(t)
	src, _ := os.CreateTemp(t.TempDir(), "snap*.jpg")
	_, _ = src.WriteString("fake")
	_ = src.Close()

	collID, frameName, err := svc.SaveManualSnapshot(src.Name(), time.Now())
	if err != nil {
		t.Fatal(err)
	}

	deleted, err := svc.DeleteSnapshot(collID, frameName)
	if err != nil {
		t.Fatalf("DeleteSnapshot failed: %v", err)
	}
	if !deleted {
		t.Error("expected deleted=true")
	}
}
