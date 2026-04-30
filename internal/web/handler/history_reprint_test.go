package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

// --- mock archiver ---

type mockArchiver struct {
	data    map[string][]byte  // relpath → bytes
	readErr map[string]error   // relpath → forced error
}

func newMockArchiver() *mockArchiver {
	return &mockArchiver{
		data:    make(map[string][]byte),
		readErr: make(map[string]error),
	}
}

func (m *mockArchiver) Archive(filename string, data []byte) (string, int64, error) {
	rel := "archived_" + filename
	m.data[rel] = data
	return rel, int64(len(data)), nil
}

func (m *mockArchiver) ReadArchive(relpath string) ([]byte, error) {
	if err := m.readErr[relpath]; err != nil {
		return nil, err
	}
	data, ok := m.data[relpath]
	if !ok {
		return nil, fmt.Errorf("not found: %s", relpath)
	}
	return data, nil
}

func (m *mockArchiver) Exists(relpath string) bool {
	_, ok := m.data[relpath]
	return ok
}

func (m *mockArchiver) ReadThumbnail(_ string) ([]byte, error) {
	return nil, nil
}

// buildReprintRequest creates a chi-routed POST request for /api/history/{id}/reprint.
func buildReprintRequest(id string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/history/"+id+"/reprint", nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", id)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, routeCtx))
}

// TestHistoryReprint_NotFound verifies 404 when the history entry does not exist.
func TestHistoryReprint_NotFound(t *testing.T) {
	h := newTestHandler(t)
	h.WithGCodeArchiver(newMockArchiver())

	w := httptest.NewRecorder()
	h.HistoryReprint(w, buildReprintRequest("999"))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d want=404", w.Code)
	}
}

// TestHistoryReprint_BadID verifies 400 for a non-numeric ID.
func TestHistoryReprint_BadID(t *testing.T) {
	h := newTestHandler(t)
	h.WithGCodeArchiver(newMockArchiver())

	w := httptest.NewRecorder()
	h.HistoryReprint(w, buildReprintRequest("notanumber"))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want=400", w.Code)
	}
}

// TestHistoryReprint_NoArchive verifies 404 when the history row exists but has
// no archive file.
func TestHistoryReprint_NoArchive(t *testing.T) {
	h := newTestHandler(t)
	archiver := newMockArchiver()
	h.WithGCodeArchiver(archiver)

	// Insert a history row without an archive.
	rowID, err := h.db.RecordStart("cube.gcode", "task-narch", "", 0)
	if err != nil || rowID == 0 {
		t.Fatalf("RecordStart: err=%v id=%d", err, rowID)
	}

	w := httptest.NewRecorder()
	h.HistoryReprint(w, buildReprintRequest(fmt.Sprint(rowID)))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d want=404 body=%s", w.Code, w.Body.String())
	}
	var resp map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["error"] == "" {
		t.Error("expected non-empty error field in 404 response")
	}
}

// TestHistoryReprint_NoArchiver verifies 503 when no archiver is configured.
func TestHistoryReprint_NoArchiver(t *testing.T) {
	h := newTestHandler(t)
	// gcodeArchiver intentionally not set

	rowID, err := h.db.RecordStart("cube.gcode", "task-noarch", "", 0)
	if err != nil || rowID == 0 {
		t.Fatalf("RecordStart: err=%v id=%d", err, rowID)
	}

	w := httptest.NewRecorder()
	h.HistoryReprint(w, buildReprintRequest(fmt.Sprint(rowID)))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want=503", w.Code)
	}
}

// TestHistoryReprint_ArchiveReadError verifies 500 when ReadArchive returns an error.
func TestHistoryReprint_ArchiveReadError(t *testing.T) {
	h := newTestHandler(t)
	archiver := newMockArchiver()
	archiver.data["myarchive.gcode"] = []byte("G28\n") // exists
	archiver.readErr["myarchive.gcode"] = errors.New("disk i/o error")
	h.WithGCodeArchiver(archiver)

	rowID, err := h.db.RecordStart("cube.gcode", "task-readerr", "myarchive.gcode", 4)
	if err != nil || rowID == 0 {
		t.Fatalf("RecordStart: err=%v id=%d", err, rowID)
	}

	w := httptest.NewRecorder()
	h.HistoryReprint(w, buildReprintRequest(fmt.Sprint(rowID)))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want=500 body=%s", w.Code, w.Body.String())
	}
}
