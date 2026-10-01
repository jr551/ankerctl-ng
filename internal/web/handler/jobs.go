package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/django1982/ankerctl/internal/service"
)

// jobReaperIface is the surface the handler needs from service.JobReaperService.
// Keeping it as an interface avoids an import cycle and makes the handler testable.
type jobReaperIface interface {
	Status() service.JobReaperStatus
	CancelStuck(ctx context.Context, maxAge time.Duration) ([]int64, error)
}

// The real reaper must satisfy the handler-facing surface.
var _ jobReaperIface = (*service.JobReaperService)(nil)

// borrowJobReaper borrows the jobreaper service via the ServiceManager.
// The caller must defer h.svc.Return("jobreaper").
func (h *Handler) borrowJobReaper() (jobReaperIface, error) {
	if h == nil || h.svc == nil {
		return nil, fmt.Errorf("jobreaper unavailable")
	}
	svc, err := h.svc.Borrow("jobreaper")
	if err != nil {
		return nil, err
	}
	reaper, ok := svc.(jobReaperIface)
	if !ok {
		h.svc.Return("jobreaper")
		return nil, fmt.Errorf("jobreaper service type mismatch")
	}
	return reaper, nil
}

// tryBorrowMqttQueue borrows the mqttqueue service, reporting ok=false when the
// service is unavailable or is not an MqttQueue. Callers that get ok=true MUST
// call h.svc.Return("mqttqueue") when done.
func (h *Handler) tryBorrowMqttQueue() (*service.MqttQueue, bool) {
	if h == nil || h.svc == nil {
		return nil, false
	}
	q, err := h.borrowMqttQueue()
	if err != nil || q == nil {
		return nil, false
	}
	return q, true
}

// HistoryResetStuck cancels every stuck print-history entry — rows still in
// status='started' that never reported printing.
//
// POST /api/history/reset-stuck
//
// Body (optional): {"max_age_sec": N} — 0 or omitted means all ages.
//
// Response (200): {"status":"ok","cancelled":[int64],"count":int,"skipped_active":[int64]}
// Error (503): job reaper or database unavailable.
func (h *Handler) HistoryResetStuck(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		h.writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	reaper, err := h.borrowJobReaper()
	if err != nil {
		h.log.Warn("reset-stuck: job reaper unavailable", "err", err)
		h.writeError(w, http.StatusServiceUnavailable, "job reaper unavailable")
		return
	}
	defer h.svc.Return("jobreaper")

	var req struct {
		MaxAgeSec int `json:"max_age_sec"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}
	maxAge := time.Duration(req.MaxAgeSec) * time.Second
	if maxAge < 0 {
		maxAge = 0
	}

	// Snapshot the stuck set first so we can report which entries the reaper
	// skipped because they match the active print.
	stuck, err := h.db.StuckStartedJobs(maxAge, time.Now().UTC())
	if err != nil {
		h.log.Error("reset-stuck: StuckStartedJobs failed", "err", err)
		h.writeError(w, http.StatusInternalServerError, "failed to list stuck jobs")
		return
	}

	cancelled, err := reaper.CancelStuck(r.Context(), maxAge)
	if err != nil {
		h.log.Error("reset-stuck: CancelStuck failed", "err", err)
		h.writeError(w, http.StatusInternalServerError, "failed to cancel stuck jobs")
		return
	}
	if cancelled == nil {
		cancelled = []int64{}
	}

	cancelledSet := make(map[int64]struct{}, len(cancelled))
	for _, id := range cancelled {
		cancelledSet[id] = struct{}{}
	}
	skipped := make([]int64, 0)
	for _, rec := range stuck {
		if _, ok := cancelledSet[rec.ID]; !ok {
			skipped = append(skipped, rec.ID)
		}
	}

	h.log.Info("history: reset stuck jobs", "cancelled", len(cancelled), "skipped_active", len(skipped))
	h.writeJSON(w, http.StatusOK, map[string]any{
		"status":         "ok",
		"cancelled":      cancelled,
		"count":          len(cancelled),
		"skipped_active": skipped,
	})
}

// JobsStatus reports the current print-queue state and the auto-cancel reaper
// status.
//
// GET /api/jobs/status
//
// Response (200):
//
//	{"status":"ok",
//	 "stuck":[{"id":N,"filename":"...","age_sec":N,"started_at":"..."}],
//	 "stuck_count":N,
//	 "active_print":{"filename":"...","printing":bool},
//	 "pending_stored_file":{"path":"...","age_sec":N},
//	 "auto_cancel":{...}}
//
// Error (503): job reaper or database unavailable.
func (h *Handler) JobsStatus(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		h.writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	reaper, err := h.borrowJobReaper()
	if err != nil {
		h.log.Warn("jobs/status: job reaper unavailable", "err", err)
		h.writeError(w, http.StatusServiceUnavailable, "job reaper unavailable")
		return
	}
	defer h.svc.Return("jobreaper")

	now := time.Now().UTC()
	records, err := h.db.StuckStartedJobs(0, now)
	if err != nil {
		h.log.Error("jobs/status: StuckStartedJobs failed", "err", err)
		h.writeError(w, http.StatusInternalServerError, "failed to list stuck jobs")
		return
	}

	stuck := make([]map[string]any, 0, len(records))
	for _, rec := range records {
		startedAt := rec.StartedAt.UTC()
		ageSec := int64(now.Sub(startedAt).Seconds())
		if ageSec < 0 {
			ageSec = 0
		}
		stuck = append(stuck, map[string]any{
			"id":         rec.ID,
			"filename":   rec.Filename,
			"age_sec":    ageSec,
			"started_at": startedAt.Format(time.RFC3339),
		})
	}

	activePrint := map[string]any{"filename": "", "printing": false}
	pendingStoredFile := map[string]any{"path": "", "age_sec": int64(0)}
	if mqtt, ok := h.tryBorrowMqttQueue(); ok {
		defer h.svc.Return("mqttqueue")
		activePrint["printing"] = mqtt.IsPrinting()
		activePrint["filename"] = mqtt.ActivePrintFilename()
		if path, since, ok := mqtt.PendingStoredFileStart(); ok {
			pendingStoredFile["path"] = path
			if !since.IsZero() {
				ageSec := int64(now.Sub(since.UTC()).Seconds())
				if ageSec < 0 {
					ageSec = 0
				}
				pendingStoredFile["age_sec"] = ageSec
			}
		}
	}

	h.writeJSON(w, http.StatusOK, map[string]any{
		"status":              "ok",
		"stuck":               stuck,
		"stuck_count":         len(stuck),
		"active_print":        activePrint,
		"pending_stored_file": pendingStoredFile,
		"auto_cancel":         reaper.Status(),
	})
}
