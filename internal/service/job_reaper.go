package service

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/django1982/ankerctl/internal/config"
	"github.com/django1982/ankerctl/internal/db"
)

const (
	// DefaultJobStartTimeout is how long a print job may remain queued
	// ('started' with no reported print start) before the reaper cancels it.
	DefaultJobStartTimeout = time.Hour

	// DefaultJobCheckInterval is how often the reaper scans for stuck jobs.
	DefaultJobCheckInterval = 5 * time.Minute

	// minJobCheckInterval is the lower clamp for the configured check interval.
	minJobCheckInterval = 30 * time.Second

	// jobStartTimeoutEnv overrides the configured start timeout with an
	// integer number of minutes. 0 disables auto-cancel.
	jobStartTimeoutEnv = "ANKERCTL_JOB_START_TIMEOUT_MIN"

	// jobReaperCancelReason is the failure_reason stored on operator resets.
	jobReaperCancelReason = "cancelled: stuck job reset by operator"
)

// JobReaperStatus is the JSON-serialisable status of the job reaper.
type JobReaperStatus struct {
	Enabled          bool       `json:"enabled"`
	StartTimeoutSec  int        `json:"start_timeout_sec"`
	CheckIntervalSec int        `json:"check_interval_sec"`
	LastRunAt        *time.Time `json:"last_run_at,omitempty"`
	NextRunAt        *time.Time `json:"next_run_at,omitempty"`
	LastCancelledIDs []int64    `json:"last_cancelled_ids,omitempty"`
	LastError        string     `json:"last_error,omitempty"`
}

// JobQueueController is the subset of the MQTT queue the job reaper needs to
// avoid cancelling a print that is actually running.
type JobQueueController interface {
	// IsPrinting reports whether the printer is currently printing or paused.
	IsPrinting() bool
	// ActivePrintFilename returns the filename of the active print, if known.
	ActivePrintFilename() string
	// PendingStoredFileStart reports a stored file that was sent to the
	// printer but has not been reported as printing yet, along with the time
	// it was queued.
	PendingStoredFileStart() (path string, since time.Time, ok bool)
	// ClearPendingStoredFileStart forgets the pending stored-file start.
	// Returns true when there was one to clear.
	ClearPendingStoredFileStart() bool
}

// JobReaperService cancels print jobs that never actually start printing.
//
// A job is recorded as 'started' as soon as it is submitted to the printer.
// If the printer never reports an actual print start (print_started_at stays
// NULL) within the configured timeout, the job is auto-cancelled so it no
// longer blocks the queue or shows as an active print in the UI.
//
// The start timeout is resolved from (highest priority first):
//  1. the ANKERCTL_JOB_START_TIMEOUT_MIN environment variable (integer
//     minutes; 0 disables auto-cancel),
//  2. config.json "jobs.start_timeout_minutes" when > 0,
//  3. DefaultJobStartTimeout.
type JobReaperService struct {
	BaseWorker

	mu      sync.Mutex
	log     *slog.Logger
	history *db.DB
	cfgMgr  *config.Manager
	printer JobQueueController

	lastRunAt        *time.Time
	nextRunAt        *time.Time
	lastCancelledIDs []int64
	lastError        string
}

// NewJobReaperService creates a JobReaperService.
func NewJobReaperService(history *db.DB, cfgMgr *config.Manager, printer JobQueueController) *JobReaperService {
	s := &JobReaperService{
		BaseWorker: NewBaseWorker("jobreaper"),
		log:        slog.With("service", "jobreaper"),
		history:    history,
		cfgMgr:     cfgMgr,
		printer:    printer,
	}
	s.BindHooks(s)
	return s
}

// ResolveStartTimeout returns the effective start timeout.
//
// ANKERCTL_JOB_START_TIMEOUT_MIN (integer minutes, 0 = disabled) wins over
// config.json "jobs.start_timeout_minutes"; DefaultJobStartTimeout is used
// when neither is set to a positive value.
func (s *JobReaperService) ResolveStartTimeout() time.Duration {
	if raw := strings.TrimSpace(os.Getenv(jobStartTimeoutEnv)); raw != "" {
		if mins, err := strconv.Atoi(raw); err == nil && mins >= 0 {
			return time.Duration(mins) * time.Minute
		}
		if s.log != nil {
			s.log.Warn("invalid job start timeout override ignored",
				"env", jobStartTimeoutEnv, "value", raw)
		}
	}
	if s.cfgMgr != nil {
		if cfg, err := s.cfgMgr.Load(); err == nil && cfg != nil && cfg.Jobs.StartTimeoutMinutes > 0 {
			return time.Duration(cfg.Jobs.StartTimeoutMinutes) * time.Minute
		}
	}
	return DefaultJobStartTimeout
}

// ResolveCheckInterval returns the effective scan interval, clamped to at
// least 30 seconds.
func (s *JobReaperService) ResolveCheckInterval() time.Duration {
	sec := 0
	if s.cfgMgr != nil {
		if cfg, err := s.cfgMgr.Load(); err == nil && cfg != nil {
			sec = cfg.Jobs.CheckIntervalSec
		}
	}
	if sec <= 0 {
		return DefaultJobCheckInterval
	}
	interval := time.Duration(sec) * time.Second
	if interval < minJobCheckInterval {
		interval = minJobCheckInterval
	}
	return interval
}

// Status returns the current reaper status.
func (s *JobReaperService) Status() JobReaperStatus {
	timeout := s.ResolveStartTimeout()
	interval := s.ResolveCheckInterval()

	s.mu.Lock()
	defer s.mu.Unlock()
	return JobReaperStatus{
		Enabled:          timeout > 0,
		StartTimeoutSec:  int(timeout / time.Second),
		CheckIntervalSec: int(interval / time.Second),
		LastRunAt:        cloneTimePtr(s.lastRunAt),
		NextRunAt:        cloneTimePtr(s.nextRunAt),
		LastCancelledIDs: append([]int64(nil), s.lastCancelledIDs...),
		LastError:        s.lastError,
	}
}

// WorkerRun ticks at the configured check interval and runs the same logic as
// RunOnce. Tick errors are recorded in LastError and never terminate the loop.
func (s *JobReaperService) WorkerRun(ctx context.Context) error {
	for {
		timer := time.NewTimer(s.ResolveCheckInterval())
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
			if _, err := s.RunOnce(ctx); err != nil && s.log != nil {
				s.log.Warn("job reaper run failed", "err", err)
			}
		}
	}
}

// RunOnce performs one auto-cancel pass.
//
// It cancels every 'started' history row that has no print_started_at and is
// older than the resolved start timeout, except the row matching the active
// print while the printer is printing. A pending stored-file start older than
// the timeout is cleared as well.
func (s *JobReaperService) RunOnce(ctx context.Context) ([]int64, error) {
	timeout := s.ResolveStartTimeout()
	now := time.Now()

	s.mu.Lock()
	s.lastRunAt = &now
	next := now.Add(s.ResolveCheckInterval())
	s.nextRunAt = &next
	s.mu.Unlock()

	if timeout <= 0 {
		// Auto-cancel disabled.
		s.mu.Lock()
		s.lastCancelledIDs = nil
		s.lastError = ""
		s.mu.Unlock()
		return nil, nil
	}

	var (
		cancelled []int64
		runErr    error
	)

	rows, err := s.history.StuckStartedJobs(timeout, now)
	if err != nil {
		runErr = fmt.Errorf("list stuck jobs: %w", err)
	} else {
		ids := s.filterActivePrint(rows)
		if len(ids) > 0 {
			reason := fmt.Sprintf("auto-cancel: no print start within %s", timeout)
			if _, err := s.history.CancelStartedJobs(ids, reason); err != nil {
				runErr = fmt.Errorf("cancel stuck jobs: %w", err)
			} else {
				cancelled = ids
				if s.log != nil {
					s.log.Info("auto-cancelled stuck print jobs", "ids", ids, "reason", reason)
				}
			}
		}
	}

	// A stored file that was sent to the printer but never reported as
	// printing leaves the queue waiting forever; clear it once it is stale.
	if s.printer != nil {
		if path, since, ok := s.printer.PendingStoredFileStart(); ok && now.Sub(since) > timeout {
			if s.printer.ClearPendingStoredFileStart() && s.log != nil {
				s.log.Info("cleared stale pending stored-file start",
					"path", path, "age", now.Sub(since).Round(time.Second).String())
			}
		}
	}

	s.mu.Lock()
	s.lastCancelledIDs = cancelled
	if runErr != nil {
		s.lastError = runErr.Error()
	} else {
		s.lastError = ""
	}
	s.mu.Unlock()

	return cancelled, runErr
}

// CancelStuck is the manual operator path: it cancels every stuck 'started'
// row regardless of the configured timeout. maxAge <= 0 means "all ages";
// otherwise only rows older than maxAge are cancelled. A pending stored-file
// start is cleared when maxAge <= 0 or when it is older than maxAge.
func (s *JobReaperService) CancelStuck(ctx context.Context, maxAge time.Duration) ([]int64, error) {
	now := time.Now()

	rows, err := s.history.StuckStartedJobs(maxAge, now)
	if err != nil {
		return nil, fmt.Errorf("list stuck jobs: %w", err)
	}

	var cancelled []int64
	if ids := s.filterActivePrint(rows); len(ids) > 0 {
		if _, err := s.history.CancelStartedJobs(ids, jobReaperCancelReason); err != nil {
			return nil, fmt.Errorf("cancel stuck jobs: %w", err)
		}
		cancelled = ids
		if s.log != nil {
			s.log.Info("operator reset stuck print jobs", "ids", ids, "reason", jobReaperCancelReason)
		}
	}

	if s.printer != nil {
		if path, since, ok := s.printer.PendingStoredFileStart(); ok {
			age := now.Sub(since)
			if maxAge <= 0 || age > maxAge {
				if s.printer.ClearPendingStoredFileStart() && s.log != nil {
					s.log.Info("cleared stale pending stored-file start",
						"path", path, "age", age.Round(time.Second).String())
				}
			}
		}
	}

	return cancelled, nil
}

// filterActivePrint drops the row matching the live print so the reaper can
// never cancel a print that is actually running. Filenames are compared by
// base name: the MQTT report may carry a stored-file path while the history
// row carries the bare filename.
func (s *JobReaperService) filterActivePrint(rows []db.PrintRecord) []int64 {
	activeBase := ""
	if s.printer != nil && s.printer.IsPrinting() {
		if name := strings.TrimSpace(s.printer.ActivePrintFilename()); name != "" {
			activeBase = filepath.Base(name)
		}
	}
	ids := make([]int64, 0, len(rows))
	for _, row := range rows {
		if activeBase != "" && filepath.Base(row.Filename) == activeBase {
			continue
		}
		ids = append(ids, row.ID)
	}
	return ids
}
