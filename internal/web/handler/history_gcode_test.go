package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// buildGCodeRequest creates a chi-routed GET request for /api/history/{id}/gcode.
func buildGCodeRequest(id string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/history/"+id+"/gcode", nil)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", id)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, routeCtx))
}

// TestHistoryGCode_BadID verifies 400 for a non-numeric ID.
func TestHistoryGCode_BadID(t *testing.T) {
	h := newTestHandler(t)
	h.WithGCodeArchiver(newMockArchiver())

	w := httptest.NewRecorder()
	h.HistoryGCode(w, buildGCodeRequest("notanumber"))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want=400", w.Code)
	}
}

// TestHistoryGCode_NoArchiver verifies 404 when no archiver is configured.
func TestHistoryGCode_NoArchiver(t *testing.T) {
	h := newTestHandler(t)
	// gcodeArchiver intentionally not set

	rowID, err := h.db.RecordStart("cube.gcode", "task-gc-noarch", "", 0)
	if err != nil || rowID == 0 {
		t.Fatalf("RecordStart: err=%v id=%d", err, rowID)
	}

	w := httptest.NewRecorder()
	h.HistoryGCode(w, buildGCodeRequest(fmt.Sprint(rowID)))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d want=404", w.Code)
	}
}

// TestHistoryGCode_NotFound verifies 404 when the history entry does not exist.
func TestHistoryGCode_NotFound(t *testing.T) {
	h := newTestHandler(t)
	h.WithGCodeArchiver(newMockArchiver())

	w := httptest.NewRecorder()
	h.HistoryGCode(w, buildGCodeRequest("999"))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d want=404", w.Code)
	}
}

// TestHistoryGCode_NoArchive verifies 404 when the row exists but has no archive.
func TestHistoryGCode_NoArchive(t *testing.T) {
	h := newTestHandler(t)
	h.WithGCodeArchiver(newMockArchiver())

	rowID, err := h.db.RecordStart("cube.gcode", "task-gc-narch", "", 0)
	if err != nil || rowID == 0 {
		t.Fatalf("RecordStart: err=%v id=%d", err, rowID)
	}

	w := httptest.NewRecorder()
	h.HistoryGCode(w, buildGCodeRequest(fmt.Sprint(rowID)))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d want=404 body=%s", w.Code, w.Body.String())
	}
}

// TestHistoryGCode_ArchiveReadError verifies 500 when ReadArchive fails.
func TestHistoryGCode_ArchiveReadError(t *testing.T) {
	h := newTestHandler(t)
	archiver := newMockArchiver()
	archiver.data["broken.gcode"] = []byte("G28\n") // Exists() true
	archiver.readErr["broken.gcode"] = errors.New("disk i/o error")
	h.WithGCodeArchiver(archiver)

	rowID, err := h.db.RecordStart("cube.gcode", "task-gc-readerr", "broken.gcode", 4)
	if err != nil || rowID == 0 {
		t.Fatalf("RecordStart: err=%v id=%d", err, rowID)
	}

	w := httptest.NewRecorder()
	h.HistoryGCode(w, buildGCodeRequest(fmt.Sprint(rowID)))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want=500 body=%s", w.Code, w.Body.String())
	}
}

// TestHistoryGCode_Success verifies 200 with the archived bytes and a text/plain
// content type, so the browser can fetch and parse the toolpath.
func TestHistoryGCode_Success(t *testing.T) {
	h := newTestHandler(t)
	archiver := newMockArchiver()
	content := []byte("G28\nG90\nG1 X10 Y10 E1\n")
	archiver.data["job.gcode"] = content
	h.WithGCodeArchiver(archiver)

	rowID, err := h.db.RecordStart("cube.gcode", "task-gc-ok", "job.gcode", int64(len(content)))
	if err != nil || rowID == 0 {
		t.Fatalf("RecordStart: err=%v id=%d", err, rowID)
	}

	w := httptest.NewRecorder()
	h.HistoryGCode(w, buildGCodeRequest(fmt.Sprint(rowID)))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want=200 body=%s", w.Code, w.Body.String())
	}
	if got := w.Body.String(); got != string(content) {
		t.Fatalf("body=%q want=%q", got, string(content))
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("Content-Type=%q want text/plain prefix", ct)
	}
}
