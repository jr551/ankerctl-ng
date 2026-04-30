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

// ExternalCameraSettings holds the external camera configuration.
type ExternalCameraSettings struct {
	Name        string `json:"name"`
	StreamURL   string `json:"stream_url"`
	SnapshotURL string `json:"snapshot_url"`
	RefreshSec  int    `json:"refresh_sec"`
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
	PrinterIndex    int                    `json:"printer_index"`
	PrinterName     string                 `json:"printer_name,omitempty"`
	PrinterSN       string                 `json:"printer_sn,omitempty"`
	Source          string                 `json:"source"`
	ConfiguredSource string               `json:"configured_source"`
	EffectiveSource string                 `json:"effective_source"`
	PrinterSupported bool                  `json:"printer_supported"`
	FeatureAvailable bool                  `json:"feature_available"`
	Detail          string                 `json:"detail"`
	External        ResolvedExternalCamera `json:"external"`
}

// ResolvedExternalCamera embeds ExternalCameraSettings with an added Configured flag.
type ResolvedExternalCamera struct {
	ExternalCameraSettings
	Configured bool `json:"configured"`
}

// filamentServiceManualSwapMinTempC and filamentServiceManualSwapMaxTempC are the
// clamp bounds for ManualSwapPreheatTempC.
const (
	filamentServiceManualSwapMinTempC = 130
	filamentServiceManualSwapMaxTempC = 150
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
