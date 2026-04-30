package handler

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/django1982/ankerctl/internal/service"
)

// TimelapseList returns available timelapse files.
func (h *Handler) TimelapseList(w http.ResponseWriter, _ *http.Request) {
	tl, ok := h.timelapse()
	if !ok {
		h.writeJSON(w, http.StatusOK, map[string]any{"videos": []string{}, "enabled": false})
		return
	}
	videos, err := tl.ListVideos()
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "failed to list timelapses")
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"videos": videos, "enabled": true})
}

// TimelapseDownload returns a timelapse mp4 as attachment.
func (h *Handler) TimelapseDownload(w http.ResponseWriter, r *http.Request) {
	filename := chi.URLParam(r, "filename")
	if filename == "" || strings.Contains(filename, "..") || strings.ContainsAny(filename, `/\\`) || filepath.Base(filename) != filename {
		h.writeError(w, http.StatusBadRequest, "invalid filename")
		return
	}
	tl, ok := h.timelapse()
	if !ok {
		h.writeError(w, http.StatusServiceUnavailable, "timelapse service unavailable")
		return
	}
	path, ok := tl.GetVideoPath(filename)
	if !ok {
		h.writeError(w, http.StatusNotFound, "Video not found")
		return
	}
	// Python: send_file(..., as_attachment=False) → Content-Disposition: inline
	w.Header().Set("Content-Disposition", "inline; filename="+filename)
	w.Header().Set("Content-Type", "video/mp4")
	http.ServeFile(w, r, path)
}

// TimelapseDelete deletes a timelapse video.
func (h *Handler) TimelapseDelete(w http.ResponseWriter, r *http.Request) {
	filename := chi.URLParam(r, "filename")
	if filename == "" || strings.Contains(filename, "..") || strings.ContainsAny(filename, `/\\`) || filepath.Base(filename) != filename {
		h.writeError(w, http.StatusBadRequest, "invalid filename")
		return
	}
	tl, ok := h.timelapse()
	if !ok {
		h.writeError(w, http.StatusServiceUnavailable, "timelapse service unavailable")
		return
	}
	if !tl.DeleteVideo(filename) {
		h.writeError(w, http.StatusNotFound, "Video not found")
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// TimelapseSnapshotsList lists snapshot collections and their frames.
// GET /api/timelapse-snapshots
func (h *Handler) TimelapseSnapshotsList(w http.ResponseWriter, r *http.Request) {
	tl, ok := h.timelapse()
	if !ok {
		h.writeJSON(w, http.StatusOK, map[string]any{"collections": []any{}, "enabled": false})
		return
	}
	collections := tl.ListSnapshots()
	if collections == nil {
		collections = []service.SnapshotCollection{}
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"collections": collections, "enabled": true})
}

// TimelapseSnapshotDownload serves a single snapshot JPEG.
// GET /api/timelapse-snapshot/{collection_id}/{filename}?download=1
func (h *Handler) TimelapseSnapshotDownload(w http.ResponseWriter, r *http.Request) {
	collectionID := chi.URLParam(r, "collection_id")
	filename := chi.URLParam(r, "filename")
	if !safeName(collectionID) || !safeName(filename) {
		h.writeError(w, http.StatusBadRequest, "invalid filename")
		return
	}
	tl, ok := h.timelapse()
	if !ok {
		h.writeError(w, http.StatusServiceUnavailable, "timelapse service unavailable")
		return
	}
	p, ok := tl.GetSnapshotPath(collectionID, filename)
	if !ok {
		h.writeError(w, http.StatusNotFound, "Snapshot not found")
		return
	}
	download := r.URL.Query().Get("download")
	if download == "1" || download == "true" || download == "yes" {
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s", filename))
	} else {
		w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%s", filename))
	}
	http.ServeFile(w, r, p)
}

// TimelapseSnapshotCollectionDelete deletes an entire snapshot collection.
// DELETE /api/timelapse-snapshot/{collection_id}
func (h *Handler) TimelapseSnapshotCollectionDelete(w http.ResponseWriter, r *http.Request) {
	collectionID := chi.URLParam(r, "collection_id")
	if !safeName(collectionID) {
		h.writeError(w, http.StatusBadRequest, "invalid filename")
		return
	}
	tl, ok := h.timelapse()
	if !ok {
		h.writeError(w, http.StatusServiceUnavailable, "timelapse service unavailable")
		return
	}
	deleted, err := tl.DeleteSnapshotCollection(collectionID)
	if err != nil {
		h.writeError(w, http.StatusConflict, err.Error())
		return
	}
	if !deleted {
		h.writeError(w, http.StatusNotFound, "Snapshot collection not found")
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// TimelapseSnapshotDelete deletes a single snapshot JPG from a collection.
// DELETE /api/timelapse-snapshot/{collection_id}/{filename}
func (h *Handler) TimelapseSnapshotDelete(w http.ResponseWriter, r *http.Request) {
	collectionID := chi.URLParam(r, "collection_id")
	filename := chi.URLParam(r, "filename")
	if !safeName(collectionID) || !safeName(filename) {
		h.writeError(w, http.StatusBadRequest, "invalid filename")
		return
	}
	tl, ok := h.timelapse()
	if !ok {
		h.writeError(w, http.StatusServiceUnavailable, "timelapse service unavailable")
		return
	}
	deleted, err := tl.DeleteSnapshot(collectionID, filename)
	if err != nil {
		h.writeError(w, http.StatusConflict, err.Error())
		return
	}
	if !deleted {
		h.writeError(w, http.StatusNotFound, "Snapshot not found")
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// TimelapseCurrentStart manually starts a timelapse for the active print.
// POST /api/timelapse/current/start
func (h *Handler) TimelapseCurrentStart(w http.ResponseWriter, r *http.Request) {
	tl, ok := h.timelapse()
	if !ok {
		h.writeError(w, http.StatusServiceUnavailable, "timelapse service unavailable")
		return
	}
	filename, err := tl.ManualStart("")
	if err != nil {
		h.writeError(w, http.StatusConflict, err.Error())
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "filename": filename})
}

// TimelapseCurrentDismiss discards the resumable paused timelapse.
// POST /api/timelapse/current/dismiss
func (h *Handler) TimelapseCurrentDismiss(w http.ResponseWriter, r *http.Request) {
	tl, ok := h.timelapse()
	if !ok {
		h.writeError(w, http.StatusServiceUnavailable, "timelapse service unavailable")
		return
	}
	tl.ManualDismiss()
	h.writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// TimelapseCurrentPause pauses the active timelapse capture.
// POST /api/timelapse/current/pause
func (h *Handler) TimelapseCurrentPause(w http.ResponseWriter, r *http.Request) {
	tl, ok := h.timelapse()
	if !ok {
		h.writeError(w, http.StatusServiceUnavailable, "timelapse service unavailable")
		return
	}
	filename, err := tl.ManualPause()
	if err != nil {
		h.writeError(w, http.StatusConflict, err.Error())
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "filename": filename})
}

// TimelapseCurrentResume resumes a paused timelapse capture.
// POST /api/timelapse/current/resume
func (h *Handler) TimelapseCurrentResume(w http.ResponseWriter, r *http.Request) {
	tl, ok := h.timelapse()
	if !ok {
		h.writeError(w, http.StatusServiceUnavailable, "timelapse service unavailable")
		return
	}
	filename, err := tl.ManualResume()
	if err != nil {
		h.writeError(w, http.StatusConflict, err.Error())
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "filename": filename})
}

// TimelapseCurrentStop stops the timelapse and triggers video assembly.
// POST /api/timelapse/current/stop
func (h *Handler) TimelapseCurrentStop(w http.ResponseWriter, r *http.Request) {
	tl, ok := h.timelapse()
	if !ok {
		h.writeError(w, http.StatusServiceUnavailable, "timelapse service unavailable")
		return
	}
	filename, err := tl.ManualStop()
	if err != nil {
		h.writeError(w, http.StatusConflict, err.Error())
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "filename": filename})
}

// SnapshotCapture captures a JPEG from VideoQueue, saves it to the timelapse
// snapshot archive (if timelapse service is available), and returns it as a
// file attachment.
// GET /api/snapshot  (replaces the stub in general.go — registered here to
// keep all timelapse-related handlers in one file)
func (h *Handler) SnapshotCapture(w http.ResponseWriter, r *http.Request) {
	vq, ok := h.videoQueue()
	if !ok {
		h.writeError(w, http.StatusServiceUnavailable, "video service not available")
		return
	}

	tmp, err := os.CreateTemp("", "ankerctl_snapshot_*.jpg")
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "failed to create temp file")
		return
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	defer func() { _ = os.Remove(tmpPath) }()

	if err := vq.CaptureSnapshot(r.Context(), tmpPath); err != nil {
		h.writeError(w, http.StatusInternalServerError, fmt.Sprintf("snapshot failed: %v", err))
		return
	}

	takenAt := time.Now()

	// Best-effort: archive the snapshot so it appears in the Snapshots tab.
	if tl, tlOK := h.timelapse(); tlOK {
		// Non-fatal — still return the image to the caller even if archiving fails.
		_, _, _ = tl.SaveManualSnapshot(tmpPath, takenAt)
	}

	ts := takenAt.Format("20060102_150405")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=ankerctl_snapshot_%s.jpg", ts))
	http.ServeFile(w, r, tmpPath)
}

// safeName returns true when a URL path component is safe to use as a filename.
func safeName(s string) bool {
	return s != "" &&
		!strings.Contains(s, "..") &&
		!strings.ContainsAny(s, `/\\`) &&
		filepath.Base(s) == s
}
