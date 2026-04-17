package handler

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/django1982/ankerctl/internal/model"
)

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
		row := map[string]any{
			"id":           rec.ID,
			"filename":     rec.Filename,
			"status":       rec.Status,
			"started_at":   rec.StartedAt,
			"finished_at":  rec.FinishedAt,
			"duration_sec": rec.DurationSec,
			"progress":     rec.Progress,
		}
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
