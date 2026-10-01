package db

import (
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// defaultRetentionDays is the maximum age of a history entry.
const defaultRetentionDays = 90

// defaultMaxEntries is the maximum number of rows kept after pruning.
const defaultMaxEntries = 500

// placeholderFilenames are filenames that indicate the printer did not
// yet know which file it was printing. We skip recording these to avoid
// polluting the history. (Python: _PLACEHOLDER_NAMES)
var placeholderFilenames = map[string]struct{}{
	"unknown":       {},
	"unknown.gcode": {},
	"":              {},
}

// historySchema is the CREATE TABLE statement for the print_history table.
// The schema matches the Python reference exactly.
const historySchema = `
CREATE TABLE IF NOT EXISTS print_history (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    filename       TEXT    NOT NULL,
    status         TEXT    NOT NULL DEFAULT 'started',
    started_at     TEXT    NOT NULL,
    finished_at    TEXT,
    duration_sec   INTEGER,
    progress       INTEGER DEFAULT 0,
    failure_reason TEXT,
    task_id        TEXT,
    archive_relpath TEXT,
    archive_size    INTEGER,
    ai_history_json TEXT,
    notification_history_json TEXT
);`

// historyMigrationColumns are columns added after the initial release.
// Each entry is (column_name, column_definition). The migration code
// checks PRAGMA table_info and only emits ALTER TABLE when the column
// is absent. (Python: _migrate_schema)
var historyMigrationColumns = []struct {
	name string
	def  string
}{
	{"task_id", "TEXT"},
	{"archive_relpath", "TEXT"},
	{"archive_size", "INTEGER"},
	{"ai_history_json", "TEXT"},
	{"notification_history_json", "TEXT"},
	{"print_started_at", "TEXT"},
}

// PrintRecord represents a single row in the print_history table.
type PrintRecord struct {
	ID               int64
	Filename         string
	Status           string
	StartedAt        time.Time
	PrintStartedAt   *time.Time // set when the printer actually reported printing; nil = queued/submitted only
	FinishedAt       *time.Time
	DurationSec      *int64
	Progress         int
	FailureReason    *string
	TaskID           *string
	ArchiveRelpath   *string // relative filename inside the archive directory
	ArchiveSize      *int64  // byte size of the archived GCode file
	ArchiveAvailable bool    // true when the file actually exists on disk
	AIHistory        []HistoryAIResult
	NotificationLog  []HistoryNotificationResult
}

// migrateHistory creates the print_history table and applies any pending
// column additions. Safe to call multiple times; already-existing columns
// are skipped without error.
func migrateHistory(db *sql.DB, log *slog.Logger) error {
	if _, err := db.Exec(historySchema); err != nil {
		return fmt.Errorf("create print_history table: %w", err)
	}

	// Create index on task_id for fast lookup by task.
	const idxSQL = `CREATE INDEX IF NOT EXISTS idx_history_task_id ON print_history(task_id)`
	if _, err := db.Exec(idxSQL); err != nil {
		return fmt.Errorf("create task_id index: %w", err)
	}

	existing, err := tableColumns(db, "print_history")
	if err != nil {
		return fmt.Errorf("read print_history columns: %w", err)
	}

	for _, col := range historyMigrationColumns {
		if _, ok := existing[col.name]; ok {
			continue
		}
		stmt := fmt.Sprintf("ALTER TABLE print_history ADD COLUMN %s %s", col.name, col.def)
		if _, err := db.Exec(stmt); err != nil {
			// SQLite may still report "duplicate column" on a race; treat as non-fatal.
			log.Warn("history migration: add column skipped", "column", col.name, "err", err)
		} else {
			log.Info("history migration: added column", "column", col.name)
		}
	}

	return nil
}

// isPlaceholder reports whether filename should be skipped when recording
// history (Python: _PLACEHOLDER_NAMES check in record_start).
func isPlaceholder(filename string) bool {
	_, ok := placeholderFilenames[strings.TrimSpace(strings.ToLower(filename))]
	return ok
}

// RecordStart records the beginning of a print job.
//
// Returns the new row ID and nil error on success.
// Returns (0, nil) if filename is a placeholder — callers should treat
// a zero ID as "no record was created".
//
// If an open 'started' entry already exists for the given taskID it is
// resumed (same semantics as the Python record_start). Any other open
// entries are closed as 'interrupted' before the new row is inserted.
// After insert, old entries are pruned (90-day retention, 500-row cap).
//
// archiveRelpath and archiveSize are optional — pass empty/0 if the archive
// is not yet available.
//
// (Python: PrintHistory.record_start)
func (d *DB) RecordStart(filename, taskID, archiveRelpath string, archiveSize int64) (int64, error) {
	if isPlaceholder(filename) {
		d.log.Debug("history: skipping placeholder filename", "filename", filename)
		return 0, nil
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	var rowID int64

	err := d.withTx(func(tx *sql.Tx) error {
		// 1. Resume existing open entry for the same taskID.
		if taskID != "" {
			var id int64
			err := tx.QueryRow(
				`SELECT id FROM print_history WHERE status='started' AND task_id=?`,
				taskID,
			).Scan(&id)
			if err == nil {
				d.log.Info("history: resuming existing entry", "id", id, "task_id", taskID)
				rowID = id
				// Update archive info if it was not yet stored.
				if archiveRelpath != "" {
					if _, err := tx.Exec(
						`UPDATE print_history SET archive_relpath=COALESCE(archive_relpath, ?), archive_size=COALESCE(archive_size, ?) WHERE id=?`,
						archiveRelpath, archiveSize, id,
					); err != nil {
						return fmt.Errorf("update archive on resume: %w", err)
					}
				}
				return nil
			}
			if err != sql.ErrNoRows {
				return fmt.Errorf("resume lookup: %w", err)
			}
		}

		// 1b. No taskID: resume a recent open entry for the same filename. The
		// upload path and the printer's "printing" report both call RecordStart
		// for the same file with no taskID; resuming keeps ONE row (and its
		// archive) instead of orphaning the first and leaving duplicate rows.
		if taskID == "" && filename != "" {
			var id int64
			err := tx.QueryRow(
				`SELECT id FROM print_history WHERE status='started' AND filename=? ORDER BY id DESC LIMIT 1`,
				filename,
			).Scan(&id)
			if err == nil {
				rowID = id
				if archiveRelpath != "" {
					if _, err := tx.Exec(
						`UPDATE print_history SET archive_relpath=COALESCE(archive_relpath, ?), archive_size=COALESCE(archive_size, ?) WHERE id=?`,
						archiveRelpath, archiveSize, id,
					); err != nil {
						return fmt.Errorf("update archive on filename resume: %w", err)
					}
				}
				d.log.Debug("history: resuming open entry by filename", "id", id, "filename", filename)
				return nil
			}
			if err != sql.ErrNoRows {
				return fmt.Errorf("filename resume lookup: %w", err)
			}
		}

		// 2. Close any orphaned open entries.
		rows, err := tx.Query(
			`SELECT id, started_at FROM print_history WHERE status='started'`,
		)
		if err != nil {
			return fmt.Errorf("query orphans: %w", err)
		}

		type orphan struct {
			id        int64
			startedAt string
		}
		var orphans []orphan
		for rows.Next() {
			var o orphan
			if err := rows.Scan(&o.id, &o.startedAt); err != nil {
				rows.Close()
				return fmt.Errorf("scan orphan: %w", err)
			}
			orphans = append(orphans, o)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate orphans: %w", err)
		}

		if len(orphans) > 0 {
			now := time.Now().UTC()
			for _, o := range orphans {
				started, parseErr := time.Parse(time.RFC3339Nano, o.startedAt)
				if parseErr != nil {
					// Fallback: try without nanoseconds (Python stores isoformat).
					started, parseErr = time.Parse("2006-01-02T15:04:05.999999999", o.startedAt)
					if parseErr != nil {
						started = now
					}
				}
				duration := int64(now.Sub(started).Seconds())
				if _, err := tx.Exec(
					`UPDATE print_history SET status='interrupted', finished_at=?, duration_sec=? WHERE id=?`,
					now.UTC().Format(time.RFC3339Nano), duration, o.id,
				); err != nil {
					return fmt.Errorf("close orphan %d: %w", o.id, err)
				}
			}
			d.log.Info("history: marked orphaned entries as interrupted", "count", len(orphans))
		}

		// 3. Insert new entry.
		now := time.Now().UTC()
		var taskIDArg, archiveRelpathArg, archiveSizeArg any
		if taskID != "" {
			taskIDArg = taskID
		}
		if archiveRelpath != "" {
			archiveRelpathArg = archiveRelpath
			archiveSizeArg = archiveSize
		}
		res, err := tx.Exec(
			`INSERT INTO print_history (filename, status, started_at, task_id, archive_relpath, archive_size) VALUES (?, 'started', ?, ?, ?, ?)`,
			filename, now.Format(time.RFC3339Nano), taskIDArg, archiveRelpathArg, archiveSizeArg,
		)
		if err != nil {
			return fmt.Errorf("insert history row: %w", err)
		}

		rowID, err = res.LastInsertId()
		if err != nil {
			return fmt.Errorf("last insert id: %w", err)
		}

		// 4. Prune old entries.
		if err := pruneHistory(tx, defaultRetentionDays, defaultMaxEntries, d.log); err != nil {
			return fmt.Errorf("prune history: %w", err)
		}

		return nil
	})

	return rowID, err
}

// RecordFinish marks an active print as finished.
//
// The active entry is located by taskID (preferred) then filename then
// most-recent fallback, matching the Python _find_active logic.
// Progress is clamped 0–100 on insert.
//
// (Python: PrintHistory.record_finish)
func (d *DB) RecordFinish(filename string, progress int, taskID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.withTx(func(tx *sql.Tx) error {
		row, err := findActive(tx, filename, taskID)
		if err != nil {
			return fmt.Errorf("find active: %w", err)
		}
		if row == nil {
			d.log.Debug("history: no active print to finish")
			return nil
		}

		now := time.Now().UTC()
		duration := computeDuration(row.startedAt, now)

		_, err = tx.Exec(
			`UPDATE print_history SET status='finished', finished_at=?, duration_sec=?, progress=? WHERE id=?`,
			now.Format(time.RFC3339Nano), duration, progress, row.id,
		)
		return err
	})
}

// RecordFail marks an active print as failed with an optional reason.
//
// (Python: PrintHistory.record_fail)
func (d *DB) RecordFail(filename, reason, taskID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.withTx(func(tx *sql.Tx) error {
		row, err := findActive(tx, filename, taskID)
		if err != nil {
			return fmt.Errorf("find active: %w", err)
		}
		if row == nil {
			d.log.Debug("history: no active print to fail")
			return nil
		}

		now := time.Now().UTC()
		duration := computeDuration(row.startedAt, now)

		var reasonArg any
		if reason != "" {
			reasonArg = reason
		}
		_, err = tx.Exec(
			`UPDATE print_history SET status='failed', finished_at=?, duration_sec=?, failure_reason=? WHERE id=?`,
			now.Format(time.RFC3339Nano), duration, reasonArg, row.id,
		)
		return err
	})
}

// MarkPrintStarted records that the printer actually began printing the
// active entry for filename/taskID. The timestamp is written at most once
// (COALESCE) so repeated "printing" reports do not move it. It is a no-op
// when no open ('started') entry exists.
func (d *DB) MarkPrintStarted(filename, taskID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.withTx(func(tx *sql.Tx) error {
		row, err := findActive(tx, filename, taskID)
		if err != nil {
			return fmt.Errorf("find active: %w", err)
		}
		if row == nil {
			d.log.Debug("history: no active print to mark started", "filename", filename, "task_id", taskID)
			return nil
		}

		now := time.Now().UTC().Format(time.RFC3339Nano)
		if _, err := tx.Exec(
			`UPDATE print_history SET print_started_at=COALESCE(print_started_at, ?) WHERE id=?`,
			now, row.id,
		); err != nil {
			return fmt.Errorf("mark print started: %w", err)
		}
		return nil
	})
}

// CancelEntry cancels a single history entry by ID. It is idempotent: only
// rows still in status='started' are changed, so a second call returns
// (false, nil). The duration is computed from the row's started_at and an
// empty reason is stored as NULL.
func (d *DB) CancelEntry(id int64, reason string) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	var (
		changed  bool
		filename string
	)
	err := d.withTx(func(tx *sql.Tx) error {
		name, ok, err := cancelStartedRow(tx, id, reason, time.Now().UTC())
		if err != nil {
			return err
		}
		filename, changed = name, ok
		return nil
	})
	if err != nil {
		return false, err
	}
	if changed {
		d.log.Info("history: cancelled entry", "id", id, "filename", filename, "reason", reason)
	}
	return changed, nil
}

// StuckStartedJobs returns open ('started') entries that never reported
// printing (print_started_at IS NULL), newest first. When timeout > 0 only
// rows whose started_at is older than now-timeout are returned; timeout <= 0
// returns rows of all ages.
func (d *DB) StuckStartedJobs(timeout time.Duration, now time.Time) ([]PrintRecord, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	rows, err := d.db.Query(
		`SELECT id, filename, status, started_at, print_started_at, finished_at, duration_sec, progress, failure_reason, task_id, archive_relpath, archive_size, ai_history_json, notification_history_json
		   FROM print_history
		  WHERE status='started' AND print_started_at IS NULL
		  ORDER BY id DESC`,
	)
	if err != nil {
		return nil, fmt.Errorf("StuckStartedJobs query: %w", err)
	}
	defer rows.Close()

	records, err := scanHistoryRows(rows)
	if err != nil {
		return nil, err
	}
	if timeout <= 0 {
		return records, nil
	}

	// Filter in Go: started_at is stored as RFC3339Nano, whose variable-length
	// fractional part makes lexicographic SQL comparison order-unsafe.
	cutoff := now.Add(-timeout)
	stuck := records[:0]
	for _, r := range records {
		if r.StartedAt.Before(cutoff) {
			stuck = append(stuck, r)
		}
	}
	return stuck, nil
}

// CancelStartedJobs cancels the given entries that are still in
// status='started' in a single transaction and returns the number of rows
// actually changed. Unknown or already-closed IDs are ignored.
func (d *DB) CancelStartedJobs(ids []int64, reason string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	var changed int64
	err := d.withTx(func(tx *sql.Tx) error {
		now := time.Now().UTC()
		for _, id := range ids {
			_, ok, err := cancelStartedRow(tx, id, reason, now)
			if err != nil {
				return err
			}
			if ok {
				changed++
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	if changed > 0 {
		d.log.Info("history: cancelled started jobs", "requested", len(ids), "cancelled", changed, "reason", reason)
	}
	return changed, nil
}

// GetHistory returns up to limit rows from print_history, newest first,
// starting at the given offset.
//
// (Python: PrintHistory.get_history)
func (d *DB) GetHistory(limit, offset int) ([]PrintRecord, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	rows, err := d.db.Query(
		`SELECT id, filename, status, started_at, print_started_at, finished_at, duration_sec, progress, failure_reason, task_id, archive_relpath, archive_size, ai_history_json, notification_history_json
		   FROM print_history
		  ORDER BY id DESC
		  LIMIT ? OFFSET ?`,
		limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("GetHistory query: %w", err)
	}
	defer rows.Close()

	return scanHistoryRows(rows)
}

// GetEntry fetches a single print history record by ID.
// Returns nil, nil when not found.
//
// (Python: PrintHistory.get_entry)
func (d *DB) GetEntry(id int64) (*PrintRecord, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	rows, err := d.db.Query(
		`SELECT id, filename, status, started_at, print_started_at, finished_at, duration_sec, progress, failure_reason, task_id, archive_relpath, archive_size, ai_history_json, notification_history_json
		   FROM print_history
		  WHERE id=?`,
		id,
	)
	if err != nil {
		return nil, fmt.Errorf("GetEntry query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	records, err := scanHistoryRows(rows)
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, nil
	}
	return &records[0], nil
}

// SetArchiveInfo stores the archive_relpath and archive_size on an existing
// history row. This is called after the GCode file has been written to disk.
// It is a no-op if the row already has an archive path set.
//
// (Python: PrintHistory.record_start — COALESCE update path)
func (d *DB) SetArchiveInfo(rowID int64, archiveRelpath string, archiveSize int64) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.withTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(
			`UPDATE print_history SET archive_relpath=COALESCE(archive_relpath, ?), archive_size=COALESCE(archive_size, ?) WHERE id=?`,
			archiveRelpath, archiveSize, rowID,
		)
		return err
	})
}

// HistoryCount returns the total number of history entries.
//
// (Python: PrintHistory.get_count)
func (d *DB) HistoryCount() (int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	var count int64
	err := d.db.QueryRow(`SELECT COUNT(*) FROM print_history`).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("HistoryCount: %w", err)
	}
	return count, nil
}

// DeleteEntries removes specific history entries by their IDs.
// It returns the count of rows actually deleted.
// Entries that do not exist are silently ignored.
//
// (Python: PrintHistory.delete_entries)
func (d *DB) DeleteEntries(ids []int64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	var deleted int64
	err := d.withTx(func(tx *sql.Tx) error {
		// Build parameterised IN clause: DELETE FROM print_history WHERE id IN (?,?,?)
		placeholders := strings.Repeat("?,", len(ids))
		placeholders = placeholders[:len(placeholders)-1] // trim trailing comma
		query := "DELETE FROM print_history WHERE id IN (" + placeholders + ")"

		args := make([]any, len(ids))
		for i, id := range ids {
			args[i] = id
		}

		res, err := tx.Exec(query, args...)
		if err != nil {
			return fmt.Errorf("DeleteEntries: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("DeleteEntries RowsAffected: %w", err)
		}
		deleted = n
		return nil
	})
	if err != nil {
		return 0, err
	}
	d.log.Info("history: deleted entries", "requested", len(ids), "deleted", deleted)
	return deleted, nil
}

// ClearHistory deletes all entries from print_history.
//
// (Python: PrintHistory.clear)
func (d *DB) ClearHistory() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.withTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(`DELETE FROM print_history`)
		if err != nil {
			return fmt.Errorf("ClearHistory: %w", err)
		}
		d.log.Info("history: cleared all entries")
		return nil
	})
}

// --- internal helpers ---

// activeRow is the minimal data needed to compute duration when closing
// an active record.
type activeRow struct {
	id        int64
	startedAt string
}

// findActive locates the most recent open ('started') print record using
// the same three-level priority as the Python _find_active helper:
//  1. Match by taskID (strongest)
//  2. Match by filename (legacy fallback)
//  3. Any open entry, most recent first
func findActive(tx *sql.Tx, filename, taskID string) (*activeRow, error) {
	var row activeRow

	if taskID != "" {
		err := tx.QueryRow(
			`SELECT id, started_at FROM print_history WHERE status='started' AND task_id=?`,
			taskID,
		).Scan(&row.id, &row.startedAt)
		if err == nil {
			return &row, nil
		}
		if err != sql.ErrNoRows {
			return nil, fmt.Errorf("task_id lookup: %w", err)
		}
	}

	if filename != "" {
		err := tx.QueryRow(
			`SELECT id, started_at FROM print_history WHERE status='started' AND filename=? ORDER BY id DESC LIMIT 1`,
			filename,
		).Scan(&row.id, &row.startedAt)
		if err == nil {
			return &row, nil
		}
		if err != sql.ErrNoRows {
			return nil, fmt.Errorf("filename lookup: %w", err)
		}
	}

	err := tx.QueryRow(
		`SELECT id, started_at FROM print_history WHERE status='started' ORDER BY id DESC LIMIT 1`,
	).Scan(&row.id, &row.startedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("fallback lookup: %w", err)
	}
	return &row, nil
}

// cancelStartedRow cancels a single row if it is still in status='started'.
// It returns the row's filename (for logging) and whether a row was changed.
// Callers must hold d.mu and run inside a transaction.
func cancelStartedRow(tx *sql.Tx, id int64, reason string, now time.Time) (string, bool, error) {
	var filename, startedAt string
	err := tx.QueryRow(
		`SELECT filename, started_at FROM print_history WHERE id=? AND status='started'`,
		id,
	).Scan(&filename, &startedAt)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("lookup entry %d: %w", id, err)
	}

	var reasonArg any
	if reason != "" {
		reasonArg = reason
	}

	res, err := tx.Exec(
		`UPDATE print_history SET status='cancelled', finished_at=?, duration_sec=?, failure_reason=? WHERE id=? AND status='started'`,
		now.UTC().Format(time.RFC3339Nano), computeDuration(startedAt, now), reasonArg, id,
	)
	if err != nil {
		return "", false, fmt.Errorf("cancel entry %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return "", false, fmt.Errorf("cancel entry %d rows affected: %w", id, err)
	}
	return filename, n > 0, nil
}

// pruneHistory removes entries older than retentionDays and trims the
// total count to maxEntries. (Python: PrintHistory._prune)
func pruneHistory(tx *sql.Tx, retentionDays, maxEntries int, log *slog.Logger) error {
	if retentionDays > 0 {
		cutoff := time.Now().UTC().AddDate(0, 0, -retentionDays).Format(time.RFC3339Nano)
		res, err := tx.Exec(`DELETE FROM print_history WHERE started_at < ?`, cutoff)
		if err != nil {
			return fmt.Errorf("retention prune: %w", err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			log.Info("history: pruned old entries", "count", n, "retention_days", retentionDays)
		}
	}

	if maxEntries > 0 {
		res, err := tx.Exec(`
			DELETE FROM print_history WHERE id NOT IN (
				SELECT id FROM print_history ORDER BY id DESC LIMIT ?
			)`, maxEntries,
		)
		if err != nil {
			return fmt.Errorf("cap prune: %w", err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			log.Info("history: trimmed entries to cap", "removed", n, "max_entries", maxEntries)
		}
	}

	return nil
}

// historyTimeLayouts are the timestamp formats written by ankerctl
// (RFC3339Nano) and by the Python reference (isoformat).
var historyTimeLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02T15:04:05.999999999",
	"2006-01-02T15:04:05",
}

// parseHistoryTime parses a stored history timestamp, reporting false when
// the value matches none of the known layouts.
func parseHistoryTime(s string) (time.Time, bool) {
	for _, layout := range historyTimeLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// computeDuration returns the number of whole seconds between startedAt
// (stored as RFC3339Nano or Python isoformat) and now.
func computeDuration(startedAt string, now time.Time) int64 {
	t, ok := parseHistoryTime(startedAt)
	if !ok {
		return 0
	}
	dur := now.Sub(t).Seconds()
	if dur < 0 {
		return 0
	}
	return int64(dur)
}

// scanHistoryRows reads all rows from a *sql.Rows into a slice of PrintRecord.
func scanHistoryRows(rows *sql.Rows) ([]PrintRecord, error) {
	var records []PrintRecord
	for rows.Next() {
		var (
			r                PrintRecord
			printStartedAtS  sql.NullString
			finishedAtS      sql.NullString
			durationSec      sql.NullInt64
			failureReason    sql.NullString
			taskID           sql.NullString
			archiveRelpath   sql.NullString
			archiveSize      sql.NullInt64
			aiHistoryJSON    sql.NullString
			notifHistoryJSON sql.NullString
			startedAtS       string
		)
		if err := rows.Scan(
			&r.ID, &r.Filename, &r.Status,
			&startedAtS,
			&printStartedAtS,
			&finishedAtS,
			&durationSec,
			&r.Progress,
			&failureReason,
			&taskID,
			&archiveRelpath,
			&archiveSize,
			&aiHistoryJSON,
			&notifHistoryJSON,
		); err != nil {
			return nil, fmt.Errorf("scan history row: %w", err)
		}

		// Parse started_at (always present).
		if t, ok := parseHistoryTime(startedAtS); ok {
			r.StartedAt = t
		}

		if printStartedAtS.Valid {
			if t, ok := parseHistoryTime(printStartedAtS.String); ok {
				r.PrintStartedAt = &t
			}
		}

		if finishedAtS.Valid {
			if t, ok := parseHistoryTime(finishedAtS.String); ok {
				r.FinishedAt = &t
			}
		}
		if durationSec.Valid {
			r.DurationSec = &durationSec.Int64
		}
		if failureReason.Valid {
			r.FailureReason = &failureReason.String
		}
		if taskID.Valid {
			r.TaskID = &taskID.String
		}
		if archiveRelpath.Valid {
			r.ArchiveRelpath = &archiveRelpath.String
		}
		if archiveSize.Valid {
			r.ArchiveSize = &archiveSize.Int64
		}
		if aiHistoryJSON.Valid {
			r.AIHistory = decodeAIHistory(aiHistoryJSON.String)
		}
		if notifHistoryJSON.Valid {
			r.NotificationLog = decodeNotificationHistory(notifHistoryJSON.String)
		}

		records = append(records, r)
	}
	return records, rows.Err()
}
