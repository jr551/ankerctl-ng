package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/django1982/ankerctl/internal/config"
	"github.com/django1982/ankerctl/internal/model"
)

func TestPowerSavingWakeForDashboardSkipsDuplicateTurnOn(t *testing.T) {
	cfgMgr, calls := newPowerSavingTestConfig(t)
	svc := NewPowerSavingService(cfgMgr)

	svc.wakeForDashboard(context.Background())
	svc.wakeForDashboard(context.Background())

	if got := calls.count("/api/services/homeassistant/turn_on"); got != 1 {
		t.Fatalf("turn_on calls = %d, want 1", got)
	}
}

func TestPowerSavingPrintingStateSkipsDuplicateTurnOn(t *testing.T) {
	cfgMgr, calls := newPowerSavingTestConfig(t)
	svc := NewPowerSavingService(cfgMgr)

	svc.handlePrintState(context.Background(), mqttStatePrinting)
	svc.handlePrintState(context.Background(), mqttStatePrinting)

	if got := calls.count("/api/services/homeassistant/turn_on"); got != 1 {
		t.Fatalf("turn_on calls = %d, want 1", got)
	}
}

// Regression for the 15s retry storm: when the entity is unavailable (HA
// answers HTTP 200 for the service call, so success alone never stopped the
// loop), evaluate() must skip turn_off entirely and back off re-probing.
func TestPowerSavingIdleTurnOffSkipsWhenEntityNotOn(t *testing.T) {
	cfgMgr, calls := newPowerSavingTestConfig(t)
	calls.mu.Lock()
	calls.state = "unavailable"
	calls.mu.Unlock()
	svc := NewPowerSavingService(cfgMgr)

	idle := time.Now().Add(-2 * time.Minute) // idle_off_sec is 60 in this config
	svc.mu.Lock()
	svc.idleSince = &idle
	svc.mu.Unlock()

	svc.evaluate(context.Background())
	svc.evaluate(context.Background()) // inside backoff window: must not re-probe

	if got := calls.count("/api/services/homeassistant/turn_off"); got != 0 {
		t.Fatalf("turn_off calls = %d, want 0 (state gate must not call the service for a non-on entity)", got)
	}
	if got := calls.count("/api/states/switch.printer"); got != 1 {
		t.Fatalf("state probes = %d, want 1 (second evaluate deferred by backoff)", got)
	}
}

func newPowerSavingTestConfig(t *testing.T) (*config.Manager, *powerSavingCallLog) {
	t.Helper()
	calls := &powerSavingCallLog{byPath: map[string]int{}, state: "on"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.add(r.URL.Path)
		if strings.HasPrefix(r.URL.Path, "/api/states/") {
			calls.mu.Lock()
			state := calls.state
			calls.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"entity_id":"switch.printer","state":%q,"last_changed":"2026-01-01T00:00:00.000000Z","last_updated":"2026-01-01T00:00:00.000000Z","attributes":{}}`, state)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	cfg := model.NewConfig(nil, nil)
	cfg.SmartSocket = model.SmartSocketConfig{
		Enabled:                     true,
		BaseURL:                     server.URL,
		Token:                       "test-token",
		SwitchEntity:                "switch.printer",
		PowerSavingEnabled:          true,
		PowerSavingIdleOffSec:       60,
		PowerSavingDashboardWakeSec: 60,
	}
	cfgMgr, err := config.NewManager(t.TempDir())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := cfgMgr.Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return cfgMgr, calls
}

type powerSavingCallLog struct {
	mu     sync.Mutex
	byPath map[string]int
	state  string // entity state returned by GET /api/states/*
}

func (l *powerSavingCallLog) add(path string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.byPath[path]++
}

func (l *powerSavingCallLog) count(path string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.byPath[path]
}
