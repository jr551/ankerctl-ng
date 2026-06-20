package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/django1982/ankerctl/internal/model"
)

func TestSettingsTemperatureOverridesUpdateAndGet(t *testing.T) {
	h := newTestHandlerWithConfig(t, &model.Config{
		Printers: []model.Printer{{SN: "SN001", Model: "M5C", Name: "M5C"}},
	})

	body := `{"temperature_overrides":{"enabled":true,"nozzle_min_temp_c":210,"bed_min_temp_c":60}}`
	req := httptest.NewRequest(http.MethodPost, "/api/settings/temperature-overrides", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.SettingsTemperatureOverridesUpdate(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("update status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/settings/temperature-overrides", nil)
	w = httptest.NewRecorder()
	h.SettingsTemperatureOverridesGet(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("get status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

	var resp struct {
		TemperatureOverrides model.TemperatureOverrideEntry `json:"temperature_overrides"`
		PrinterSN            string                         `json:"printer_sn"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.PrinterSN != "SN001" {
		t.Fatalf("printer_sn = %q, want SN001", resp.PrinterSN)
	}
	got := resp.TemperatureOverrides
	if !got.Enabled || got.NozzleMinTempC != 210 || got.BedMinTempC != 60 {
		t.Fatalf("temperature_overrides = %+v, want enabled nozzle=210 bed=60", got)
	}
}

func TestSettingsTemperatureOverridesClampsValues(t *testing.T) {
	h := newTestHandlerWithConfig(t, &model.Config{
		Printers: []model.Printer{{SN: "SN001", Model: "M5C", Name: "M5C"}},
	})

	body := `{"enabled":true,"nozzle_min_temp_c":999,"bed_min_temp_c":-5}`
	req := httptest.NewRequest(http.MethodPost, "/api/settings/temperature-overrides", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.SettingsTemperatureOverridesUpdate(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

	var resp struct {
		TemperatureOverrides model.TemperatureOverrideEntry `json:"temperature_overrides"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	got := resp.TemperatureOverrides
	if !got.Enabled || got.NozzleMinTempC != 320 || got.BedMinTempC != 0 {
		t.Fatalf("temperature_overrides = %+v, want enabled nozzle=320 bed=0", got)
	}
}
