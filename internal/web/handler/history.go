package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/django1982/ankerctl/internal/db"
	"github.com/django1982/ankerctl/internal/model"
)

// thumbnailRelpath returns the conventional thumbnail path for an archive relpath.
// Mirrors GCodeArchiver.ThumbnailRelpath — kept here to avoid a service import in handler.
func thumbnailRelpath(archiveRelpath string) string {
	if archiveRelpath == "" {
		return ""
	}
	return archiveRelpath + ".thumbnail.png"
}

// historyEntryJSON converts a PrintRecord into the full JSON shape expected by
// the Python reference (web/__init__.py app_api_history).
//
// Python fields added beyond the original Go stub:
//   - thumbnail_url:        "/api/history/{id}/thumbnail" or null
//   - thumbnail_available:  bool — true when the thumbnail file exists on disk
//   - printer_name:         null (Phase C will add per-printer isolation)
//   - archive_relpath:      pass-through from DB row
//   - archive_size:         pass-through from DB row
//   - failure_reason:       pass-through from DB row
func historyEntryJSON(rec *db.PrintRecord, archiver GCodeArchiverIface) map[string]any {
	row := map[string]any{
		"id":              rec.ID,
		"filename":        rec.Filename,
		"status":          rec.Status,
		"started_at":      rec.StartedAt,
		"finished_at":     rec.FinishedAt,
		"duration_sec":    rec.DurationSec,
		"progress":        rec.Progress,
		// Extended fields matching Python response shape
		"printer_name":    nil, // FORGE-NOTE: per-printer isolation deferred to Phase C
		"archive_relpath": rec.ArchiveRelpath,
		"archive_size":    rec.ArchiveSize,
		"failure_reason":  rec.FailureReason,
	}

	// thumbnail_available: true when the thumbnail file actually exists on disk.
	// thumbnail_url: absolute URL path to the thumbnail endpoint, or null.
	thumbAvailable := false
	if archiver != nil && rec.ArchiveRelpath != nil && *rec.ArchiveRelpath != "" {
		thumbRelpath := thumbnailRelpath(*rec.ArchiveRelpath)
		if archiver.Exists(thumbRelpath) {
			thumbAvailable = true
		}
	}
	row["thumbnail_available"] = thumbAvailable
	if thumbAvailable {
		row["thumbnail_url"] = fmt.Sprintf("/api/history/%d/thumbnail", rec.ID)
	} else {
		row["thumbnail_url"] = nil
	}

	return row
}

// HistoryList returns print history.
// Response shape matches Python: {"entries": [...], "total": N}.
func (h *Handler) HistoryList(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		h.writeJSON(w, http.StatusOK, map[string]any{"entries": []any{}, "total": 0})
		return
	}
	limit := 50
	offset := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			offset = n
		}
	}
	if limit < 1 {
		limit = 1
	}
	if limit > 500 {
		limit = 500
	}
	if offset < 0 {
		offset = 0
	}

	records, err := h.db.GetHistory(limit, offset)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "failed to load history")
		return
	}
	total, err := h.db.HistoryCount()
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "failed to count history")
		return
	}

	entries := make([]map[string]any, 0, len(records))
	for _, rec := range records {
		row := historyEntryJSON(&rec, h.gcodeArchiver)
		entries = append(entries, row)
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"entries": entries, "total": total})
}

// HistoryReprint re-sends a previously uploaded GCode file to the printer.
//
// POST /api/history/{id}/reprint
//
// Response (200): {"status":"ok","name":"<filename>","upload_rate_mbps":N,"upload_rate_source":"<src>"}
// Error (404): entry not found, or archive file missing on disk.
// Error (409): printer is already printing.
// Error (503): PPPP / file-transfer service unavailable.
//
// (Python: app_api_history_reprint)
func (h *Handler) HistoryReprint(w http.ResponseWriter, r *http.Request) {
	idRaw := chi.URLParam(r, "id")
	entryID, err := strconv.ParseInt(idRaw, 10, 64)
	if err != nil || entryID <= 0 {
		h.writeError(w, http.StatusBadRequest, "invalid history entry id")
		return
	}

	if h.db == nil {
		h.writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	if h.gcodeArchiver == nil {
		h.writeError(w, http.StatusServiceUnavailable, "gcode archive not configured")
		return
	}

	entry, err := h.db.GetEntry(entryID)
	if err != nil {
		h.log.Error("reprint: GetEntry failed", "id", entryID, "err", err)
		h.writeError(w, http.StatusInternalServerError, "failed to load history entry")
		return
	}
	if entry == nil {
		h.writeError(w, http.StatusNotFound, "history entry not found")
		return
	}

	if entry.ArchiveRelpath == nil || !h.gcodeArchiver.Exists(*entry.ArchiveRelpath) {
		h.writeError(w, http.StatusNotFound, "no archived GCode is available for this history entry")
		return
	}

	archiveBytes, err := h.gcodeArchiver.ReadArchive(*entry.ArchiveRelpath)
	if err != nil {
		h.log.Error("reprint: ReadArchive failed", "relpath", *entry.ArchiveRelpath, "err", err)
		h.writeError(w, http.StatusInternalServerError, "failed to read archived GCode")
		return
	}

	cfg, _ := h.loadConfig()
	userID := ""
	rateLimit := 10
	if cfg != nil {
		if cfg.Account != nil {
			userID = cfg.Account.UserID
		}
		if cfg.UploadRateMbps > 0 {
			rateLimit = cfg.UploadRateMbps
		}
	}

	// Pre-flight: refuse reprint when the printer is already busy.
	// Python: if mqtt.is_printing or mqtt.has_pending_print_start → 409
	// Placed after DB/archive validation so 404/500 take precedence over 503/409.
	mqtt, mqttOK := h.mqttQueue()
	if !mqttOK {
		h.writeError(w, http.StatusServiceUnavailable, "MQTT service unavailable — printer not connected")
		return
	}
	if mqtt.IsPrinting() {
		h.writeError(w, http.StatusConflict, "printer is busy")
		return
	}

	// Borrow services (same pattern as SlicerUpload).
	if _, err := h.svc.Borrow("ppppservice"); err != nil {
		h.writeError(w, http.StatusServiceUnavailable, "pppp service unavailable")
		return
	}
	defer h.svc.Return("ppppservice")

	if _, err := h.svc.Borrow("filetransfer"); err != nil {
		h.writeError(w, http.StatusServiceUnavailable, "file transfer service unavailable")
		return
	}
	defer h.svc.Return("filetransfer")

	ft, ok := h.fileTransfer()
	if !ok {
		h.writeError(w, http.StatusServiceUnavailable, "file transfer service unavailable")
		return
	}

	userName := strings.TrimSpace(r.UserAgent())
	if userName == "" {
		userName = "ankerctl"
	}

	// start_print=true — reprint always triggers a print start.
	if err := ft.SendFile(r.Context(), entry.Filename, userName, userID, archiveBytes, rateLimit, true); err != nil {
		h.log.Error("reprint: SendFile failed", "filename", entry.Filename, "err", err)
		h.writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}

	cfgRate := 0
	if cfg != nil {
		cfgRate = cfg.UploadRateMbps
	}
	effectiveRate, rateSource := model.ResolveUploadRateMbpsWithSource(cfgRate, 0)
	h.writeJSON(w, http.StatusOK, map[string]any{
		"status":             "ok",
		"name":               entry.Filename,
		"upload_rate_mbps":   effectiveRate,
		"upload_rate_source": rateSource,
	})
}

// HistoryThumbnail serves the PNG thumbnail for a history entry.
//
// GET /api/history/{id}/thumbnail
//
// Returns 200 image/png when a thumbnail was archived with the GCode file.
// Returns 404 when the entry does not exist, has no archive, or no thumbnail was found.
//
// Python reference: web/__init__.py app_api_history_thumbnail
func (h *Handler) HistoryThumbnail(w http.ResponseWriter, r *http.Request) {
	idRaw := chi.URLParam(r, "id")
	entryID, err := strconv.ParseInt(idRaw, 10, 64)
	if err != nil || entryID <= 0 {
		h.writeError(w, http.StatusBadRequest, "invalid history entry id")
		return
	}

	if h.db == nil {
		http.NotFound(w, r)
		return
	}
	if h.gcodeArchiver == nil {
		http.NotFound(w, r)
		return
	}

	entry, err := h.db.GetEntry(entryID)
	if err != nil {
		h.log.Error("thumbnail: GetEntry failed", "id", entryID, "err", err)
		h.writeError(w, http.StatusInternalServerError, "failed to load history entry")
		return
	}
	if entry == nil || entry.ArchiveRelpath == nil {
		http.NotFound(w, r)
		return
	}

	thumbBytes, err := h.gcodeArchiver.ReadThumbnail(*entry.ArchiveRelpath)
	if err != nil {
		h.log.Error("thumbnail: ReadThumbnail failed", "relpath", *entry.ArchiveRelpath, "err", err)
		h.writeError(w, http.StatusInternalServerError, "failed to read thumbnail")
		return
	}
	if len(thumbBytes) == 0 {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Content-Length", strconv.Itoa(len(thumbBytes)))
	w.Header().Set("Cache-Control", "max-age=86400")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(thumbBytes)
}

// HistoryDeleteSelected deletes specific print history entries by ID.
//
// POST /api/history/delete
//
// Body: {"ids": [1, 2, 3]}
//
// Response (200): {"status":"ok","deleted":N,"requested":N}
// Error (400): ids not a list, or contains non-integers, or empty after filtering.
// Error (409): at least one selected entry is still in progress.
// Error (503): database unavailable.
//
// (Python: app_api_history_delete_selected)
func (h *Handler) HistoryDeleteSelected(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		h.writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}

	var payload struct {
		IDs []any `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload.IDs == nil {
		h.writeError(w, http.StatusBadRequest, "ids must be a list of history entry ids")
		return
	}

	// Validate and de-duplicate IDs, matching Python's coercion logic.
	seen := make(map[int64]struct{}, len(payload.IDs))
	entryIDs := make([]int64, 0, len(payload.IDs))
	for _, raw := range payload.IDs {
		var id int64
		switch v := raw.(type) {
		case float64:
			id = int64(v)
		case json.Number:
			n, err := v.Int64()
			if err != nil {
				h.writeError(w, http.StatusBadRequest, "ids must contain integers")
				return
			}
			id = n
		default:
			h.writeError(w, http.StatusBadRequest, "ids must contain integers")
			return
		}
		if id <= 0 {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		entryIDs = append(entryIDs, id)
	}
	if len(entryIDs) == 0 {
		h.writeError(w, http.StatusBadRequest, "No history entries were selected")
		return
	}

	// Refuse to delete any entry that is still in progress.
	for _, id := range entryIDs {
		entry, err := h.db.GetEntry(id)
		if err != nil {
			h.writeError(w, http.StatusInternalServerError, "failed to load history entry")
			return
		}
		if entry != nil && entry.Status == "started" {
			h.writeError(w, http.StatusConflict, "Cannot delete an in-progress history entry")
			return
		}
	}

	deleted, err := h.db.DeleteEntries(entryIDs)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "failed to delete history entries")
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]any{
		"status":    "ok",
		"deleted":   deleted,
		"requested": len(entryIDs),
	})
}

// HistoryClear clears print history.
func (h *Handler) HistoryClear(w http.ResponseWriter, _ *http.Request) {
	if h.db == nil {
		h.writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	if err := h.db.ClearHistory(); err != nil {
		h.writeError(w, http.StatusInternalServerError, "failed to clear history")
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
