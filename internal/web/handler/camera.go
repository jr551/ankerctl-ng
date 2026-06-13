package handler

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/django1982/ankerctl/internal/model"
	"github.com/django1982/ankerctl/internal/service"
)

const (
	cameraDefaultMJPEGFPS     = 5
	cameraDefaultMJPEGQuality = 5

	cameraMaxMJPEGFPS     = 30
	cameraMaxMJPEGQuality = 31

	cameraDefaultStreamWidth  = 1280
	cameraDefaultStreamHeight = 720

	cameraMultipartBoundary = "frame"
)

// CameraFrame returns a single JPEG snapshot from the active camera source.
// GET /api/camera/frame
//
// Response: image/jpeg, Content-Disposition: inline.
// Errors are returned as JSON with the appropriate HTTP status.
func (h *Handler) CameraFrame(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.loadConfig()
	if err != nil || cfg == nil {
		h.writeError(w, http.StatusBadRequest, "No printers configured")
		return
	}
	_, idx, _ := h.activePrinter(cfg)
	resolved := resolveCameraSettings(cfg, idx)
	effective := resolveEffectiveSourceWithOverride(resolved, r.URL.Query().Get("source"))

	if effective == "" {
		detail := resolved.Detail
		if detail == "" {
			detail = "No camera source is available."
		}
		h.writeError(w, http.StatusBadRequest, detail)
		return
	}

	tmp, err := os.CreateTemp("", "ankerctl_camera_*.jpg")
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "failed to create temp file")
		return
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	defer func() { _ = os.Remove(tmpPath) }()

	switch effective {
	case model.CameraSourcePrinter:
		vq, ok := h.videoQueue()
		if !ok {
			h.writeError(w, http.StatusServiceUnavailable, "video service not available")
			return
		}
		if err := vq.CaptureSnapshot(r.Context(), tmpPath); err != nil {
			h.writeError(w, http.StatusInternalServerError, fmt.Sprintf("snapshot failed: %v", err))
			return
		}
	case model.CameraSourceExternal:
		haCfg := resolved.External.HomeAssistant
		if service.HomeAssistantCameraConfigured(haCfg) {
			if err := service.HomeAssistantCameraSnapshot(r.Context(), haCfg, tmpPath); err != nil {
				h.writeError(w, http.StatusBadGateway, fmt.Sprintf("home assistant camera snapshot failed: %v", err))
				return
			}
			break
		}
		input := externalSnapshotInputURL(resolved)
		if input == "" {
			h.writeError(w, http.StatusBadRequest, "External camera is selected, but no stream or snapshot URL is configured.")
			return
		}
		if err := service.SnapshotExternal(r.Context(), input, tmpPath); err != nil {
			h.writeError(w, http.StatusBadGateway, fmt.Sprintf("external camera snapshot failed: %v", err))
			return
		}
	default:
		h.writeError(w, http.StatusBadRequest, "Unsupported camera source")
		return
	}

	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Content-Disposition", "inline; filename=\"camera.jpg\"")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeFile(w, r, tmpPath)
}

// CameraStream serves a multipart/x-mixed-replace MJPEG stream from the active
// camera source. Each HTTP request spawns its own ffmpeg process; there is no
// fan-out hub. When the client disconnects (request context cancelled) ffmpeg
// is terminated and all goroutines exit.
//
// GET /api/camera/stream?fps=5&quality=5&source=auto|printer|external
func (h *Handler) CameraStream(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.loadConfig()
	if err != nil || cfg == nil {
		h.writeError(w, http.StatusBadRequest, "No printers configured")
		return
	}
	_, idx, _ := h.activePrinter(cfg)
	resolved := resolveCameraSettings(cfg, idx)
	effective := resolveEffectiveSourceWithOverride(resolved, r.URL.Query().Get("source"))

	if effective == "" {
		detail := resolved.Detail
		if detail == "" {
			detail = "No camera source is available."
		}
		h.writeError(w, http.StatusBadRequest, detail)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		h.writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	fps := parseClampedInt(r.URL.Query().Get("fps"), cameraDefaultMJPEGFPS, 1, cameraMaxMJPEGFPS)
	quality := parseClampedInt(r.URL.Query().Get("quality"), cameraDefaultMJPEGQuality, 1, cameraMaxMJPEGQuality)
	scale := service.MJPEGScale{cameraDefaultStreamWidth, cameraDefaultStreamHeight}
	ctx := r.Context()

	var cmd *exec.Cmd
	switch effective {
	case model.CameraSourcePrinter:
		videoURL := h.printerVideoLoopbackURL()
		apiKey := h.resolveAPIKey()
		cmd = service.PrinterMJPEGCmd(ctx, videoURL, apiKey, fps, quality, scale)
	case model.CameraSourceExternal:
		haCfg := resolved.External.HomeAssistant
		if service.HomeAssistantCameraConfigured(haCfg) {
			input := service.HomeAssistantCameraStreamURL(haCfg)
			if input == "" {
				h.writeError(w, http.StatusBadRequest, "Home Assistant camera stream is not configured.")
				return
			}
			cmd = service.ExternalMJPEGCmdWithHeaders(ctx, input, map[string]string{
				"Authorization": "Bearer " + haCfg.Token,
			}, scale)
			break
		}
		input := externalStreamInputURL(resolved)
		if input == "" {
			h.writeError(w, http.StatusBadRequest, "External camera is selected, but no stream URL is configured.")
			return
		}
		cmd = service.ExternalMJPEGCmd(ctx, input, scale)
	default:
		h.writeError(w, http.StatusBadRequest, "Unsupported camera source")
		return
	}

	h.streamMJPEG(ctx, w, flusher, cmd)
}

// streamMJPEG runs the prepared ffmpeg command, reads MJPEG frames, and writes
// them to the client as a multipart/x-mixed-replace response. It returns when
// the request context is cancelled, when ffmpeg exits, or when a write to the
// client fails. ffmpeg is bound to ctx via exec.CommandContext, so the shared
// cancellation path cleanly terminates both the process and its reader goroutine.
func (h *Handler) streamMJPEG(ctx context.Context, w http.ResponseWriter, flusher http.Flusher, cmd *exec.Cmd) {
	frames, err := service.ReadMJPEGFrames(ctx, cmd)
	if err != nil {
		// Headers haven't been written yet — surface a clean HTTP error.
		http.Error(w, fmt.Sprintf("stream start failed: %v", err), http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary="+cameraMultipartBoundary)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	for {
		select {
		case <-ctx.Done():
			return
		case frame, ok := <-frames:
			if !ok {
				return
			}
			if err := writeMultipartFrame(w, frame); err != nil {
				h.logCameraStreamError(err)
				return
			}
			flusher.Flush()
		}
	}
}

// writeMultipartFrame writes a single MJPEG part (boundary + headers + body)
// to w using the standard multipart/x-mixed-replace framing.
func writeMultipartFrame(w http.ResponseWriter, frame []byte) error {
	header := "--" + cameraMultipartBoundary + "\r\n" +
		"Content-Type: image/jpeg\r\n" +
		"Cache-Control: no-store\r\n" +
		"Content-Length: " + strconv.Itoa(len(frame)) + "\r\n\r\n"
	if _, err := w.Write([]byte(header)); err != nil {
		return err
	}
	if _, err := w.Write(frame); err != nil {
		return err
	}
	if _, err := w.Write([]byte("\r\n")); err != nil {
		return err
	}
	return nil
}

// resolveEffectiveSourceWithOverride applies an explicit ?source=... override
// to a resolved camera settings struct. The override may be "printer",
// "external", "auto" or empty. Unrecognised values fall through to the
// resolved effective source.
func resolveEffectiveSourceWithOverride(resolved model.ResolvedCameraSettings, override string) string {
	override = strings.ToLower(strings.TrimSpace(override))
	switch override {
	case model.CameraSourcePrinter:
		if resolved.PrinterSupported {
			return model.CameraSourcePrinter
		}
		return ""
	case model.CameraSourceExternal:
		if resolved.External.Configured {
			return model.CameraSourceExternal
		}
		return ""
	}
	return resolved.EffectiveSource
}

// externalSnapshotInputURL returns the preferred input URL for a single-frame
// snapshot capture (snapshot_url first, falling back to stream_url).
func externalSnapshotInputURL(resolved model.ResolvedCameraSettings) string {
	if u := strings.TrimSpace(resolved.External.SnapshotURL); u != "" {
		return u
	}
	return strings.TrimSpace(resolved.External.StreamURL)
}

// externalStreamInputURL returns the URL to use for live MJPEG streaming.
func externalStreamInputURL(resolved model.ResolvedCameraSettings) string {
	return strings.TrimSpace(resolved.External.StreamURL)
}

// printerVideoLoopbackURL returns the local /video URL used as ffmpeg input
// when streaming the printer's H.264 feed. The API key is sent as an HTTP
// header (see PrinterMJPEGCmd) rather than embedded in the URL, so the URL
// itself contains no secrets.
func (h *Handler) printerVideoLoopbackURL() string {
	host := strings.TrimSpace(os.Getenv("ANKERCTL_HOST"))
	if host == "" {
		host = strings.TrimSpace(os.Getenv("FLASK_HOST"))
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}

	port := strings.TrimSpace(os.Getenv("ANKERCTL_PORT"))
	if port == "" {
		port = strings.TrimSpace(os.Getenv("FLASK_PORT"))
	}
	if port == "" {
		port = "4470"
	}
	if _, err := strconv.Atoi(port); err != nil {
		port = "4470"
	}
	return fmt.Sprintf("http://%s:%s/video?for_timelapse=1", host, port)
}

// resolveAPIKey returns the effective API key, or "" on any error.
func (h *Handler) resolveAPIKey() string {
	if h.cfg == nil {
		return ""
	}
	key, err := h.cfg.ResolveAPIKey()
	if err != nil {
		return ""
	}
	return key
}

// logCameraStreamError logs an MJPEG streaming failure at debug level. These
// are typically client disconnects or ffmpeg terminations — normal lifecycle
// events that should not produce log noise.
func (h *Handler) logCameraStreamError(err error) {
	if h.log == nil || err == nil {
		return
	}
	h.log.Debug("camera stream write failed", "err", err)
}

// parseClampedInt parses raw as an integer and clamps it to [min, max].
// Returns def when raw is empty or unparsable.
func parseClampedInt(raw string, def, minVal, maxVal int) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return def
	}
	if n < minVal {
		return minVal
	}
	if n > maxVal {
		return maxVal
	}
	return n
}
