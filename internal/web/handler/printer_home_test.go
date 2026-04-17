package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/django1982/ankerctl/internal/service"
)

func TestPrinterHome_ServiceUnavailable(t *testing.T) {
	// No mqttqueue registered → handler must return 503.
	h := &Handler{svc: service.NewServiceManager()}
	req := httptest.NewRequest(http.MethodPost, "/api/printer/home", strings.NewReader(`{"axis":"all"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	h.PrinterHome(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusServiceUnavailable)
	}
}

func TestPrinterHome_InvalidAxis(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"unknown axis x", `{"axis":"x"}`},
		{"unknown axis xyz", `{"axis":"xyz"}`},
		{"unknown axis y", `{"axis":"y"}`},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			h := &Handler{svc: service.NewServiceManager()}
			req := httptest.NewRequest(http.MethodPost, "/api/printer/home", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			h.PrinterHome(rr, req)
			if rr.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", rr.Code)
			}
			if !strings.Contains(rr.Body.String(), "Invalid home axis") {
				t.Errorf("body = %q, want 'Invalid home axis'", rr.Body.String())
			}
		})
	}
}

func TestPrinterHome_DefaultAxisPassesValidation(t *testing.T) {
	// Empty body and missing axis field both default to "all" which is valid.
	// The service manager has no mqttqueue → we get 503 (not 400), proving
	// that axis validation succeeded before the service lookup.
	cases := []struct {
		name string
		body string
	}{
		{"empty object", `{}`},
		{"empty string body", ``},
		{"explicit all", `{"axis":"all"}`},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			h := &Handler{svc: service.NewServiceManager()}
			req := httptest.NewRequest(http.MethodPost, "/api/printer/home", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			h.PrinterHome(rr, req)
			if rr.Code == http.StatusBadRequest {
				t.Errorf("unexpected 400 for valid/default axis body=%q: %s", tc.body, rr.Body.String())
			}
			// Expect 503 (service unavailable) because no mqttqueue is wired.
			if rr.Code != http.StatusServiceUnavailable {
				t.Errorf("status = %d, want 503 for no-service path; body=%q", rr.Code, rr.Body.String())
			}
		})
	}
}

func TestPrinterHome_ValidAxisPassesValidation(t *testing.T) {
	// "xy" and "z" are the two valid non-"all" axes.
	cases := []struct {
		name string
		body string
	}{
		{"xy axis", `{"axis":"xy"}`},
		{"z axis", `{"axis":"z"}`},
		{"uppercase XY", `{"axis":"XY"}`},
		{"uppercase Z", `{"axis":"Z"}`},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			h := &Handler{svc: service.NewServiceManager()}
			req := httptest.NewRequest(http.MethodPost, "/api/printer/home", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			h.PrinterHome(rr, req)
			if rr.Code == http.StatusBadRequest {
				t.Errorf("unexpected 400 for valid axis body=%q: %s", tc.body, rr.Body.String())
			}
		})
	}
}

func TestPrinterHome_ErrorResponseIsJSON(t *testing.T) {
	// Error paths must produce valid JSON with an "error" key.
	h := &Handler{svc: service.NewServiceManager()}
	req := httptest.NewRequest(http.MethodPost, "/api/printer/home", strings.NewReader(`{"axis":"z"}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.PrinterHome(rr, req)

	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response is not valid JSON: %v — body: %s", err, rr.Body.String())
	}
	if _, ok := resp["error"]; !ok {
		t.Errorf("error response missing 'error' key: %v", resp)
	}
}
