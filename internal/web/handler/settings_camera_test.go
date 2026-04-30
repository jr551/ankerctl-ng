package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/django1982/ankerctl/internal/config"
	"github.com/django1982/ankerctl/internal/db"
	"github.com/django1982/ankerctl/internal/model"
)

// newTestHandlerWithConfig creates a handler backed by a config manager pre-seeded
// with cfg written to a temp directory.
func newTestHandlerWithConfig(t *testing.T, cfg *model.Config) *Handler {
	t.Helper()
	cfgDir := t.TempDir()
	cfgMgr, err := config.NewManager(cfgDir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := cfgMgr.Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return New(cfgMgr, database, nil, nil, false, func(http.ResponseWriter, string, any) error { return nil })
}

func TestResolveCameraSettings_PrinterSource(t *testing.T) {
	cfg := &model.Config{
		Printers: []model.Printer{{SN: "SN001", Name: "TestPrinter", Model: "M5"}},
		Camera:   model.DefaultCameraConfig(),
	}
	got := resolveCameraSettings(cfg, 0)
	if got.EffectiveSource != model.CameraSourcePrinter {
		t.Errorf("effective_source = %q, want %q", got.EffectiveSource, model.CameraSourcePrinter)
	}
	if !got.PrinterSupported {
		t.Error("printer_supported should be true for M5")
	}
	if !got.FeatureAvailable {
		t.Error("feature_available should be true when printer_supported")
	}
}

func TestResolveCameraSettings_NoCameraModel(t *testing.T) {
	cfg := &model.Config{
		Printers: []model.Printer{{SN: "SN002", Model: "V8110"}},
		Camera:   model.DefaultCameraConfig(),
	}
	got := resolveCameraSettings(cfg, 0)
	if got.PrinterSupported {
		t.Error("V8110 should not support printer camera")
	}
	if got.FeatureAvailable {
		t.Error("feature_available should be false: no camera, no external configured")
	}
	if got.EffectiveSource != "" {
		t.Errorf("effective_source = %q, want empty", got.EffectiveSource)
	}
}

func TestResolveCameraSettings_ExternalConfigured(t *testing.T) {
	cfg := &model.Config{
		Printers: []model.Printer{{SN: "SN003", Model: "V8110"}},
		Camera: model.CameraConfig{
			PerPrinter: map[string]model.PrinterCameraEntry{
				"SN003": {
					Source: model.CameraSourceExternal,
					External: model.ExternalCameraSettings{
						StreamURL:  "rtsp://192.168.1.100/live",
						RefreshSec: 3,
					},
				},
			},
		},
	}
	got := resolveCameraSettings(cfg, 0)
	if got.EffectiveSource != model.CameraSourceExternal {
		t.Errorf("effective_source = %q, want %q", got.EffectiveSource, model.CameraSourceExternal)
	}
	if !got.External.Configured {
		t.Error("external.configured should be true")
	}
	if !got.FeatureAvailable {
		t.Error("feature_available should be true with external configured")
	}
}

func TestResolveCameraSettings_PrinterSourceFallbackToExternal(t *testing.T) {
	// V8110 has no camera; source=printer should fall back to external when configured.
	cfg := &model.Config{
		Printers: []model.Printer{{SN: "SN004", Model: "V8110"}},
		Camera: model.CameraConfig{
			PerPrinter: map[string]model.PrinterCameraEntry{
				"SN004": {
					Source: model.CameraSourcePrinter,
					External: model.ExternalCameraSettings{
						SnapshotURL: "http://cam.local/snap.jpg",
						RefreshSec:  5,
					},
				},
			},
		},
	}
	got := resolveCameraSettings(cfg, 0)
	if got.EffectiveSource != model.CameraSourceExternal {
		t.Errorf("effective_source = %q, want %q (fallback)", got.EffectiveSource, model.CameraSourceExternal)
	}
}

func TestNormalizeCameraSource(t *testing.T) {
	cases := []struct{ in, fallback, want string }{
		{"printer", "", "printer"},
		{"external", "", "external"},
		{"EXTERNAL", "", "external"},
		{"unknown", "printer", "printer"},
		{"", "external", "external"},
		{"garbage", "garbage", "printer"},
	}
	for _, tc := range cases {
		got := normalizeCameraSource(tc.in, tc.fallback)
		if got != tc.want {
			t.Errorf("normalizeCameraSource(%q, %q) = %q, want %q", tc.in, tc.fallback, got, tc.want)
		}
	}
}

func TestSettingsCameraGet_NoPrinters(t *testing.T) {
	h := newTestHandler(t)
	r := httptest.NewRequest(http.MethodGet, "/api/settings/camera", nil)
	w := httptest.NewRecorder()
	h.SettingsCameraGet(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestSettingsCameraUpdate_InvalidURL(t *testing.T) {
	h := newTestHandlerWithConfig(t, &model.Config{
		Printers: []model.Printer{{SN: "SN001", Model: "M5", Name: "Test"}},
		Camera:   model.DefaultCameraConfig(),
	})

	body := `{"external":{"stream_url":"ftp://bad-scheme.com/stream"}}`
	r := httptest.NewRequest(http.MethodPost, "/api/settings/camera", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.SettingsCameraUpdate(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for disallowed URL scheme", w.Code)
	}
}

func TestSettingsCameraUpdate_ValidExternal(t *testing.T) {
	h := newTestHandlerWithConfig(t, &model.Config{
		Printers: []model.Printer{{SN: "SN001", Model: "M5", Name: "Test"}},
		Camera:   model.DefaultCameraConfig(),
	})

	body := `{"source":"external","external":{"stream_url":"rtsp://192.168.1.10/live","refresh_sec":5}}`
	r := httptest.NewRequest(http.MethodPost, "/api/settings/camera", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.SettingsCameraUpdate(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

	var resp map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	var cam model.ResolvedCameraSettings
	if err := json.Unmarshal(resp["camera"], &cam); err != nil {
		t.Fatal(err)
	}
	if cam.Source != model.CameraSourceExternal {
		t.Errorf("source = %q, want %q", cam.Source, model.CameraSourceExternal)
	}
	if cam.External.StreamURL != "rtsp://192.168.1.10/live" {
		t.Errorf("stream_url = %q", cam.External.StreamURL)
	}
	if cam.External.RefreshSec != 5 {
		t.Errorf("refresh_sec = %d, want 5", cam.External.RefreshSec)
	}
}
