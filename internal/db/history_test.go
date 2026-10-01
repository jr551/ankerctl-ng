package db

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// TestRecordStart_Basic verifies that a valid filename is recorded and
// returns a non-zero ID.
func TestRecordStart_Basic(t *testing.T) {
	d := openTestDB(t)

	id, err := d.RecordStart("cube.gcode", "task-001", "", 0)
	if err != nil {
		t.Fatalf("RecordStart: %v", err)
	}
	if id == 0 {
		t.Error("expected non-zero row ID")
	}
}

// TestRecordStart_PlaceholderFilter verifies that placeholder filenames are
// skipped and return id=0 without error.
func TestRecordStart_PlaceholderFilter(t *testing.T) {
	d := openTestDB(t)

	cases := []string{"unknown", "unknown.gcode", "", "  "}
	for _, name := range cases {
		id, err := d.RecordStart(name, "", "", 0)
		if err != nil {
			t.Errorf("RecordStart(%q): unexpected error: %v", name, err)
		}
		if id != 0 {
			t.Errorf("RecordStart(%q): expected id=0 (placeholder), got %d", name, id)
		}
	}
}

// TestRecordStart_Resume verifies that a second call with the same task_id
// returns the original row ID (resume, not a new entry).
func TestRecordStart_Resume(t *testing.T) {
	d := openTestDB(t)

	id1, err := d.RecordStart("cube.gcode", "task-abc", "", 0)
	if err != nil {
		t.Fatalf("first RecordStart: %v", err)
	}

	id2, err := d.RecordStart("cube.gcode", "task-abc", "", 0)
	if err != nil {
		t.Fatalf("second RecordStart (resume): %v", err)
	}

	if id1 != id2 {
		t.Errorf("expected same row ID on resume: got %d and %d", id1, id2)
	}

	count, err := d.HistoryCount()
	if err != nil {
		t.Fatalf("HistoryCount: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 history row, got %d", count)
	}
}

// TestRecordStart_OrphanClose verifies that starting a new print with a
// different task_id marks the previous open entry as 'interrupted'.
func TestRecordStart_OrphanClose(t *testing.T) {
	d := openTestDB(t)

	_, err := d.RecordStart("first.gcode", "task-1", "", 0)
	if err != nil {
		t.Fatalf("first RecordStart: %v", err)
	}

	_, err = d.RecordStart("second.gcode", "task-2", "", 0)
	if err != nil {
		t.Fatalf("second RecordStart: %v", err)
	}

	rows, err := d.GetHistory(10, 0)
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 history rows, got %d", len(rows))
	}

	// Newest entry (id DESC) should be 'started'.
	// The older one should be 'interrupted'.
	statuses := make(map[string]int)
	for _, r := range rows {
		statuses[r.Status]++
	}
	if statuses["started"] != 1 {
		t.Errorf("expected 1 'started', got %d", statuses["started"])
	}
	if statuses["interrupted"] != 1 {
		t.Errorf("expected 1 'interrupted', got %d", statuses["interrupted"])
	}
}

// TestRecordFinish verifies status, duration, and progress are set.
func TestRecordFinish(t *testing.T) {
	d := openTestDB(t)

	_, err := d.RecordStart("cube.gcode", "task-fin", "", 0)
	if err != nil {
		t.Fatalf("RecordStart: %v", err)
	}

	time.Sleep(10 * time.Millisecond) // ensure non-zero duration

	if err := d.RecordFinish("cube.gcode", 100, "task-fin"); err != nil {
		t.Fatalf("RecordFinish: %v", err)
	}

	rows, err := d.GetHistory(1, 0)
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("no history rows returned")
	}

	r := rows[0]
	if r.Status != "finished" {
		t.Errorf("expected status='finished', got %q", r.Status)
	}
	if r.Progress != 100 {
		t.Errorf("expected progress=100, got %d", r.Progress)
	}
	if r.DurationSec == nil {
		t.Error("expected non-nil duration_sec")
	}
	if r.FinishedAt == nil {
		t.Error("expected non-nil finished_at")
	}
}

// TestRecordFail verifies failure_reason and status are set.
func TestRecordFail(t *testing.T) {
	d := openTestDB(t)

	_, err := d.RecordStart("fail_job.gcode", "task-fail", "", 0)
	if err != nil {
		t.Fatalf("RecordStart: %v", err)
	}

	if err := d.RecordFail("fail_job.gcode", "nozzle clog", "task-fail"); err != nil {
		t.Fatalf("RecordFail: %v", err)
	}

	rows, err := d.GetHistory(1, 0)
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("no history rows returned")
	}

	r := rows[0]
	if r.Status != "failed" {
		t.Errorf("expected status='failed', got %q", r.Status)
	}
	if r.FailureReason == nil || *r.FailureReason != "nozzle clog" {
		t.Errorf("expected failure_reason='nozzle clog', got %v", r.FailureReason)
	}
}

// TestClearHistory verifies that all rows are removed.
func TestClearHistory(t *testing.T) {
	d := openTestDB(t)

	for i := range 5 {
		_, err := d.RecordStart(fmt.Sprintf("job%d.gcode", i), "", "", 0)
		if err != nil {
			t.Fatalf("RecordStart %d: %v", i, err)
		}
	}

	if err := d.ClearHistory(); err != nil {
		t.Fatalf("ClearHistory: %v", err)
	}

	count, err := d.HistoryCount()
	if err != nil {
		t.Fatalf("HistoryCount: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 rows after clear, got %d", count)
	}
}

// TestGetHistory_Pagination verifies limit and offset.
func TestGetHistory_Pagination(t *testing.T) {
	d := openTestDB(t)

	// Insert 10 jobs (each new one closes the previous as 'interrupted').
	for i := range 10 {
		_, err := d.RecordStart(fmt.Sprintf("job%d.gcode", i), fmt.Sprintf("t%d", i), "", 0)
		if err != nil {
			t.Fatalf("RecordStart %d: %v", i, err)
		}
	}

	page1, err := d.GetHistory(5, 0)
	if err != nil {
		t.Fatalf("GetHistory page 1: %v", err)
	}
	if len(page1) != 5 {
		t.Errorf("expected 5 rows, got %d", len(page1))
	}

	page2, err := d.GetHistory(5, 5)
	if err != nil {
		t.Fatalf("GetHistory page 2: %v", err)
	}
	if len(page2) != 5 {
		t.Errorf("expected 5 rows on page 2, got %d", len(page2))
	}

	// IDs should be descending across pages without overlap.
	if page1[len(page1)-1].ID <= page2[0].ID {
		t.Error("pages overlap or are not in descending ID order")
	}
}

// TestRetention_90Days verifies that the 90-day prune fires during insert.
func TestRetention_90Days(t *testing.T) {
	d := openTestDB(t)

	// Manually insert a row that is 91 days old.
	oldTime := time.Now().UTC().AddDate(0, 0, -91).Format(time.RFC3339Nano)
	_, err := d.db.Exec(
		`INSERT INTO print_history (filename, status, started_at) VALUES ('old.gcode', 'finished', ?)`,
		oldTime,
	)
	if err != nil {
		t.Fatalf("insert old row: %v", err)
	}

	// Trigger prune by inserting a new record.
	_, err = d.RecordStart("new.gcode", "", "", 0)
	if err != nil {
		t.Fatalf("RecordStart: %v", err)
	}

	rows, err := d.GetHistory(100, 0)
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	for _, r := range rows {
		if r.Filename == "old.gcode" {
			t.Error("old.gcode should have been pruned by 90-day retention")
		}
	}
}

// TestRetention_500Cap verifies the 500-row max-entries cap.
func TestRetention_500Cap(t *testing.T) {
	d := openTestDB(t)

	// Insert 502 finished rows directly (bypassing RecordStart to avoid
	// orphan-close churn and to keep the test fast).
	ts := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)
	for i := range 502 {
		_, err := d.db.Exec(
			`INSERT INTO print_history (filename, status, started_at) VALUES (?, 'finished', ?)`,
			fmt.Sprintf("job%d.gcode", i), ts,
		)
		if err != nil {
			t.Fatalf("insert row %d: %v", i, err)
		}
	}

	// Trigger pruning via RecordStart.
	_, err := d.RecordStart("trigger.gcode", "", "", 0)
	if err != nil {
		t.Fatalf("RecordStart: %v", err)
	}

	count, err := d.HistoryCount()
	if err != nil {
		t.Fatalf("HistoryCount: %v", err)
	}
	// After pruning to 500 and inserting 1 new row the max should be <=500.
	if count > 500 {
		t.Errorf("expected <=500 rows after cap prune, got %d", count)
	}
}

// TestHistoryCount_Empty verifies count on a fresh DB.
func TestHistoryCount_Empty(t *testing.T) {
	d := openTestDB(t)

	count, err := d.HistoryCount()
	if err != nil {
		t.Fatalf("HistoryCount: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0, got %d", count)
	}
}

// TestRecordFinish_NoActiveEntry verifies that RecordFinish is a no-op
// when there is no open entry (no error).
func TestRecordFinish_NoActiveEntry(t *testing.T) {
	d := openTestDB(t)

	if err := d.RecordFinish("cube.gcode", 100, "no-such-task"); err != nil {
		t.Errorf("RecordFinish with no active entry: %v", err)
	}
}

// TestRecordFail_NoActiveEntry verifies that RecordFail is a no-op
// when there is no open entry (no error).
func TestRecordFail_NoActiveEntry(t *testing.T) {
	d := openTestDB(t)

	if err := d.RecordFail("cube.gcode", "reason", "no-such-task"); err != nil {
		t.Errorf("RecordFail with no active entry: %v", err)
	}
}

// insertHistoryRow inserts a row directly, bypassing RecordStart so tests can
// control status, started_at and print_started_at precisely.
func insertHistoryRow(t *testing.T, d *DB, filename, status string, startedAt time.Time, printStartedAt *time.Time) int64 {
	t.Helper()

	var printStarted any
	if printStartedAt != nil {
		printStarted = printStartedAt.UTC().Format(time.RFC3339Nano)
	}

	res, err := d.db.Exec(
		`INSERT INTO print_history (filename, status, started_at, print_started_at) VALUES (?, ?, ?, ?)`,
		filename, status, startedAt.UTC().Format(time.RFC3339Nano), printStarted,
	)
	if err != nil {
		t.Fatalf("insert history row %q: %v", filename, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("last insert id: %v", err)
	}
	return id
}

// TestMigration_PrintStartedAtColumn verifies the print_started_at column is
// added by the migration and that re-running it stays idempotent.
func TestMigration_PrintStartedAtColumn(t *testing.T) {
	d := openTestDB(t)

	cols, err := tableColumns(d.db, "print_history")
	if err != nil {
		t.Fatalf("tableColumns: %v", err)
	}
	if _, ok := cols["print_started_at"]; !ok {
		t.Fatal("print_started_at column missing from print_history")
	}

	d.mu.Lock()
	err = migrateHistory(d.db, d.log)
	d.mu.Unlock()
	if err != nil {
		t.Errorf("second migrateHistory call: %v", err)
	}
}

// TestMigration_PrintStartedAtColumn_LegacyDB verifies the ALTER TABLE path
// adds print_started_at to a database created before the column existed.
// historySchema is exactly that older schema: print_started_at only exists in
// historyMigrationColumns.
func TestMigration_PrintStartedAtColumn_LegacyDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")

	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	if _, err := legacy.Exec(historySchema); err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close legacy db: %v", err)
	}

	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open(legacy): %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	cols, err := tableColumns(d.db, "print_history")
	if err != nil {
		t.Fatalf("tableColumns: %v", err)
	}
	if _, ok := cols["print_started_at"]; !ok {
		t.Error("print_started_at column not added to legacy database")
	}
}

// TestMarkPrintStarted verifies the timestamp is set on the active row and
// never moves on repeated calls.
func TestMarkPrintStarted(t *testing.T) {
	cases := []struct {
		name     string
		filename string
		taskID   string
	}{
		{"by task id", "cube.gcode", "task-ms"},
		{"by filename only", "cube.gcode", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := openTestDB(t)

			id, err := d.RecordStart(tc.filename, tc.taskID, "", 0)
			if err != nil {
				t.Fatalf("RecordStart: %v", err)
			}

			if err := d.MarkPrintStarted(tc.filename, tc.taskID); err != nil {
				t.Fatalf("MarkPrintStarted: %v", err)
			}

			rec, err := d.GetEntry(id)
			if err != nil {
				t.Fatalf("GetEntry: %v", err)
			}
			if rec.PrintStartedAt == nil {
				t.Fatal("expected print_started_at to be set")
			}
			first := *rec.PrintStartedAt

			time.Sleep(5 * time.Millisecond)
			if err := d.MarkPrintStarted(tc.filename, tc.taskID); err != nil {
				t.Fatalf("second MarkPrintStarted: %v", err)
			}

			rec, err = d.GetEntry(id)
			if err != nil {
				t.Fatalf("GetEntry after second mark: %v", err)
			}
			if rec.PrintStartedAt == nil {
				t.Fatal("print_started_at cleared by second call")
			}
			if !rec.PrintStartedAt.Equal(first) {
				t.Errorf("print_started_at moved on second call: %v -> %v", first, *rec.PrintStartedAt)
			}
		})
	}
}

// TestMarkPrintStarted_NoActiveRow verifies the call is a silent no-op when
// there is no open entry, including when only closed rows exist.
func TestMarkPrintStarted_NoActiveRow(t *testing.T) {
	d := openTestDB(t)

	if err := d.MarkPrintStarted("cube.gcode", "no-such-task"); err != nil {
		t.Errorf("MarkPrintStarted with no active row: %v", err)
	}

	id, err := d.RecordStart("cube.gcode", "task-closed", "", 0)
	if err != nil {
		t.Fatalf("RecordStart: %v", err)
	}
	if err := d.RecordFinish("cube.gcode", 100, "task-closed"); err != nil {
		t.Fatalf("RecordFinish: %v", err)
	}

	if err := d.MarkPrintStarted("cube.gcode", "task-closed"); err != nil {
		t.Errorf("MarkPrintStarted on closed row: %v", err)
	}

	rec, err := d.GetEntry(id)
	if err != nil {
		t.Fatalf("GetEntry: %v", err)
	}
	if rec.PrintStartedAt != nil {
		t.Errorf("closed row was marked as printing: %v", *rec.PrintStartedAt)
	}
}

// TestGetHistory_PrintStartedAt verifies the field is exposed by GetHistory
// (nil while queued, set after the printer reports printing).
func TestGetHistory_PrintStartedAt(t *testing.T) {
	d := openTestDB(t)

	id, err := d.RecordStart("cube.gcode", "task-ps", "", 0)
	if err != nil {
		t.Fatalf("RecordStart: %v", err)
	}

	rows, err := d.GetHistory(1, 0)
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != id {
		t.Fatalf("unexpected rows: %+v", rows)
	}
	if rows[0].PrintStartedAt != nil {
		t.Errorf("expected nil print_started_at for queued row, got %v", *rows[0].PrintStartedAt)
	}

	if err := d.MarkPrintStarted("cube.gcode", "task-ps"); err != nil {
		t.Fatalf("MarkPrintStarted: %v", err)
	}

	rows, err = d.GetHistory(1, 0)
	if err != nil {
		t.Fatalf("GetHistory after mark: %v", err)
	}
	if rows[0].PrintStartedAt == nil {
		t.Fatal("expected print_started_at after MarkPrintStarted")
	}
}

// TestCancelEntry verifies a started row is cancelled once and that a second
// call is a no-op.
func TestCancelEntry(t *testing.T) {
	d := openTestDB(t)

	id, err := d.RecordStart("cube.gcode", "task-cancel", "", 0)
	if err != nil {
		t.Fatalf("RecordStart: %v", err)
	}
	time.Sleep(10 * time.Millisecond) // ensure non-zero duration

	changed, err := d.CancelEntry(id, "stuck")
	if err != nil {
		t.Fatalf("CancelEntry: %v", err)
	}
	if !changed {
		t.Fatal("expected the first CancelEntry to change the row")
	}

	rec, err := d.GetEntry(id)
	if err != nil {
		t.Fatalf("GetEntry: %v", err)
	}
	if rec.Status != "cancelled" {
		t.Errorf("expected status='cancelled', got %q", rec.Status)
	}
	if rec.FinishedAt == nil {
		t.Error("expected finished_at to be set")
	}
	if rec.DurationSec == nil {
		t.Error("expected duration_sec to be set")
	}
	if rec.FailureReason == nil || *rec.FailureReason != "stuck" {
		t.Errorf("expected failure_reason='stuck', got %v", rec.FailureReason)
	}

	changed, err = d.CancelEntry(id, "stuck again")
	if err != nil {
		t.Fatalf("second CancelEntry: %v", err)
	}
	if changed {
		t.Error("expected the second CancelEntry to be a no-op")
	}

	rec, err = d.GetEntry(id)
	if err != nil {
		t.Fatalf("GetEntry after second cancel: %v", err)
	}
	if rec.Status != "cancelled" || rec.FailureReason == nil || *rec.FailureReason != "stuck" {
		t.Errorf("second cancel mutated the row: status=%q reason=%v", rec.Status, rec.FailureReason)
	}
}

// TestCancelEntry_NonStartedRows verifies only status='started' rows are
// touched.
func TestCancelEntry_NonStartedRows(t *testing.T) {
	cases := []struct {
		name   string
		status string
	}{
		{"finished", "finished"},
		{"failed", "failed"},
		{"interrupted", "interrupted"},
		{"cancelled", "cancelled"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := openTestDB(t)
			id := insertHistoryRow(t, d, "x.gcode", tc.status, time.Now().UTC().Add(-time.Hour), nil)

			changed, err := d.CancelEntry(id, "nope")
			if err != nil {
				t.Fatalf("CancelEntry: %v", err)
			}
			if changed {
				t.Errorf("CancelEntry changed a %s row", tc.status)
			}

			rec, err := d.GetEntry(id)
			if err != nil {
				t.Fatalf("GetEntry: %v", err)
			}
			if rec.Status != tc.status {
				t.Errorf("expected status %q, got %q", tc.status, rec.Status)
			}
		})
	}

	t.Run("unknown id", func(t *testing.T) {
		d := openTestDB(t)
		changed, err := d.CancelEntry(4242, "nope")
		if err != nil {
			t.Fatalf("CancelEntry: %v", err)
		}
		if changed {
			t.Error("CancelEntry reported a change for an unknown id")
		}
	})
}

// TestCancelEntry_EmptyReasonStoresNull verifies an empty reason is stored as
// NULL rather than an empty string.
func TestCancelEntry_EmptyReasonStoresNull(t *testing.T) {
	d := openTestDB(t)

	id, err := d.RecordStart("cube.gcode", "task-noreason", "", 0)
	if err != nil {
		t.Fatalf("RecordStart: %v", err)
	}

	changed, err := d.CancelEntry(id, "")
	if err != nil {
		t.Fatalf("CancelEntry: %v", err)
	}
	if !changed {
		t.Fatal("expected the row to be cancelled")
	}

	rec, err := d.GetEntry(id)
	if err != nil {
		t.Fatalf("GetEntry: %v", err)
	}
	if rec.FailureReason != nil {
		t.Errorf("expected NULL failure_reason, got %q", *rec.FailureReason)
	}
}

// TestStuckStartedJobs verifies the age and print_started_at filters.
func TestStuckStartedJobs(t *testing.T) {
	d := openTestDB(t)
	now := time.Now().UTC()

	old := now.Add(-2 * time.Hour)
	fresh := now.Add(-1 * time.Minute)
	printing := now.Add(-3 * time.Hour)

	idOld := insertHistoryRow(t, d, "old.gcode", "started", old, nil)
	idFresh := insertHistoryRow(t, d, "fresh.gcode", "started", fresh, nil)
	insertHistoryRow(t, d, "printing.gcode", "started", printing, &printing)
	insertHistoryRow(t, d, "done.gcode", "finished", old, nil)

	t.Run("timeout filters fresh rows", func(t *testing.T) {
		got, err := d.StuckStartedJobs(time.Hour, now)
		if err != nil {
			t.Fatalf("StuckStartedJobs: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("expected 1 stuck row, got %d: %+v", len(got), got)
		}
		if got[0].ID != idOld || got[0].Filename != "old.gcode" {
			t.Errorf("expected old.gcode (%d), got %s (%d)", idOld, got[0].Filename, got[0].ID)
		}
	})

	t.Run("zero timeout returns all ages", func(t *testing.T) {
		got, err := d.StuckStartedJobs(0, now)
		if err != nil {
			t.Fatalf("StuckStartedJobs: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("expected 2 stuck rows, got %d: %+v", len(got), got)
		}
		// Newest first.
		if got[0].ID != idFresh || got[1].ID != idOld {
			t.Errorf("expected newest-first order [%d %d], got [%d %d]", idFresh, idOld, got[0].ID, got[1].ID)
		}
	})

	t.Run("negative timeout returns all ages", func(t *testing.T) {
		got, err := d.StuckStartedJobs(-time.Minute, now)
		if err != nil {
			t.Fatalf("StuckStartedJobs: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("expected 2 stuck rows, got %d", len(got))
		}
	})
}

// TestCancelStartedJobs verifies bulk cancellation only touches started rows
// and reports the changed count.
func TestCancelStartedJobs(t *testing.T) {
	d := openTestDB(t)
	now := time.Now().UTC()

	idA := insertHistoryRow(t, d, "a.gcode", "started", now.Add(-time.Hour), nil)
	idB := insertHistoryRow(t, d, "b.gcode", "started", now.Add(-time.Hour), nil)
	idC := insertHistoryRow(t, d, "c.gcode", "finished", now.Add(-time.Hour), nil)

	changed, err := d.CancelStartedJobs([]int64{idA, idB, idC, 9999}, "reaper")
	if err != nil {
		t.Fatalf("CancelStartedJobs: %v", err)
	}
	if changed != 2 {
		t.Errorf("expected 2 rows changed, got %d", changed)
	}

	for _, tc := range []struct {
		id     int64
		status string
	}{{idA, "cancelled"}, {idB, "cancelled"}, {idC, "finished"}} {
		rec, err := d.GetEntry(tc.id)
		if err != nil {
			t.Fatalf("GetEntry(%d): %v", tc.id, err)
		}
		if rec.Status != tc.status {
			t.Errorf("entry %d: expected status %q, got %q", tc.id, tc.status, rec.Status)
		}
	}

	// Idempotent: nothing left in 'started'.
	changed, err = d.CancelStartedJobs([]int64{idA, idB}, "reaper")
	if err != nil {
		t.Fatalf("second CancelStartedJobs: %v", err)
	}
	if changed != 0 {
		t.Errorf("expected 0 rows changed on second call, got %d", changed)
	}

	// Empty input is a no-op.
	changed, err = d.CancelStartedJobs(nil, "reaper")
	if err != nil {
		t.Fatalf("CancelStartedJobs(nil): %v", err)
	}
	if changed != 0 {
		t.Errorf("expected 0 rows changed for empty ids, got %d", changed)
	}
}

// TestCancelledRow_NotClosedByFinishOrFail verifies that RecordFinish and
// RecordFail (which use findActive on status='started') ignore a cancelled row.
func TestCancelledRow_NotClosedByFinishOrFail(t *testing.T) {
	cases := []struct {
		name  string
		close func(*DB) error
	}{
		{"RecordFinish", func(d *DB) error { return d.RecordFinish("cube.gcode", 100, "task-x") }},
		{"RecordFail", func(d *DB) error { return d.RecordFail("cube.gcode", "boom", "task-x") }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := openTestDB(t)

			id, err := d.RecordStart("cube.gcode", "task-x", "", 0)
			if err != nil {
				t.Fatalf("RecordStart: %v", err)
			}

			changed, err := d.CancelEntry(id, "stuck")
			if err != nil {
				t.Fatalf("CancelEntry: %v", err)
			}
			if !changed {
				t.Fatal("expected the row to be cancelled")
			}

			if err := tc.close(d); err != nil {
				t.Fatalf("%s after cancel: %v", tc.name, err)
			}

			rec, err := d.GetEntry(id)
			if err != nil {
				t.Fatalf("GetEntry: %v", err)
			}
			if rec.Status != "cancelled" {
				t.Errorf("expected status='cancelled', got %q", rec.Status)
			}
		})
	}
}
