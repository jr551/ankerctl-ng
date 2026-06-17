package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/django1982/ankerctl/internal/model"
)

func TestCameraFrame_HomeAssistantSnapshotTimesOutFast(t *testing.T) {
	oldTimeout := cameraSnapshotTimeout
	cameraSnapshotTimeout = 20 * time.Millisecond
	defer func() { cameraSnapshotTimeout = oldTimeout }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(200 * time.Millisecond):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("not-a-jpeg"))
		}
	}))
	defer srv.Close()

	h := newTestHandlerWithConfig(t, &model.Config{
		Printers: []model.Printer{{SN: "SN001", Model: "V8110", Name: "Test"}},
		Camera: model.CameraConfig{
			PerPrinter: map[string]model.PrinterCameraEntry{
				"SN001": {
					Source: model.CameraSourceExternal,
					External: model.ExternalCameraSettings{
						HomeAssistant: model.HomeAssistantCameraSettings{
							Enabled:        true,
							BaseURL:        srv.URL,
							Token:          "token",
							CameraEntityID: "camera.front_door_camera",
						},
						RefreshSec: 3,
					},
				},
			},
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/api/camera/frame", nil)
	rr := httptest.NewRecorder()
	start := time.Now()
	h.CameraFrame(rr, req)
	elapsed := time.Since(start)

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status=%d want %d body=%s", rr.Code, http.StatusBadGateway, rr.Body.String())
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("CameraFrame took %v, want fast timeout", elapsed)
	}
}
