package handler

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
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

// FilamentDetectColor detects the dominant filament colour from a camera frame
// via the vision model, so the Filaments form can suggest a matching library
// colour.
//
// POST /api/filament/detect-color  body: {"image":"data:image/...;base64,..."}
// Returns {"hex":"#RRGGBB"} or {"skipped":true}.
func (h *Handler) FilamentDetectColor(w http.ResponseWriter, r *http.Request) {
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
	hex, ran, err := pm.DetectFilamentColor(ctx, body.Image)
	if err != nil || !ran {
		resp := map[string]any{"skipped": true}
		if err != nil {
			resp["error"] = err.Error()
		}
		h.writeJSON(w, http.StatusOK, resp)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"hex": hex})
}

// OpenscadEdit rewrites OpenSCAD source for a natural-language prompt, with
// optional reference images, via the vision/text model.
//
// POST /api/openscad/edit  body: {"scad":"...","prompt":"...","images":["data:..."]}
// Returns {"scad":"<updated code>"} or {"skipped":true}.
func (h *Handler) OpenscadEdit(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Scad   string   `json:"scad"`
		Prompt string   `json:"prompt"`
		Images []string `json:"images"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 24<<20)).Decode(&body); err != nil || strings.TrimSpace(body.Prompt) == "" {
		h.writeError(w, http.StatusBadRequest, "missing prompt")
		return
	}
	pm, ok := h.printMonitor()
	if !ok {
		h.writeJSON(w, http.StatusOK, map[string]any{"skipped": true})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	code, ran, err := pm.EditOpenSCAD(ctx, body.Scad, body.Prompt, body.Images)
	if err != nil || !ran {
		resp := map[string]any{"skipped": true}
		if err != nil {
			resp["error"] = err.Error()
		}
		h.writeJSON(w, http.StatusOK, resp)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"scad": code})
}
