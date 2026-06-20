package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"
)

// SliceCheck runs an AI sanity check on a rendered slice-preview image (sent by
// the Slice tab) — efficient because only the small preview image is sent, never
// the gcode.
//
// POST /api/slice/check  body: {"image":"data:image/...;base64,..."}
// Returns {"serious":bool,"issue":string}, or {"skipped":true} when no AI
// provider is configured or the check could not run (the UI then proceeds).
func (h *Handler) SliceCheck(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Image string `json:"image"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 12<<20)).Decode(&body); err != nil || body.Image == "" {
		h.writeError(w, http.StatusBadRequest, "missing image")
		return
	}
	pm, ok := h.printMonitor()
	if !ok {
		h.writeJSON(w, http.StatusOK, map[string]any{"skipped": true})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	res, ran, err := pm.AnalyzeSliceImage(ctx, body.Image)
	if err != nil || !ran {
		resp := map[string]any{"skipped": true}
		if err != nil {
			resp["error"] = err.Error()
		}
		h.writeJSON(w, http.StatusOK, resp)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"serious": res.Serious, "issue": res.Issue})
}
