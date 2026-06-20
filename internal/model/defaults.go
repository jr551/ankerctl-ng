package model

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// DefaultUploadRateMbps is the default upload speed limit.
const DefaultUploadRateMbps = 10

// UploadRateMbpsChoices are the valid upload rate options.
var UploadRateMbpsChoices = []int{5, 10, 25, 50, 100}

// ResolveUploadRateMbpsWithSource returns the effective upload rate and a source
// string indicating where the value came from: "override", "env", "config", or "default".
// This mirrors Python's cli.util.resolve_upload_rate_mbps_with_source.
func ResolveUploadRateMbpsWithSource(cfgRate int, override int) (int, string) {
	if override > 0 {
		return override, "override"
	}
	if envVal := os.Getenv("UPLOAD_RATE_MBPS"); envVal != "" {
		if rate, err := strconv.Atoi(envVal); err == nil && rate > 0 {
			return rate, "env"
		}
	}
	if cfgRate > 0 {
		return cfgRate, "config"
	}
	return DefaultUploadRateMbps, "default"
}

// AppriseEvents holds the enabled/disabled state for each notification event.
type AppriseEvents struct {
	PrintStarted  bool `json:"print_started"`
	PrintFinished bool `json:"print_finished"`
	PrintFailed   bool `json:"print_failed"`
	PrintPaused   bool `json:"print_paused"`
	PrintResumed  bool `json:"print_resumed"`
	GcodeUploaded bool `json:"gcode_uploaded"`
	PrintProgress bool `json:"print_progress"`
}

// AppriseProgress holds progress notification settings.
type AppriseProgress struct {
	IntervalPercent  int    `json:"interval_percent"`
	IncludeImage     bool   `json:"include_image"`
	SnapshotQuality  string `json:"snapshot_quality"`
	SnapshotFallback bool   `json:"snapshot_fallback"`
	SnapshotLight    bool   `json:"snapshot_light"`
	MaxValue         int    `json:"max_value,omitempty"`
}

// AppriseTemplates holds notification message templates.
type AppriseTemplates struct {
	PrintStarted  string `json:"print_started"`
	PrintFinished string `json:"print_finished"`
	PrintFailed   string `json:"print_failed"`
	PrintPaused   string `json:"print_paused"`
	PrintResumed  string `json:"print_resumed"`
	GcodeUploaded string `json:"gcode_uploaded"`
	PrintProgress string `json:"print_progress"`
}

// AppriseConfig holds all Apprise notification settings.
type AppriseConfig struct {
	Enabled   bool             `json:"enabled"`
	ServerURL string           `json:"server_url"`
	Key       string           `json:"key"`
	Tag       string           `json:"tag"`
	Events    AppriseEvents    `json:"events"`
	Progress  AppriseProgress  `json:"progress"`
	Templates AppriseTemplates `json:"templates"`
}

// NotificationsConfig wraps notification provider configs.
type NotificationsConfig struct {
	Apprise AppriseConfig `json:"apprise"`
}

// TimelapseConfig holds timelapse recording settings.
type TimelapseConfig struct {
	Enabled        bool    `json:"enabled"`
	Interval       int     `json:"interval"`
	MaxVideos      int     `json:"max_videos"`
	SavePersistent bool    `json:"save_persistent"`
	OutputDir      string  `json:"output_dir"`
	Light          *string `json:"light"` // nil = not set
}

// HomeAssistantConfig holds Home Assistant MQTT discovery settings.
type HomeAssistantConfig struct {
	Enabled         bool   `json:"enabled"`
	MQTTHost        string `json:"mqtt_host"`
	MQTTPort        int    `json:"mqtt_port"`
	MQTTUsername    string `json:"mqtt_username"`
	MQTTPassword    string `json:"mqtt_password"`
	DiscoveryPrefix string `json:"discovery_prefix"`
	NodeID          string `json:"node_id"`
}

// PrintHistoryConfig holds print history pruning settings.
// These values are read from environment variables and default to the same
// values as the hardcoded constants in internal/db.
// NOTE: the DB layer (internal/db/history.go) currently uses its own constants.
// This struct is the canonical source for future plumbing.
type PrintHistoryConfig struct {
	RetentionDays int `json:"retention_days"`
	MaxEntries    int `json:"max_entries"`
}

// FilamentServiceConfig holds filament service behaviour settings.
//
// AllowLegacySwap enables the legacy automatic unload/load swap flow.
// When false (default), the manual swap flow is used: the printer only preheats
// the nozzle to ManualSwapPreheatTempC and waits for the user to confirm.
//
// ManualSwapPreheatTempC is the preheat temperature used in manual swap mode.
// It is clamped to [130, 150]°C on read.
type FilamentServiceConfig struct {
	AllowLegacySwap        bool `json:"allow_legacy_swap"`
	ManualSwapPreheatTempC int  `json:"manual_swap_preheat_temp_c"`
}

// AppearanceConfig holds appearance/theming settings.
type AppearanceConfig struct {
	AccentColor string `json:"accent_color"`
}

// PrintersWithoutCamera lists model codes that have no built-in camera.
// Comparison is case-insensitive. Matches Python's PRINTERS_WITHOUT_CAMERA set.
var PrintersWithoutCamera = map[string]struct{}{"V8110": {}}

// CameraSourcePrinter and CameraSourceExternal are the two valid source values.
const (
	CameraSourcePrinter  = "printer"
	CameraSourceExternal = "external"
)

// Camera preset kinds. These record which "easy setup" preset the user picked
// for an external camera, so the UI can re-populate the friendly fields. The
// preset always resolves to a concrete StreamURL/SnapshotURL (derived by
// DeriveExternalCameraURLs) so the backend stays preset-agnostic.
//
// CameraKindCustom is the default/backward-compatible value: the user supplies
// the stream/snapshot URLs directly (this is the original "external" behaviour).
const (
	CameraKindCustom    = "custom"    // raw stream/snapshot URLs (advanced)
	CameraKindMJPEG     = "mjpeg"     // a single MJPEG stream URL
	CameraKindOctoPrint = "octoprint" // mjpg-streamer / OctoPrint webcam base URL
	CameraKindFrigate   = "frigate"   // Frigate NVR base URL + camera name
	CameraKindGo2RTC    = "go2rtc"    // go2rtc / MediaMTX base URL + stream name
	CameraKindReolink   = "reolink"   // Reolink host + credentials + channel
	CameraKindRTSP      = "rtsp"      // raw RTSP (needs a restreamer for browsers)
)

// CameraKinds lists every valid preset kind. Used for validation / round-trip.
var CameraKinds = []string{
	CameraKindCustom,
	CameraKindMJPEG,
	CameraKindOctoPrint,
	CameraKindFrigate,
	CameraKindGo2RTC,
	CameraKindReolink,
	CameraKindRTSP,
}

// ExternalCameraSettings holds the external camera configuration.
//
// Kind records which UI preset produced these settings (one of the CameraKind*
// constants). Fields holds the raw per-preset inputs (e.g. {"base_url":...,
// "camera":...}) so the UI can re-render the friendly form. StreamURL and
// SnapshotURL are the resolved URLs the backend actually dials; for presets
// they are derived from Kind+Fields (see DeriveExternalCameraURLs) and for the
// custom/legacy kind they are entered directly. Kind and Fields are optional
// and omitted from JSON when empty, so existing config.json files (which only
// have name/stream_url/snapshot_url/refresh_sec) load unchanged.
type ExternalCameraSettings struct {
	Name        string            `json:"name"`
	StreamURL   string            `json:"stream_url"`
	SnapshotURL string            `json:"snapshot_url"`
	RefreshSec  int               `json:"refresh_sec"`
	Kind        string            `json:"kind,omitempty"`
	Fields      map[string]string `json:"fields,omitempty"`
}

// NormalizeCameraKind returns a valid camera kind, defaulting to
// CameraKindCustom for empty/unknown values (backward compatible: old configs
// have no "kind" and behave as custom raw-URL entries).
func NormalizeCameraKind(kind string) string {
	k := strings.ToLower(strings.TrimSpace(kind))
	for _, valid := range CameraKinds {
		if k == valid {
			return valid
		}
	}
	return CameraKindCustom
}

// DeriveExternalCameraURLs computes the resolved stream and snapshot URLs for a
// preset kind given its raw fields. It is the single source of truth for URL
// derivation and is mirrored client-side in ankersrv.js (deriveCameraUrls) so
// the form can preview/auto-fill before saving; the server re-derives on save
// for any non-custom kind so a hand-edited config still resolves correctly.
//
// For CameraKindCustom (and unknown kinds) it returns empty strings, signalling
// "use the StreamURL/SnapshotURL already on the struct verbatim".
//
// Field keys per kind:
//   - mjpeg:     stream_url
//   - octoprint: base_url            -> {base}/webcam/?action=stream + ?action=snapshot
//   - frigate:   base_url, camera    -> {base}/api/{cam} (mjpeg) + {base}/api/{cam}/latest.jpg
//   - go2rtc:    base_url, stream    -> {base}/api/stream.mjpeg?src={s} + frame.jpeg
//   - reolink:   host, user, password[, channel] -> flv stream + snap cgi
//   - rtsp:      stream_url          -> passthrough (RTSP, needs restreamer)
func DeriveExternalCameraURLs(kind string, fields map[string]string) (streamURL, snapshotURL string) {
	get := func(k string) string { return strings.TrimSpace(fields[k]) }
	trimSlash := func(s string) string { return strings.TrimRight(strings.TrimSpace(s), "/") }

	switch NormalizeCameraKind(kind) {
	case CameraKindMJPEG:
		return get("stream_url"), ""

	case CameraKindRTSP:
		// Browsers cannot play RTSP directly; the UI warns the user to point at
		// a restreamer. We still store/derive it so snapshots via ffmpeg work.
		return get("stream_url"), ""

	case CameraKindOctoPrint:
		base := trimSlash(get("base_url"))
		if base == "" {
			return "", ""
		}
		return base + "/webcam/?action=stream", base + "/webcam/?action=snapshot"

	case CameraKindFrigate:
		base := trimSlash(get("base_url"))
		cam := get("camera")
		if base == "" || cam == "" {
			return "", ""
		}
		return base + "/api/" + cam, base + "/api/" + cam + "/latest.jpg"

	case CameraKindGo2RTC:
		base := trimSlash(get("base_url"))
		stream := get("stream")
		if base == "" || stream == "" {
			return "", ""
		}
		return base + "/api/stream.mjpeg?src=" + stream, base + "/api/frame.jpeg?src=" + stream

	case CameraKindReolink:
		host := trimSlash(get("host"))
		if host == "" {
			return "", ""
		}
		// Default to http:// when the user gives a bare host.
		if !strings.Contains(host, "://") {
			host = "http://" + host
		}
		channel := get("channel")
		if channel == "" {
			channel = "0"
		}
		user := get("user")
		pass := get("password")
		cred := ""
		if user != "" {
			cred = "&user=" + user + "&password=" + pass
		}
		streamURL = host + "/flv?port=1935&app=bcs&stream=channel" + channel + "_main.bcs" + cred
		snapshotURL = host + "/cgi-bin/api.cgi?cmd=Snap&channel=" + channel + "&rs=ankerctl" + cred
		return streamURL, snapshotURL

	default: // CameraKindCustom
		return "", ""
	}
}

// PrinterCameraEntry holds per-printer camera source settings.
type PrinterCameraEntry struct {
	Source   string                 `json:"source"`
	External ExternalCameraSettings `json:"external"`
}

// CameraConfig is the top-level camera configuration persisted in config.json.
// It mirrors Python's `default_camera_config`: `{"per_printer": {}}`.
type CameraConfig struct {
	PerPrinter map[string]PrinterCameraEntry `json:"per_printer"`
}

// DefaultCameraConfig returns the default camera configuration.
func DefaultCameraConfig() CameraConfig {
	return CameraConfig{PerPrinter: map[string]PrinterCameraEntry{}}
}

// DefaultExternalCameraSettings returns default external camera settings.
func DefaultExternalCameraSettings() ExternalCameraSettings {
	return ExternalCameraSettings{RefreshSec: 3}
}

// PrinterSupportsCamera returns true when the printer model has a built-in camera.
func PrinterSupportsCamera(model string) bool {
	if model == "" {
		return false
	}
	_, noCam := PrintersWithoutCamera[strings.ToUpper(model)]
	return !noCam
}

// ResolvedCameraSettings is the computed camera state returned by the API.
// Mirrors Python's resolve_camera_settings() return value.
type ResolvedCameraSettings struct {
	PrinterIndex     int                    `json:"printer_index"`
	PrinterName      string                 `json:"printer_name,omitempty"`
	PrinterSN        string                 `json:"printer_sn,omitempty"`
	Source           string                 `json:"source"`
	ConfiguredSource string                 `json:"configured_source"`
	EffectiveSource  string                 `json:"effective_source"`
	PrinterSupported bool                   `json:"printer_supported"`
	FeatureAvailable bool                   `json:"feature_available"`
	Detail           string                 `json:"detail"`
	External         ResolvedExternalCamera `json:"external"`
}

// ResolvedExternalCamera embeds ExternalCameraSettings with an added Configured flag.
type ResolvedExternalCamera struct {
	ExternalCameraSettings
	Configured bool `json:"configured"`
}

// filamentServiceManualSwapMinTempC and filamentServiceManualSwapMaxTempC are the
// clamp bounds for ManualSwapPreheatTempC.
const (
	filamentServiceManualSwapMinTempC  = 130
	filamentServiceManualSwapMaxTempC  = 150
	filamentServiceDefaultPreheatTempC = 140
)

// DefaultAppriseConfig returns the default Apprise notification configuration.
func DefaultAppriseConfig() AppriseConfig {
	return AppriseConfig{
		Enabled:   false,
		ServerURL: "",
		Key:       "",
		Tag:       "",
		Events: AppriseEvents{
			PrintStarted:  true,
			PrintFinished: true,
			PrintFailed:   true,
			PrintPaused:   true,
			PrintResumed:  true,
			GcodeUploaded: true,
			PrintProgress: true,
		},
		Progress: AppriseProgress{
			IntervalPercent:  25,
			IncludeImage:     false,
			SnapshotQuality:  "hd",
			SnapshotFallback: true,
		},
		Templates: AppriseTemplates{
			PrintStarted:  "Print started: {filename}",
			PrintFinished: "Print finished: {filename} ({duration})",
			PrintFailed:   "Print failed: {filename} ({reason})",
			PrintPaused:   "Print paused: {filename}",
			PrintResumed:  "Print resumed: {filename}",
			GcodeUploaded: "Upload complete: {filename} ({size})",
			PrintProgress: "Progress: {percent}% - {filename}",
		},
	}
}

// DefaultNotificationsConfig returns the default notifications configuration.
func DefaultNotificationsConfig() NotificationsConfig {
	return NotificationsConfig{
		Apprise: DefaultAppriseConfig(),
	}
}

// DefaultTimelapseConfig returns the default timelapse configuration,
// reading overrides from environment variables.
func DefaultTimelapseConfig() TimelapseConfig {
	light := os.Getenv("TIMELAPSE_LIGHT")
	var lightPtr *string
	if light != "" {
		lightPtr = &light
	}

	return TimelapseConfig{
		Enabled:        envBool("TIMELAPSE_ENABLED", false),
		Interval:       envInt("TIMELAPSE_INTERVAL_SEC", 30),
		MaxVideos:      envInt("TIMELAPSE_MAX_VIDEOS", 10),
		SavePersistent: envBool("TIMELAPSE_SAVE_PERSISTENT", true),
		OutputDir:      envString("TIMELAPSE_CAPTURES_DIR", defaultCapturesDir()),
		Light:          lightPtr,
	}
}

// DefaultHomeAssistantConfig returns the default Home Assistant configuration,
// reading overrides from environment variables.
func DefaultHomeAssistantConfig() HomeAssistantConfig {
	return HomeAssistantConfig{
		Enabled:         envBool("HA_MQTT_ENABLED", false),
		MQTTHost:        envString("HA_MQTT_HOST", "localhost"),
		MQTTPort:        envInt("HA_MQTT_PORT", 1883),
		MQTTUsername:    envString("HA_MQTT_USER", ""),
		MQTTPassword:    envString("HA_MQTT_PASSWORD", ""),
		DiscoveryPrefix: envString("HA_MQTT_DISCOVERY_PREFIX", "homeassistant"),
		NodeID:          "ankermake_m5",
	}
}

// DefaultPrintHistoryConfig returns the default print history configuration,
// reading overrides from environment variables.
func DefaultPrintHistoryConfig() PrintHistoryConfig {
	return PrintHistoryConfig{
		RetentionDays: envInt("PRINT_HISTORY_RETENTION_DAYS", 90),
		MaxEntries:    envInt("PRINT_HISTORY_MAX_ENTRIES", 500),
	}
}

// DefaultFilamentServiceConfig returns the default filament service configuration,
// reading overrides from environment variables.
func DefaultFilamentServiceConfig() FilamentServiceConfig {
	raw := envInt("FILAMENT_MANUAL_SWAP_PREHEAT_TEMP_C", filamentServiceDefaultPreheatTempC)
	temp := raw
	if temp < filamentServiceManualSwapMinTempC {
		temp = filamentServiceManualSwapMinTempC
	}
	if temp > filamentServiceManualSwapMaxTempC {
		temp = filamentServiceManualSwapMaxTempC
	}
	return FilamentServiceConfig{
		AllowLegacySwap:        envBool("FILAMENT_ALLOW_LEGACY_SWAP", false),
		ManualSwapPreheatTempC: temp,
	}
}

// DefaultAppearanceConfig returns the default appearance configuration.
// The default accent color is Anker green (#88f387).
func DefaultAppearanceConfig() AppearanceConfig {
	return AppearanceConfig{
		AccentColor: "#88f387",
	}
}

// ClampManualSwapPreheatTempC clamps v to [130, 150]°C.
// Use this whenever a preheat temp is read from config or user input.
func ClampManualSwapPreheatTempC(v int) int {
	if v < filamentServiceManualSwapMinTempC {
		return filamentServiceManualSwapMinTempC
	}
	if v > filamentServiceManualSwapMaxTempC {
		return filamentServiceManualSwapMaxTempC
	}
	return v
}

// defaultCapturesDir returns the default timelapse captures directory.
// Uses the user config directory (~/.config/ankerctl/captures) instead of
// the hardcoded /captures, matching Python's platformdirs-based default.
func defaultCapturesDir() string {
	if cfgDir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(cfgDir, "ankerctl", "captures")
	}
	return filepath.Join(os.Getenv("HOME"), ".config", "ankerctl", "captures")
}

// DefaultFilamentSwapAdvancedConfig returns the default advanced filament swap
// command configuration as a generic map. The map is intended to be
// JSON-serialised and stored as filament_swap_commands.json in the config dir.
//
// Mirrors Python's FILAMENT_SWAP_ADVANCED_CONFIG_DEFAULT dict and
// _default_filament_swap_commands().
func DefaultFilamentSwapAdvancedConfig() map[string]any {
	// Settings from environment variables mirror Python's env-based defaults.
	homeReadyTempC := envInt("FILAMENT_SWAP_HOME_READY_TEMP_C", 180)
	// FILAMENT_SWAP_HOME_PAUSE_S takes precedence over FILAMENT_SWAP_HOME_SETTLE_S
	homePauseS := envFloat("FILAMENT_SWAP_HOME_PAUSE_S", 0)
	if homePauseS == 0 {
		homePauseS = envFloat("FILAMENT_SWAP_HOME_SETTLE_S", 70.0)
	}
	cooldownDelayS := envFloat("FILAMENT_SWAP_COOLDOWN_DELAY_S", 0.75)

	return map[string]any{
		"version": 1,
		"description": "Advanced filament swap command templates and default settings. Edit only " +
			"if you know your printer accepts the replacement G-code. Restart is not required.",
		"settings": map[string]any{
			"allow_legacy_swap":           false,
			"manual_swap_preheat_temp_c":  180,
			"quick_move_length_mm":        40.0,
			"swap_prime_length_mm":        10.0,
			"swap_unload_length_mm":       40.0,
			"swap_load_length_mm":         120.0,
			"swap_home_pause_s":           homePauseS,
			"swap_home_ready_temp_c":      homeReadyTempC,
			"swap_z_lift_mm":              50.0,
			"swap_z_feedrate_mm_min":      600,
			"swap_prime_feedrate_mm_min":  240,
			"swap_unload_feedrate_mm_min": 2000,
			"swap_load_feedrate_mm_min":   240,
			"swap_cooldown_delay_s":       cooldownDelayS,
		},
		"commands": map[string]any{
			"set_nozzle_temp": "M104 S{temp_c}",
			"cooldown_nozzle": "M104 S0",
			"home_all":        "native:home_z",
			"relative_mode":   "G91",
			"z_lift":          "G1 Z{z_lift_mm} F{z_feedrate}",
			"wait_for_moves":  "M400",
			"absolute_mode":   "G90",
			"prime":           "M83\nG1 E{prime_length_mm} F{prime_feedrate}\nM400\nM82",
			"unload":          "M83\nG1 E-{unload_length_mm} F{unload_feedrate}\nM400\nM82",
			"load":            "M83\nG1 E{load_length_mm} F{load_feedrate}\nM400\nM82",
		},
		"available_variables": map[string]any{
			"set_nozzle_temp": []string{"temp_c"},
			"z_lift":          []string{"z_lift_mm", "z_feedrate"},
			"prime":           []string{"prime_length_mm", "prime_feedrate"},
			"unload":          []string{"unload_length_mm", "unload_feedrate"},
			"load":            []string{"load_length_mm", "load_feedrate"},
		},
	}
}

// envFloat reads an environment variable as a float64.
func envFloat(key string, defaultVal float64) float64 {
	val := os.Getenv(key)
	if val == "" {
		return defaultVal
	}
	var f float64
	var seenDot bool
	var frac float64 = 0.1
	for _, c := range val {
		if c >= '0' && c <= '9' {
			if seenDot {
				f += float64(c-'0') * frac
				frac *= 0.1
			} else {
				f = f*10 + float64(c-'0')
			}
		} else if c == '.' && !seenDot {
			seenDot = true
		} else {
			return defaultVal
		}
	}
	return f
}

// envBool reads an environment variable as a boolean.
// Recognizes "true", "1", "yes" (case-insensitive) as true.
func envBool(key string, defaultVal bool) bool {
	val := os.Getenv(key)
	if val == "" {
		return defaultVal
	}
	switch val {
	case "true", "True", "TRUE", "1", "yes", "Yes", "YES":
		return true
	}
	return false
}

// envInt reads an environment variable as an integer.
func envInt(key string, defaultVal int) int {
	val := os.Getenv(key)
	if val == "" {
		return defaultVal
	}
	var result int
	for _, c := range val {
		if c < '0' || c > '9' {
			return defaultVal
		}
		result = result*10 + int(c-'0')
	}
	return result
}

// envString reads an environment variable with a default fallback.
func envString(key, defaultVal string) string {
	val := os.Getenv(key)
	if val == "" {
		return defaultVal
	}
	return val
}
