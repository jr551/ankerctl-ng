package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/django1982/ankerctl/internal/model"
)

func TestSmartSocketStateRequiresBaseURLTokenAndSwitch(t *testing.T) {
	h := newTestHandlerWithConfig(t, smartSocketTestConfig(model.SmartSocketConfig{
		Enabled:      true,
		SwitchEntity: "switch.printer",
	}))

	w := httptest.NewRecorder()
	h.SmartSocketState(w, httptest.NewRequest(http.MethodGet, "/api/smart-socket/state", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if body["available"] != false {
		t.Fatalf("available = %v, want false", body["available"])
	}
	if body["error"] != "smart socket is not configured" {
		t.Fatalf("error = %v", body["error"])
	}
}

func TestSmartSocketControlRequiresBaseURLTokenAndSwitch(t *testing.T) {
	h := newTestHandlerWithConfig(t, smartSocketTestConfig(model.SmartSocketConfig{
		Enabled:      true,
		SwitchEntity: "switch.printer",
	}))

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/smart-socket/control", strings.NewReader(`{"action":"on"}`))
	h.SmartSocketControl(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", w.Code, w.Body.String())
	}
}

func smartSocketTestConfig(ss model.SmartSocketConfig) *model.Config {
	cfg := model.NewConfig(nil, nil)
	cfg.SmartSocket = ss
	return cfg
}
