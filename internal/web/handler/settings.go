package handler

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/django1982/ankerctl/internal/model"
	"github.com/django1982/ankerctl/internal/util"
)

// SettingsTimelapseGet returns timelapse settings.
func (h *Handler) SettingsTimelapseGet(w http.ResponseWriter, _ *http.Request) {
	cfg, _ := h.loadConfig()
	var tl model.TimelapseConfig
	if cfg != nil {
		tl = cfg.Timelapse
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"timelapse": tl})
}

// SettingsTimelapseUpdate updates timelapse settings.
func (h *Handler) SettingsTimelapseUpdate(w http.ResponseWriter, r *http.Request) {
	var payload map[string]any
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		h.writeError(w, http.StatusBadRequest, "Invalid JSON payload")
		return
	}
	tlPayload := payload
	if raw, ok := payload["timelapse"]; ok {
		m, ok := raw.(map[string]any)
		if !ok {
			h.writeError(w, http.StatusBadRequest, "Invalid timelapse payload")
			return
		}
		tlPayload = m
	}

	var updated model.TimelapseConfig
	err := h.cfg.Modify(func(cfg *model.Config) (*model.Config, error) {
		if cfg == nil {
			return cfg, nil
		}
		updated = cfg.Timelapse
		mergeIntoStruct(&updated, tlPayload)
		cfg.Timelapse = updated
		return cfg, nil
	})
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "failed to update timelapse settings")
		return
	}

	if tl, ok := h.timelapse(); ok {
		printerSN := ""
		if cfg, err := h.loadConfig(); err == nil {
			if p, _, _ := h.activePrinter(cfg); p != nil {
				printerSN = p.SN
			}
		}
		tl.Configure(updated, printerSN)
	}

	h.writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "timelapse": updated})
}

// SettingsMQTTGet returns HomeAssistant MQTT settings.
func (h *Handler) SettingsMQTTGet(w http.ResponseWriter, _ *http.Request) {
	cfg, _ := h.loadConfig()
	var ha model.HomeAssistantConfig
	if cfg != nil {
		ha = cfg.HomeAssistant
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"home_assistant": ha})
}

// SettingsMQTTUpdate updates HomeAssistant MQTT settings.
func (h *Handler) SettingsMQTTUpdate(w http.ResponseWriter, r *http.Request) {
	var payload map[string]any
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		h.writeError(w, http.StatusBadRequest, "Invalid JSON payload")
		return
	}
	haPayload := payload
	if raw, ok := payload["home_assistant"]; ok {
		m, ok := raw.(map[string]any)
		if !ok {
			h.writeError(w, http.StatusBadRequest, "Invalid home_assistant payload")
			return
		}
		haPayload = m
	}

	var updated model.HomeAssistantConfig
	err := h.cfg.Modify(func(cfg *model.Config) (*model.Config, error) {
		if cfg == nil {
			return cfg, nil
		}
		updated = cfg.HomeAssistant
		mergeIntoStruct(&updated, haPayload)
		cfg.HomeAssistant = updated
		return cfg, nil
	})
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "failed to update mqtt settings")
		return
	}

	if ha, ok := h.homeAssistant(); ok {
		ha.Configure(updated)
	}

	h.writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "home_assistant": updated})
}

// SettingsAppearanceGet returns the appearance settings.
func (h *Handler) SettingsAppearanceGet(w http.ResponseWriter, _ *http.Request) {
	cfg, _ := h.loadConfig()
	var app model.AppearanceConfig
	if cfg != nil {
		app = cfg.Appearance
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"appearance": app})
}

// SettingsAppearanceUpdate updates the appearance settings.
func (h *Handler) SettingsAppearanceUpdate(w http.ResponseWriter, r *http.Request) {
	var payload map[string]any
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		h.writeError(w, http.StatusBadRequest, "Invalid JSON payload")
		return
	}
	appPayload := payload
	if raw, ok := payload["appearance"]; ok {
		m, ok := raw.(map[string]any)
		if !ok {
			h.writeError(w, http.StatusBadRequest, "Invalid appearance payload")
			return
		}
		appPayload = m
	}

	var updated model.AppearanceConfig
	err := h.cfg.Modify(func(cfg *model.Config) (*model.Config, error) {
		if cfg == nil {
			return cfg, nil
		}
		updated = cfg.Appearance
		mergeIntoStruct(&updated, appPayload)
		cfg.Appearance = updated
		return cfg, nil
	})
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "failed to update appearance settings")
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "appearance": updated})
}

// SettingsCameraGet returns the resolved camera settings for the active printer.
func (h *Handler) SettingsCameraGet(w http.ResponseWriter, r *http.Request) {
	cfg, _ := h.loadConfig()
	if cfg == nil {
		h.writeError(w, http.StatusBadRequest, "No printers configured")
		return
	}
	_, idx, _ := h.activePrinter(cfg)
	resolved := resolveCameraSettings(cfg, idx)
	h.writeJSON(w, http.StatusOK, map[string]any{"camera": resolved})
}

// SettingsCameraUpdate updates the camera source and/or external camera settings.
// Body: {"source":"printer"|"external", "external":{"stream_url":"...","snapshot_url":"...","name":"...","refresh_sec":3}}
func (h *Handler) SettingsCameraUpdate(w http.ResponseWriter, r *http.Request) {
	var payload map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload == nil {
		h.writeError(w, http.StatusBadRequest, "Invalid JSON payload")
		return
	}

	// Accept both {"camera":{...}} and flat {"source":...,"external":{...}}
	cameraRaw, hasCameraKey := payload["camera"]
	var cameraPayload map[string]json.RawMessage
	if hasCameraKey {
		if err := json.Unmarshal(cameraRaw, &cameraPayload); err != nil || cameraPayload == nil {
			h.writeError(w, http.StatusBadRequest, "Invalid camera payload")
			return
		}
	} else {
		cameraPayload = payload
	}

	cfg, err := h.loadConfig()
	if err != nil || cfg == nil {
		h.writeError(w, http.StatusBadRequest, "No printers configured")
		return
	}
	printer, idx, _ := h.activePrinter(cfg)
	if printer == nil {
		h.writeError(w, http.StatusBadRequest, "No active printer configured")
		return
	}

	var updated model.ResolvedCameraSettings
	modErr := h.cfg.Modify(func(c *model.Config) (*model.Config, error) {
		entry := cameraEntryForPrinter(c, printer.SN)

		if sourceRaw, ok := cameraPayload["source"]; ok {
			var src string
			if err := json.Unmarshal(sourceRaw, &src); err == nil {
				entry.Source = normalizeCameraSource(src, entry.Source)
			}
		}

		if extRaw, ok := cameraPayload["external"]; ok {
			var extMap map[string]json.RawMessage
			if err := json.Unmarshal(extRaw, &extMap); err == nil {
				mergeExternalCamera(&entry.External, extMap)
			}
		}

		if err := util.ValidateExternalURL(entry.External.StreamURL); err != nil {
			return nil, err
		}
		if err := util.ValidateExternalURL(entry.External.SnapshotURL); err != nil {
			return nil, err
		}

		if c.Camera.PerPrinter == nil {
			c.Camera.PerPrinter = map[string]model.PrinterCameraEntry{}
		}
		c.Camera.PerPrinter[printer.SN] = entry
		updated = resolveCameraSettings(c, idx)
		return c, nil
	})
	if modErr != nil {
		h.writeError(w, http.StatusBadRequest, modErr.Error())
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "camera": updated})
}

// resolveCameraSettings mirrors Python's web.camera.resolve_camera_settings().
func resolveCameraSettings(cfg *model.Config, printerIndex int) model.ResolvedCameraSettings {
	var printer *model.Printer
	if printerIndex >= 0 && printerIndex < len(cfg.Printers) {
		p := cfg.Printers[printerIndex]
		printer = &p
	}

	var printerName, printerSN string
	var printerModel string
	if printer != nil {
		printerName = printer.Name
		printerSN = printer.SN
		printerModel = printer.Model
	}
	printerSupported := model.PrinterSupportsCamera(printerModel)

	entry := cameraEntryForPrinter(cfg, printerSN)
	source := normalizeCameraSource(entry.Source, model.CameraSourcePrinter)
	ext := normalizeExternalSettings(entry.External)
	externalConfigured := ext.StreamURL != "" || ext.SnapshotURL != ""

	effectiveSource := ""
	switch {
	case source == model.CameraSourcePrinter && printerSupported:
		effectiveSource = model.CameraSourcePrinter
	case source == model.CameraSourceExternal && externalConfigured:
		effectiveSource = model.CameraSourceExternal
	case source == model.CameraSourcePrinter && !printerSupported && externalConfigured:
		effectiveSource = model.CameraSourceExternal
	}

	var detail string
	switch {
	case effectiveSource == model.CameraSourcePrinter:
		detail = "Using the printer camera."
	case source == model.CameraSourceExternal && !externalConfigured:
		detail = "External camera is selected, but no stream or snapshot URL is configured yet."
	case !printerSupported && !externalConfigured:
		detail = "This printer does not expose a built-in camera. Configure an external feed in Setup -> Camera."
	case effectiveSource == model.CameraSourceExternal:
		if ext.StreamURL != "" {
			detail = "Using external camera live stream."
		} else {
			detail = "Using external camera snapshot preview."
		}
	default:
		detail = "No camera source is ready yet."
	}

	return model.ResolvedCameraSettings{
		PrinterIndex:     printerIndex,
		PrinterName:      printerName,
		PrinterSN:        printerSN,
		Source:           source,
		ConfiguredSource: source,
		EffectiveSource:  effectiveSource,
		PrinterSupported: printerSupported,
		FeatureAvailable: printerSupported || externalConfigured,
		Detail:           detail,
		External: model.ResolvedExternalCamera{
			ExternalCameraSettings: ext,
			Configured:             externalConfigured,
		},
	}
}

func cameraEntryForPrinter(cfg *model.Config, sn string) model.PrinterCameraEntry {
	if sn != "" {
		if entry, ok := cfg.Camera.PerPrinter[sn]; ok {
			return entry
		}
	}
	return model.PrinterCameraEntry{
		Source:   model.CameraSourcePrinter,
		External: model.DefaultExternalCameraSettings(),
	}
}

func normalizeCameraSource(val, fallback string) string {
	v := strings.ToLower(strings.TrimSpace(val))
	switch v {
	case model.CameraSourcePrinter, model.CameraSourceExternal:
		return v
	}
	if fallback == model.CameraSourcePrinter || fallback == model.CameraSourceExternal {
		return fallback
	}
	return model.CameraSourcePrinter
}

func normalizeExternalSettings(e model.ExternalCameraSettings) model.ExternalCameraSettings {
	if e.RefreshSec < 1 {
		e.RefreshSec = 3
	}
	if e.RefreshSec > 30 {
		e.RefreshSec = 30
	}
	return e
}

func mergeExternalCamera(dst *model.ExternalCameraSettings, src map[string]json.RawMessage) {
	if v, ok := src["name"]; ok {
		var s string
		if json.Unmarshal(v, &s) == nil {
			dst.Name = strings.TrimSpace(s)
		}
	}
	if v, ok := src["stream_url"]; ok {
		var s string
		if json.Unmarshal(v, &s) == nil {
			dst.StreamURL = strings.TrimSpace(s)
		}
	}
	if v, ok := src["snapshot_url"]; ok {
		var s string
		if json.Unmarshal(v, &s) == nil {
			dst.SnapshotURL = strings.TrimSpace(s)
		}
	}
	if v, ok := src["refresh_sec"]; ok {
		var n int
		if json.Unmarshal(v, &n) == nil && n >= 1 && n <= 30 {
			dst.RefreshSec = n
		}
	}
}
