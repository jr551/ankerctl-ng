package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/django1982/ankerctl/internal/config"
	"github.com/django1982/ankerctl/internal/model"
)

func TestPowerSavingWakeForDashboardSkipsDuplicateTurnOn(t *testing.T) {
	cfgMgr, calls := newPowerSavingTestConfig(t)
	svc := NewPowerSavingService(cfgMgr)

	svc.wakeForDashboard(context.Background())
	svc.wakeForDashboard(context.Background())

	if got := calls.count("/api/services/switch/turn_on"); got != 1 {
		t.Fatalf("turn_on calls = %d, want 1", got)
	}
}

func TestPowerSavingPrintingStateSkipsDuplicateTurnOn(t *testing.T) {
	cfgMgr, calls := newPowerSavingTestConfig(t)
	svc := NewPowerSavingService(cfgMgr)

	svc.handlePrintState(context.Background(), mqttStatePrinting)
	svc.handlePrintState(context.Background(), mqttStatePrinting)

	if got := calls.count("/api/services/switch/turn_on"); got != 1 {
		t.Fatalf("turn_on calls = %d, want 1", got)
	}
}

func newPowerSavingTestConfig(t *testing.T) (*config.Manager, *powerSavingCallLog) {
	t.Helper()
	calls := &powerSavingCallLog{byPath: map[string]int{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.add(r.URL.Path)
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
