package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/django1982/ankerctl/internal/config"
	"github.com/django1982/ankerctl/internal/db"
	"github.com/django1982/ankerctl/internal/service"
	"github.com/go-chi/chi/v5"
)

// newTestHandlerWithDB creates a Handler backed by a file-based SQLite DB so a
// test can seed rows directly — notably two concurrent 'started' rows, which
// the public RecordStart API would otherwise mark 'interrupted'.
func newTestHandlerWithDB(t *testing.T) (*Handler, string) {
	t.Helper()
	cfgMgr, err := config.NewManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	dbPath := filepath.Join(t.TempDir(), "history.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	h := New(cfgMgr, database, nil, nil, false, func(http.ResponseWriter, string, any) error { return nil })
	return h, dbPath
}

// seedStartedRow inserts a 'started' history row directly, bypassing
// RecordStart's single-active-print orphan handling.
func seedStartedRow(t *testing.T, dbPath, filename, taskID string) int64 {
	t.Helper()
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer func() { _ = raw.Close() }()

	res, err := raw.Exec(
		`INSERT INTO print_history (filename, status, started_at, task_id) VALUES (?, 'started', ?, ?)`,
		filename, time.Now().UTC().Format(time.RFC3339Nano), taskID,
	)
	if err != nil {
		t.Fatalf("insert started row: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("LastInsertId: %v", err)
	}
	return id
}

// stubJobReaper is a minimal Service + jobReaperIface implementation backed by
// the real DB. It lets handler tests exercise the cancel wiring
// deterministically without starting the real reaper's background loop.
type stubJobReaper struct {
	db       *db.DB
	status   service.JobReaperStatus
	cancelFn func(ctx context.Context, maxAge time.Duration) ([]int64, error)
}

func (s *stubJobReaper) WorkerInit()                       {}
func (s *stubJobReaper) WorkerStart() error                { return nil }
func (s *stubJobReaper) WorkerRun(_ context.Context) error { return nil }
func (s *stubJobReaper) WorkerStop()                       {}
func (s *stubJobReaper) Name() string                      { return "jobreaper" }
func (s *stubJobReaper) State() service.RunState           { return service.StateStopped }
func (s *stubJobReaper) Start(_ context.Context)           {}
func (s *stubJobReaper) Stop()                             {}
func (s *stubJobReaper) Restart()                          {}
func (s *stubJobReaper) Shutdown()                         {}
func (s *stubJobReaper) Notify(_ any)                      {}
func (s *stubJobReaper) Tap(_ func(any)) func()            { return func() {} }

func (s *stubJobReaper) Status() service.JobReaperStatus { return s.status }

func (s *stubJobReaper) CancelStuck(_ context.Context, maxAge time.Duration) ([]int64, error) {
	if s.cancelFn != nil {
		return s.cancelFn(context.Background(), maxAge)
	}
	stuck, err := s.db.StuckStartedJobs(maxAge, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(stuck))
	for _, rec := range stuck {
		ids = append(ids, rec.ID)
	}
	if _, err := s.db.CancelStartedJobs(ids, "auto-cancel: job did not start printing"); err != nil {
		return nil, err
	}
	return ids, nil
}

// buildCancelRequest creates a chi-routed POST request for /api/history/{id}/cancel.
func buildCancelRequest(id, body string) *http.Request {
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/history/"+id+"/cancel", rdr)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", id)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, routeCtx))
}

func decodeJSON(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var resp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v body=%s", err, w.Body.String())
	}
	return resp
}

// TestHistoryEntryJSON_PrintStartedAt verifies the history JSON exposes
// print_started_at as null before printing and an RFC3339 string afterwards.
func TestHistoryEntryJSON_PrintStartedAt(t *testing.T) {
	h := newTestHandler(t)
	id, err := h.db.RecordStart("cube.gcode", "task-ps", "", 0)
	if err != nil || id == 0 {
		t.Fatalf("RecordStart: err=%v id=%d", err, id)
	}

	entry, err := h.db.GetEntry(id)
	if err != nil || entry == nil {
		t.Fatalf("GetEntry: err=%v entry=%v", err, entry)
	}
	if got := historyEntryJSON(entry, nil)["print_started_at"]; got != nil {
		t.Errorf("print_started_at=%v want nil before printing", got)
	}

	if err := h.db.MarkPrintStarted("cube.gcode", "task-ps"); err != nil {
		t.Fatalf("MarkPrintStarted: %v", err)
	}
	entry, err = h.db.GetEntry(id)
	if err != nil || entry == nil {
		t.Fatalf("GetEntry after mark: err=%v entry=%v", err, entry)
	}
	got, ok := historyEntryJSON(entry, nil)["print_started_at"].(string)
	if !ok || got == "" {
		t.Fatalf("print_started_at=%v want RFC3339 string", historyEntryJSON(entry, nil)["print_started_at"])
	}
	if _, err := time.Parse(time.RFC3339, got); err != nil {
		t.Errorf("print_started_at=%q not RFC3339: %v", got, err)
	}
}

// TestHistoryCancel_StartedRowCancelled verifies a 'started' entry is cancelled
// and the DB row transitions to 'cancelled'.
func TestHistoryCancel_StartedRowCancelled(t *testing.T) {
	h := newTestHandler(t)
	id, err := h.db.RecordStart("cube.gcode", "task-cancel", "", 0)
	if err != nil || id == 0 {
		t.Fatalf("RecordStart: err=%v id=%d", err, id)
	}

	w := httptest.NewRecorder()
	h.HistoryCancel(w, buildCancelRequest(fmt.Sprint(id), ""))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want=200 body=%s", w.Code, w.Body.String())
	}
	resp := decodeJSON(t, w)
	if resp["changed"] != true {
		t.Errorf("changed=%v want=true", resp["changed"])
	}
	if resp["entry_status"] != "cancelled" {
		t.Errorf("entry_status=%v want=cancelled", resp["entry_status"])
	}
	if resp["stopped_printer"] != false {
		t.Errorf("stopped_printer=%v want=false", resp["stopped_printer"])
	}

	entry, err := h.db.GetEntry(id)
	if err != nil || entry == nil {
		t.Fatalf("GetEntry: err=%v entry=%v", err, entry)
	}
	if entry.Status != "cancelled" {
		t.Errorf("db status=%q want=cancelled", entry.Status)
	}
}

// TestHistoryCancel_FinishedRowNoChange verifies cancelling an already-closed
// entry is a no-op reported with changed=false.
func TestHistoryCancel_FinishedRowNoChange(t *testing.T) {
	h := newTestHandler(t)
	id, err := h.db.RecordStart("done.gcode", "task-fin", "", 0)
	if err != nil || id == 0 {
		t.Fatalf("RecordStart: err=%v id=%d", err, id)
	}
	if err := h.db.RecordFinish("done.gcode", 100, "task-fin"); err != nil {
		t.Fatalf("RecordFinish: %v", err)
	}

	w := httptest.NewRecorder()
	h.HistoryCancel(w, buildCancelRequest(fmt.Sprint(id), ""))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want=200 body=%s", w.Code, w.Body.String())
	}
	resp := decodeJSON(t, w)
	if resp["changed"] != false {
		t.Errorf("changed=%v want=false", resp["changed"])
	}
	if resp["entry_status"] != "finished" {
		t.Errorf("entry_status=%v want=finished", resp["entry_status"])
	}

	entry, err := h.db.GetEntry(id)
	if err != nil || entry == nil {
		t.Fatalf("GetEntry: err=%v entry=%v", err, entry)
	}
	if entry.Status != "finished" {
		t.Errorf("db status=%q want=finished (unchanged)", entry.Status)
	}
}

// TestHistoryCancel_MissingEntry verifies 404 for an unknown id.
func TestHistoryCancel_MissingEntry(t *testing.T) {
	h := newTestHandler(t)

	w := httptest.NewRecorder()
	h.HistoryCancel(w, buildCancelRequest("999", ""))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status=%d want=404 body=%s", w.Code, w.Body.String())
	}
}

// TestHistoryCancel_BadID verifies 400 for a non-numeric or non-positive id.
func TestHistoryCancel_BadID(t *testing.T) {
	h := newTestHandler(t)
	for _, id := range []string{"notanumber", "0", "-3"} {
		w := httptest.NewRecorder()
		h.HistoryCancel(w, buildCancelRequest(id, ""))
		if w.Code != http.StatusBadRequest {
			t.Errorf("id=%q status=%d want=400", id, w.Code)
		}
	}
}

// TestHistoryResetStuck_CancelsStuckRows verifies two stuck rows are cancelled
// and a second call is a no-op.
func TestHistoryResetStuck_CancelsStuckRows(t *testing.T) {
	h, dbPath := newTestHandlerWithDB(t)
	h.svc = service.NewServiceManager()
	h.svc.Register(&stubJobReaper{db: h.db})

	id1 := seedStartedRow(t, dbPath, "stuck-a.gcode", "task-a")
	id2 := seedStartedRow(t, dbPath, "stuck-b.gcode", "task-b")

	w := httptest.NewRecorder()
	h.HistoryResetStuck(w, httptest.NewRequest(http.MethodPost, "/api/history/reset-stuck", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want=200 body=%s", w.Code, w.Body.String())
	}
	resp := decodeJSON(t, w)
	if resp["count"] != float64(2) {
		t.Errorf("count=%v want=2 body=%s", resp["count"], w.Body.String())
	}
	cancelled, _ := resp["cancelled"].([]any)
	if len(cancelled) != 2 {
		t.Errorf("cancelled=%v want 2 ids", resp["cancelled"])
	}
	for _, id := range []int64{id1, id2} {
		entry, err := h.db.GetEntry(id)
		if err != nil || entry == nil {
			t.Fatalf("GetEntry %d: err=%v entry=%v", id, err, entry)
		}
		if entry.Status != "cancelled" {
			t.Errorf("entry %d status=%q want=cancelled", id, entry.Status)
		}
	}

	// Second call: nothing left to cancel.
	w = httptest.NewRecorder()
	h.HistoryResetStuck(w, httptest.NewRequest(http.MethodPost, "/api/history/reset-stuck", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("second status=%d want=200 body=%s", w.Code, w.Body.String())
	}
	resp = decodeJSON(t, w)
	if resp["count"] != float64(0) {
		t.Errorf("second count=%v want=0 body=%s", resp["count"], w.Body.String())
	}
}

// TestHistoryResetStuck_ReaperUnavailable verifies 503 when no reaper is registered.
func TestHistoryResetStuck_ReaperUnavailable(t *testing.T) {
	h := newTestHandler(t)

	w := httptest.NewRecorder()
	h.HistoryResetStuck(w, httptest.NewRequest(http.MethodPost, "/api/history/reset-stuck", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want=503 body=%s", w.Code, w.Body.String())
	}
}

// TestJobsStatus_Shape verifies the frozen /api/jobs/status response shape.
func TestJobsStatus_Shape(t *testing.T) {
	h := newTestHandler(t)
	h.svc = service.NewServiceManager()
	h.svc.Register(&stubJobReaper{
		db: h.db,
		status: service.JobReaperStatus{
			Enabled:          true,
			StartTimeoutSec:  3600,
			CheckIntervalSec: 300,
		},
	})

	id, err := h.db.RecordStart("stuck.gcode", "task-stuck", "", 0)
	if err != nil || id == 0 {
		t.Fatalf("RecordStart: err=%v id=%d", err, id)
	}

	w := httptest.NewRecorder()
	h.JobsStatus(w, httptest.NewRequest(http.MethodGet, "/api/jobs/status", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d want=200 body=%s", w.Code, w.Body.String())
	}
	resp := decodeJSON(t, w)
	if resp["status"] != "ok" {
		t.Errorf("status=%v want=ok", resp["status"])
	}
	if resp["stuck_count"] != float64(1) {
		t.Errorf("stuck_count=%v want=1 body=%s", resp["stuck_count"], w.Body.String())
	}
	stuck, _ := resp["stuck"].([]any)
	if len(stuck) != 1 {
		t.Fatalf("stuck=%v want 1 entry", resp["stuck"])
	}
	row, _ := stuck[0].(map[string]any)
	if row["filename"] != "stuck.gcode" {
		t.Errorf("stuck[0].filename=%v want=stuck.gcode", row["filename"])
	}
	if row["started_at"] == "" || row["started_at"] == nil {
		t.Errorf("stuck[0].started_at=%v want non-empty", row["started_at"])
	}
	if _, ok := row["age_sec"]; !ok {
		t.Errorf("stuck[0] missing age_sec: %v", row)
	}

	active, _ := resp["active_print"].(map[string]any)
	if active == nil || active["printing"] != false {
		t.Errorf("active_print=%v want printing=false", resp["active_print"])
	}
	if _, ok := resp["pending_stored_file"].(map[string]any); !ok {
		t.Errorf("pending_stored_file=%v want object", resp["pending_stored_file"])
	}
	auto, _ := resp["auto_cancel"].(map[string]any)
	if auto == nil {
		t.Fatalf("auto_cancel=%v want object", resp["auto_cancel"])
	}
	if auto["enabled"] != true {
		t.Errorf("auto_cancel.enabled=%v want=true", auto["enabled"])
	}
	if auto["start_timeout_sec"] != float64(3600) {
		t.Errorf("auto_cancel.start_timeout_sec=%v want=3600", auto["start_timeout_sec"])
	}
}

// TestJobsStatus_ReaperUnavailable verifies 503 when no reaper is registered.
func TestJobsStatus_ReaperUnavailable(t *testing.T) {
	h := newTestHandler(t)

	w := httptest.NewRecorder()
	h.JobsStatus(w, httptest.NewRequest(http.MethodGet, "/api/jobs/status", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want=503 body=%s", w.Code, w.Body.String())
	}
}
