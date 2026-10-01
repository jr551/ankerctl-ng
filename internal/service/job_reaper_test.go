package service

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/django1982/ankerctl/internal/config"
	"github.com/django1982/ankerctl/internal/db"
	"github.com/django1982/ankerctl/internal/model"
)

// fakeJobQueue is a minimal JobQueueController for the reaper tests.
type fakeJobQueue struct {
	printing       bool
	activeFilename string
	pendingPath    string
	pendingSince   time.Time
	pendingOK      bool
	clearCalls     int
}

func (f *fakeJobQueue) IsPrinting() bool { return f.printing }

func (f *fakeJobQueue) ActivePrintFilename() string { return f.activeFilename }

func (f *fakeJobQueue) PendingStoredFileStart() (string, time.Time, bool) {
	return f.pendingPath, f.pendingSince, f.pendingOK
}

func (f *fakeJobQueue) ClearPendingStoredFileStart() bool {
	if !f.pendingOK {
		return false
	}
	f.pendingOK = false
	f.clearCalls++
	return true
}

// unsetEnv removes an environment variable for the duration of the test.
func unsetEnv(t *testing.T, key string) {
	t.Helper()
	old, ok := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("unset %s: %v", key, err)
	}
	t.Cleanup(func() {
		if ok {
			_ = os.Setenv(key, old)
		} else {
			_ = os.Unsetenv(key)
		}
	})
}

// newReaperTestDB opens a real temp-file database for the reaper tests.
func newReaperTestDB(t *testing.T) (*db.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "history.db")
	d, err := db.Open(path)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d, path
}

// insertHistoryRow inserts a print_history row directly so tests can control
// started_at / print_started_at (RecordStart always uses "now").
func insertHistoryRow(t *testing.T, path, filename, status string, startedAt time.Time, printStartedAt *time.Time) int64 {
	t.Helper()
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer raw.Close()

	var printStarted any
	if printStartedAt != nil {
		printStarted = printStartedAt.UTC().Format(time.RFC3339Nano)
	}
	res, err := raw.Exec(
		`INSERT INTO print_history (filename, status, started_at, print_started_at) VALUES (?, ?, ?, ?)`,
		filename, status, startedAt.UTC().Format(time.RFC3339Nano), printStarted,
	)
	if err != nil {
		t.Fatalf("insert history row: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("LastInsertId: %v", err)
	}
	return id
}

// newReaperTestService builds a reaper backed by a temp config dir (no
// config.json, so the default start timeout applies unless overridden).
func newReaperTestService(t *testing.T, d *db.DB, q JobQueueController) *JobReaperService {
	t.Helper()
	cfgMgr, err := config.NewManager(t.TempDir())
	if err != nil {
		t.Fatalf("config.NewManager: %v", err)
	}
	return NewJobReaperService(d, cfgMgr, q)
}

func TestJobReaper_RunOnce_AutoCancelRules(t *testing.T) {
	unsetEnv(t, jobStartTimeoutEnv)
	d, path := newReaperTestDB(t)

	now := time.Now()
	stale := now.Add(-2 * time.Hour)
	printStarted := now.Add(-2 * time.Hour)

	staleID := insertHistoryRow(t, path, "stale.gcode", "started", stale, nil)
	printingID := insertHistoryRow(t, path, "printing.gcode", "started", stale, &printStarted)
	activeID := insertHistoryRow(t, path, "active.gcode", "started", stale, nil)
	freshID := insertHistoryRow(t, path, "fresh.gcode", "started", now, nil)

	q := &fakeJobQueue{printing: true, activeFilename: "active.gcode"}
	s := newReaperTestService(t, d, q)

	got, err := s.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(got) != 1 || got[0] != staleID {
		t.Fatalf("RunOnce cancelled = %v, want [%d]", got, staleID)
	}

	for _, tc := range []struct {
		id   int64
		want string
	}{
		{staleID, "cancelled"},
		{printingID, "started"},
		{activeID, "started"},
		{freshID, "started"},
	} {
		entry, err := d.GetEntry(tc.id)
		if err != nil {
			t.Fatalf("GetEntry(%d): %v", tc.id, err)
		}
		if entry == nil {
			t.Fatalf("GetEntry(%d) = nil", tc.id)
		}
		if entry.Status != tc.want {
			t.Errorf("entry %d (%s) status = %q, want %q", tc.id, entry.Filename, entry.Status, tc.want)
		}
	}

	// The cancelled row records why it was cancelled.
	entry, err := d.GetEntry(staleID)
	if err != nil {
		t.Fatalf("GetEntry(%d): %v", staleID, err)
	}
	if entry.FailureReason == nil || !strings.Contains(*entry.FailureReason, "auto-cancel") {
		t.Errorf("cancelled row failure_reason = %v, want auto-cancel reason", entry.FailureReason)
	}

	// Idempotent: a second pass finds nothing left to cancel.
	again, err := s.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("second RunOnce: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("second RunOnce cancelled = %v, want none", again)
	}

	st := s.Status()
	if !st.Enabled {
		t.Error("Status().Enabled = false, want true")
	}
	if st.LastRunAt == nil {
		t.Error("Status().LastRunAt = nil, want set")
	}
	if st.NextRunAt == nil {
		t.Error("Status().NextRunAt = nil, want set")
	}
	if len(st.LastCancelledIDs) != 0 {
		t.Errorf("Status().LastCancelledIDs = %v, want empty after second run", st.LastCancelledIDs)
	}
	if st.LastError != "" {
		t.Errorf("Status().LastError = %q, want empty", st.LastError)
	}
}

func TestJobReaper_RunOnce_ClearsStalePendingStoredFile(t *testing.T) {
	unsetEnv(t, jobStartTimeoutEnv)
	d, _ := newReaperTestDB(t)

	q := &fakeJobQueue{
		pendingPath:  "/tmp/stale.gcode",
		pendingSince: time.Now().Add(-2 * time.Hour),
		pendingOK:    true,
	}
	s := newReaperTestService(t, d, q)

	if _, err := s.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if q.pendingOK {
		t.Error("stale pending stored-file start was not cleared")
	}
	if q.clearCalls != 1 {
		t.Errorf("ClearPendingStoredFileStart calls = %d, want 1", q.clearCalls)
	}

	// A fresh pending start must survive.
	q.pendingOK = true
	q.pendingSince = time.Now()
	if _, err := s.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if !q.pendingOK {
		t.Error("fresh pending stored-file start was cleared, want kept")
	}
}

func TestJobReaper_CancelStuck_ManualReset(t *testing.T) {
	unsetEnv(t, jobStartTimeoutEnv)
	d, path := newReaperTestDB(t)

	now := time.Now()
	freshID := insertHistoryRow(t, path, "fresh.gcode", "started", now, nil)
	staleID := insertHistoryRow(t, path, "stale.gcode", "started", now.Add(-2*time.Hour), nil)
	activeID := insertHistoryRow(t, path, "active.gcode", "started", now.Add(-2*time.Hour), nil)

	q := &fakeJobQueue{
		printing:       true,
		activeFilename: "active.gcode",
		pendingPath:    "/tmp/stale.gcode",
		pendingSince:   now.Add(-2 * time.Hour),
		pendingOK:      true,
	}
	s := newReaperTestService(t, d, q)

	// maxAge = 1h: only the stale row is reset; the live print is never touched.
	got, err := s.CancelStuck(context.Background(), time.Hour)
	if err != nil {
		t.Fatalf("CancelStuck: %v", err)
	}
	if len(got) != 1 || got[0] != staleID {
		t.Fatalf("CancelStuck(1h) = %v, want [%d]", got, staleID)
	}
	if q.pendingOK {
		t.Error("pending stored-file start older than maxAge was not cleared")
	}

	// maxAge <= 0: all ages, so the fresh row is reset too.
	got, err = s.CancelStuck(context.Background(), 0)
	if err != nil {
		t.Fatalf("CancelStuck(0): %v", err)
	}
	if len(got) != 1 || got[0] != freshID {
		t.Fatalf("CancelStuck(0) = %v, want [%d]", got, freshID)
	}

	for _, tc := range []struct {
		id   int64
		want string
	}{
		{freshID, "cancelled"},
		{staleID, "cancelled"},
		{activeID, "started"},
	} {
		entry, err := d.GetEntry(tc.id)
		if err != nil {
			t.Fatalf("GetEntry(%d): %v", tc.id, err)
		}
		if entry == nil {
			t.Fatalf("GetEntry(%d) = nil", tc.id)
		}
		if entry.Status != tc.want {
			t.Errorf("entry %d (%s) status = %q, want %q", tc.id, entry.Filename, entry.Status, tc.want)
		}
	}
}

func TestJobReaper_ResolveStartTimeout(t *testing.T) {
	d, _ := newReaperTestDB(t)

	newCfgMgr := func(t *testing.T, minutes int) *config.Manager {
		t.Helper()
		cfgMgr, err := config.NewManager(t.TempDir())
		if err != nil {
			t.Fatalf("config.NewManager: %v", err)
		}
		cfg := model.NewConfig(nil, []model.Printer{})
		cfg.Jobs.StartTimeoutMinutes = minutes
		if err := cfgMgr.Save(cfg); err != nil {
			t.Fatalf("cfgMgr.Save: %v", err)
		}
		return cfgMgr
	}

	t.Run("env override wins over config", func(t *testing.T) {
		unsetEnv(t, jobStartTimeoutEnv)
		t.Setenv(jobStartTimeoutEnv, "5")
		s := NewJobReaperService(d, newCfgMgr(t, 30), &fakeJobQueue{})
		if got := s.ResolveStartTimeout(); got != 5*time.Minute {
			t.Errorf("ResolveStartTimeout() = %v, want 5m", got)
		}
	})

	t.Run("zero disables auto-cancel", func(t *testing.T) {
		unsetEnv(t, jobStartTimeoutEnv)
		t.Setenv(jobStartTimeoutEnv, "0")
		s := NewJobReaperService(d, newCfgMgr(t, 30), &fakeJobQueue{})
		if got := s.ResolveStartTimeout(); got != 0 {
			t.Errorf("ResolveStartTimeout() = %v, want 0", got)
		}
		if st := s.Status(); st.Enabled {
			t.Error("Status().Enabled = true with timeout 0, want false")
		}
	})

	t.Run("config value used when env unset", func(t *testing.T) {
		unsetEnv(t, jobStartTimeoutEnv)
		s := NewJobReaperService(d, newCfgMgr(t, 30), &fakeJobQueue{})
		if got := s.ResolveStartTimeout(); got != 30*time.Minute {
			t.Errorf("ResolveStartTimeout() = %v, want 30m", got)
		}
	})

	t.Run("invalid env falls back to config", func(t *testing.T) {
		unsetEnv(t, jobStartTimeoutEnv)
		t.Setenv(jobStartTimeoutEnv, "not-a-number")
		s := NewJobReaperService(d, newCfgMgr(t, 30), &fakeJobQueue{})
		if got := s.ResolveStartTimeout(); got != 30*time.Minute {
			t.Errorf("ResolveStartTimeout() = %v, want 30m", got)
		}
	})

	t.Run("empty env treated as unset", func(t *testing.T) {
		unsetEnv(t, jobStartTimeoutEnv)
		t.Setenv(jobStartTimeoutEnv, "")
		s := NewJobReaperService(d, newCfgMgr(t, 30), &fakeJobQueue{})
		if got := s.ResolveStartTimeout(); got != 30*time.Minute {
			t.Errorf("ResolveStartTimeout() = %v, want 30m", got)
		}
	})

	t.Run("default when nothing configured", func(t *testing.T) {
		unsetEnv(t, jobStartTimeoutEnv)
		s := newReaperTestService(t, d, &fakeJobQueue{})
		if got := s.ResolveStartTimeout(); got != DefaultJobStartTimeout {
			t.Errorf("ResolveStartTimeout() = %v, want %v", got, DefaultJobStartTimeout)
		}
		if got := s.ResolveCheckInterval(); got != DefaultJobCheckInterval {
			t.Errorf("ResolveCheckInterval() = %v, want %v", got, DefaultJobCheckInterval)
		}
	})
}

func TestJobReaper_ResolveCheckInterval_Clamped(t *testing.T) {
	d, _ := newReaperTestDB(t)

	cfgMgr, err := config.NewManager(t.TempDir())
	if err != nil {
		t.Fatalf("config.NewManager: %v", err)
	}
	cfg := model.NewConfig(nil, []model.Printer{})
	cfg.Jobs.CheckIntervalSec = 5 // below the 30s floor
	if err := cfgMgr.Save(cfg); err != nil {
		t.Fatalf("cfgMgr.Save: %v", err)
	}

	s := NewJobReaperService(d, cfgMgr, &fakeJobQueue{})
	if got := s.ResolveCheckInterval(); got != 30*time.Second {
		t.Errorf("ResolveCheckInterval() = %v, want 30s", got)
	}
}

func TestJobReaper_WorkerRun_StopsOnContextCancel(t *testing.T) {
	unsetEnv(t, jobStartTimeoutEnv)
	d, _ := newReaperTestDB(t)
	s := newReaperTestService(t, d, &fakeJobQueue{})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.WorkerRun(ctx) }()
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("WorkerRun returned %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WorkerRun did not return after context cancel")
	}
}
