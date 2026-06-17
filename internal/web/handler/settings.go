package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/django1982/ankerctl/internal/model"
	"github.com/django1982/ankerctl/internal/service"
	"github.com/django1982/ankerctl/internal/util"
)

// filamentSwapAdvancedConfigName is the basename (without .json) used for the
// advanced filament swap command config file stored in the config directory.
//
// (Python: FILAMENT_SWAP_ADVANCED_CONFIG_NAME = "filament_swap_commands")
const filamentSwapAdvancedConfigName = "filament_swap_commands"

// SettingsLauncherBat generates a Windows .bat launcher script for ankerctl.
//
// POST /api/settings/launcher-bat
//
// Body: {"install_dir": "C:\\path\\to\\ankerctl"}
//
// Response (200): text/plain with Content-Disposition attachment
// Error (400): missing/invalid install_dir
//
// (Python: app_api_settings_launcher_bat / _build_windows_launcher_bat)
func (h *Handler) SettingsLauncherBat(w http.ResponseWriter, r *http.Request) {
	var payload map[string]any
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload == nil {
		h.writeError(w, http.StatusBadRequest, "Invalid JSON payload")
		return
	}

	installDir, _ := payload["install_dir"].(string)
	script, err := buildWindowsLauncherBat(installDir)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="ankerctl-launcher.bat"`)
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprint(w, script)
}

// buildWindowsLauncherBat produces the .bat script content for the given
// install directory. Mirrors Python's _build_windows_launcher_bat exactly,
// including %‐escaping and CRLF line endings.
func buildWindowsLauncherBat(installDir string) (string, error) {
	installDir = strings.TrimSpace(installDir)
	if installDir == "" {
		return "", fmt.Errorf("Install directory is required.")
	}
	if strings.ContainsAny(installDir, "\"") {
		return "", fmt.Errorf("Install directory cannot contain double quotes.")
	}
	if strings.ContainsAny(installDir, "\r\n\x00") {
		return "", fmt.Errorf("Install directory cannot contain newline or null characters.")
	}

	// Escape % signs so the batch file doesn't expand them as env-var tokens.
	escapedDir := strings.ReplaceAll(installDir, "%", "%%")

	lines := []string{
		"@echo off",
		"setlocal",
		fmt.Sprintf(`set "ANKERCTL_DIR=%s"`, escapedDir),
		`cd /d "%ANKERCTL_DIR%" || (`,
		`    echo Could not open the Ankerctl folder:`,
		`    echo %ANKERCTL_DIR%`,
		"    pause",
		"    exit /b 1",
		")",
		"echo Starting ankerctl web server...",
		"where py >nul 2>&1",
		"if %errorlevel%==0 (",
		`    py .\ankerctl.py webserver run`,
		") else (",
		`    python .\ankerctl.py webserver run`,
		")",
		"echo.",
		"echo ankerctl exited.",
		"pause",
	}
	return strings.Join(lines, "\r\n") + "\r\n", nil
}

// SettingsFilamentServiceAdvancedGet reads (and creates if missing) the
// advanced filament swap command configuration file from the config directory.
//
// GET /api/settings/filament-service/advanced
//
// Response (200): {"status":"ok","path":"/path/to/file"|null,"created":bool,"config":{...}}
//
// (Python: app_api_settings_filament_service_advanced / _ensure_filament_swap_advanced_config)
func (h *Handler) SettingsFilamentServiceAdvancedGet(w http.ResponseWriter, _ *http.Request) {
	config, path, created := h.ensureFilamentSwapAdvancedConfig()
	var pathStr *string
	if path != "" {
		pathStr = &path
	}
	h.writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"path":    pathStr,
		"created": created,
		"config":  config,
	})
}

// SettingsFilamentServiceAdvancedOpen attempts to open the advanced filament
// swap config file with the system default application. On a headless server
// this is a no-op that still returns the config path and content so callers
// can display it.
//
// POST /api/settings/filament-service/advanced/open
//
// Response (200): {"status":"ok","path":"...","created":bool,"config":{...}}
// Error (500): no config path available
//
// (Python: app_api_settings_filament_service_advanced_open)
func (h *Handler) SettingsFilamentServiceAdvancedOpen(w http.ResponseWriter, _ *http.Request) {
	config, path, created := h.ensureFilamentSwapAdvancedConfig()
	if path == "" {
		h.writeError(w, http.StatusInternalServerError,
			"No local config path is available for advanced filament swap commands")
		return
	}
	// FORGE-NOTE: on a headless server there is no desktop app to open the
	// file with. We return the path + content so the frontend can display it
	// inline. The Python would call xdg-open / open / start here.
	h.writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"path":    path,
		"created": created,
		"config":  config,
	})
}

// ensureFilamentSwapAdvancedConfig reads or creates the advanced filament swap
// command config JSON file. Returns (config, path, created).
// path is empty when no config directory is available (config not loaded).
//
// Mirrors Python's _ensure_filament_swap_advanced_config.
func (h *Handler) ensureFilamentSwapAdvancedConfig() (any, string, bool) {
	defaults := model.DefaultFilamentSwapAdvancedConfig()

	if h.cfg == nil {
		return defaults, "", false
	}
	configPath := filepath.Join(h.cfg.ConfigDir(), filamentSwapAdvancedConfigName+".json")

	// If the file doesn't exist yet, write defaults and return created=true.
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		if data, err := json.MarshalIndent(defaults, "", "  "); err == nil {
			if writeErr := os.WriteFile(configPath, append(data, '\n'), 0o600); writeErr != nil {
				// Non-fatal: return defaults with path but created=false.
				return defaults, configPath, false
			}
		}
		return defaults, configPath, true
	}

	// File exists — read it and merge in any new default keys.
	data, err := os.ReadFile(configPath)
	if err != nil {
		return defaults, configPath, false
	}
	var loaded any
	if err := json.Unmarshal(data, &loaded); err != nil {
		return defaults, configPath, false
	}
	merged, changed := mergeFilamentSwapAdvancedConfig(loaded, defaults)
	if changed {
		if out, err := json.MarshalIndent(merged, "", "  "); err == nil {
			_ = os.WriteFile(configPath, append(out, '\n'), 0o600)
		}
	}
	return merged, configPath, false
}

// mergeFilamentSwapAdvancedConfig merges loaded config with defaults, adding
// any top-level keys that are missing. Returns (merged, changed).
// Mirrors Python's _merge_filament_swap_advanced_config.
func mergeFilamentSwapAdvancedConfig(loaded, defaults any) (any, bool) {
	loadedMap, ok := loaded.(map[string]any)
	if !ok {
		return defaults, true
	}
	defaultsMap, ok := defaults.(map[string]any)
	if !ok {
		return loaded, false
	}
	changed := false
	merged := make(map[string]any, len(defaultsMap))
	for k, v := range loadedMap {
		merged[k] = v
	}
	for k, v := range defaultsMap {
		if _, exists := merged[k]; !exists {
			merged[k] = v
			changed = true
		}
	}
	return merged, changed
}

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
		if v, ok := haPayload["mqtt_password"].(string); ok && strings.TrimSpace(v) == "" {
			delete(haPayload, "mqtt_password")
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

// SettingsTemperatureOverridesGet returns upload-time temperature overrides for the active printer.
func (h *Handler) SettingsTemperatureOverridesGet(w http.ResponseWriter, _ *http.Request) {
	cfg, _ := h.loadConfig()
	if cfg == nil {
		h.writeError(w, http.StatusBadRequest, "No printers configured")
		return
	}
	printer, _, _ := h.activePrinter(cfg)
	if printer == nil {
		h.writeError(w, http.StatusBadRequest, "No active printer configured")
		return
	}
	entry := temperatureOverrideEntryForPrinter(cfg, printer.SN)
	h.writeJSON(w, http.StatusOK, map[string]any{
		"temperature_overrides": entry,
		"printer_sn":            printer.SN,
		"printer_name":          printer.Name,
	})
}

// SettingsTemperatureOverridesUpdate updates upload-time temperature overrides for the active printer.
func (h *Handler) SettingsTemperatureOverridesUpdate(w http.ResponseWriter, r *http.Request) {
	var payload map[string]any
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		h.writeError(w, http.StatusBadRequest, "Invalid JSON payload")
		return
	}
	tempPayload := payload
	if raw, ok := payload["temperature_overrides"]; ok {
		m, ok := raw.(map[string]any)
		if !ok {
			h.writeError(w, http.StatusBadRequest, "Invalid temperature_overrides payload")
			return
		}
		tempPayload = m
	}

	var updated model.TemperatureOverrideEntry
	var printerSN, printerName string
	err := h.cfg.Modify(func(cfg *model.Config) (*model.Config, error) {
		if cfg == nil {
			return cfg, nil
		}
		printer, _, _ := h.activePrinter(cfg)
		if printer == nil {
			return nil, fmt.Errorf("No active printer configured")
		}
		printerSN = printer.SN
		printerName = printer.Name
		updated = temperatureOverrideEntryForPrinter(cfg, printer.SN)
		if v, ok := tempPayload["enabled"].(bool); ok {
			updated.Enabled = v
		}
		if v, ok := tempPayload["nozzle_min_temp_c"]; ok {
			n, ok := intFromAny(v)
			if !ok {
				return nil, fmt.Errorf("nozzle_min_temp_c must be an integer")
			}
			updated.NozzleMinTempC = n
		}
		if v, ok := tempPayload["bed_min_temp_c"]; ok {
			n, ok := intFromAny(v)
			if !ok {
				return nil, fmt.Errorf("bed_min_temp_c must be an integer")
			}
			updated.BedMinTempC = n
		}
		updated = model.NormalizeTemperatureOverrideEntry(updated)
		if cfg.TemperatureOverrides.PerPrinter == nil {
			cfg.TemperatureOverrides.PerPrinter = map[string]model.TemperatureOverrideEntry{}
		}
		cfg.TemperatureOverrides.PerPrinter[printer.SN] = updated
		return cfg, nil
	})
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]any{
		"status":                "ok",
		"temperature_overrides": updated,
		"printer_sn":            printerSN,
		"printer_name":          printerName,
	})
}

func temperatureOverrideEntryForPrinter(cfg *model.Config, sn string) model.TemperatureOverrideEntry {
	if cfg == nil || sn == "" || cfg.TemperatureOverrides.PerPrinter == nil {
		return model.TemperatureOverrideEntry{}
	}
	return model.NormalizeTemperatureOverrideEntry(cfg.TemperatureOverrides.PerPrinter[sn])
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
	externalConfigured := ext.StreamURL != "" || ext.SnapshotURL != "" || service.HomeAssistantCameraConfigured(ext.HomeAssistant)

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
		if service.HomeAssistantCameraConfigured(ext.HomeAssistant) {
			detail = "Using Home Assistant camera."
		} else if ext.StreamURL != "" {
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
		e.RefreshSec = model.DefaultExternalCameraSettings().RefreshSec
	}
	if e.RefreshSec > 30 {
		e.RefreshSec = 30
	}
	e.HomeAssistant.BaseURL = strings.TrimRight(strings.TrimSpace(e.HomeAssistant.BaseURL), "/")
	e.HomeAssistant.Token = strings.TrimSpace(e.HomeAssistant.Token)
	e.HomeAssistant.CameraEntityID = strings.TrimSpace(e.HomeAssistant.CameraEntityID)
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
	if v, ok := src["home_assistant"]; ok {
		var patch map[string]json.RawMessage
		if json.Unmarshal(v, &patch) == nil {
			mergeHomeAssistantCamera(&dst.HomeAssistant, patch)
		}
	}
}

func mergeHomeAssistantCamera(dst *model.HomeAssistantCameraSettings, src map[string]json.RawMessage) {
	if v, ok := src["enabled"]; ok {
		var b bool
		if json.Unmarshal(v, &b) == nil {
			dst.Enabled = b
		}
	}
	if v, ok := src["base_url"]; ok {
		var s string
		if json.Unmarshal(v, &s) == nil {
			dst.BaseURL = strings.TrimRight(strings.TrimSpace(s), "/")
		}
	}
	if v, ok := src["token"]; ok {
		var s string
		if json.Unmarshal(v, &s) == nil && strings.TrimSpace(s) != "" {
			dst.Token = strings.TrimSpace(s)
		}
	}
	if v, ok := src["camera_entity_id"]; ok {
		var s string
		if json.Unmarshal(v, &s) == nil {
			dst.CameraEntityID = strings.TrimSpace(s)
		}
	}
}
