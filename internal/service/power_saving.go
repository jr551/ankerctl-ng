package service

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/django1982/ankerctl/internal/config"
	"github.com/django1982/ankerctl/internal/model"
)

type powerSavingDashboardCmd struct{}

type PowerSavingStatus struct {
	Configured  bool       `json:"configured"`
	Enabled     bool       `json:"enabled"`
	PrintActive bool       `json:"print_active"`
	AwakeUntil  *time.Time `json:"awake_until,omitempty"`
	LastAction  string     `json:"last_action,omitempty"`
	LastError   string     `json:"last_error,omitempty"`
}

type PowerSavingService struct {
	BaseWorker

	mu          sync.Mutex
	log         *slog.Logger
	cfgMgr      *config.Manager
	printActive bool
	awakeUntil  *time.Time
	lastAction  string
	lastError   string

	cmdCh chan any
}

func NewPowerSavingService(cfgMgr *config.Manager) *PowerSavingService {
	s := &PowerSavingService{
		BaseWorker: NewBaseWorker("powersaving"),
		log:        slog.With("service", "powersaving"),
		cfgMgr:     cfgMgr,
		cmdCh:      make(chan any, 16),
	}
	s.BindHooks(s)
	return s
}

func (s *PowerSavingService) TouchDashboard() {
	select {
	case s.cmdCh <- powerSavingDashboardCmd{}:
	default:
	}
}

func (s *PowerSavingService) Status() PowerSavingStatus {
	cfg := s.loadSmartSocketConfig()
	s.mu.Lock()
	defer s.mu.Unlock()
	return PowerSavingStatus{
		Configured:  smartSocketReady(cfg),
		Enabled:     cfg.Enabled && cfg.PowerSavingEnabled,
		PrintActive: s.printActive,
		AwakeUntil:  cloneTimePtr(s.awakeUntil),
		LastAction:  s.lastAction,
		LastError:   s.lastError,
	}
}

func (s *PowerSavingService) Notify(data any) {
	s.BaseWorker.Notify(data)
	payload, ok := data.(map[string]any)
	if !ok || payload["event"] != "print_state" {
		return
	}
	state, ok := asIntIface(payload["state"])
	if !ok {
		return
	}
	select {
	case s.cmdCh <- map[string]int{"state": state}:
	default:
	}
}

func (s *PowerSavingService) WorkerStart() error { return nil }

func (s *PowerSavingService) WorkerRun(ctx context.Context) error {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case raw := <-s.cmdCh:
			s.handleCommand(ctx, raw)
		case <-ticker.C:
			s.evaluate(ctx)
		}
	}
}

func (s *PowerSavingService) WorkerStop() {}

func (s *PowerSavingService) handleCommand(ctx context.Context, raw any) {
	switch cmd := raw.(type) {
	case powerSavingDashboardCmd:
		s.wakeForDashboard(ctx)
	case map[string]int:
		s.handlePrintState(ctx, cmd["state"])
	}
}

func (s *PowerSavingService) wakeForDashboard(ctx context.Context) {
	cfg := s.loadSmartSocketConfig()
	if !cfg.Enabled || !cfg.PowerSavingEnabled || !smartSocketReady(cfg) {
		return
	}
	wakeSec := cfg.PowerSavingDashboardWakeSec
	if wakeSec <= 0 {
		wakeSec = model.DefaultSmartSocketConfig().PowerSavingDashboardWakeSec
	}
	if wakeSec < 60 {
		wakeSec = 60
	}
	until := time.Now().Add(time.Duration(wakeSec) * time.Second)
	s.mu.Lock()
	s.awakeUntil = &until
	s.mu.Unlock()
	s.setSocket(ctx, cfg, true, "dashboard wake")
}

func (s *PowerSavingService) handlePrintState(ctx context.Context, state int) {
	cfg := s.loadSmartSocketConfig()
	if !cfg.Enabled || !cfg.PowerSavingEnabled || !smartSocketReady(cfg) {
		return
	}
	switch state {
	case mqttStatePrinting:
		s.mu.Lock()
		s.printActive = true
		s.mu.Unlock()
		s.setSocket(ctx, cfg, true, "print started")
	case mqttStateIdle, mqttStateAborted:
		s.mu.Lock()
		s.printActive = false
		s.mu.Unlock()
		s.evaluate(ctx)
	case mqttStatePaused:
		s.mu.Lock()
		s.printActive = true
		s.mu.Unlock()
	}
}

func (s *PowerSavingService) evaluate(ctx context.Context) {
	cfg := s.loadSmartSocketConfig()
	if !cfg.Enabled || !cfg.PowerSavingEnabled || !smartSocketReady(cfg) {
		return
	}
	s.mu.Lock()
	printActive := s.printActive
	awakeUntil := cloneTimePtr(s.awakeUntil)
	if awakeUntil != nil && time.Now().After(*awakeUntil) {
		s.awakeUntil = nil
		awakeUntil = nil
	}
	s.mu.Unlock()
	if printActive || awakeUntil != nil {
		return
	}
	s.setSocket(ctx, cfg, false, "idle cooldown expired")
}

func (s *PowerSavingService) setSocket(ctx context.Context, cfg model.SmartSocketConfig, on bool, action string) {
	client := NewHomeAssistantClient(cfg.BaseURL, cfg.Token)
	serviceName := "turn_off"
	if on {
		serviceName = "turn_on"
	}
	err := client.CallService(ctx, "switch", serviceName, cfg.SwitchEntity)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastAction = action
	if err != nil {
		s.lastError = err.Error()
		if s.log != nil {
			s.log.Warn("power saving socket action failed", "action", action, "err", err)
		}
		return
	}
	s.lastError = ""
}

func (s *PowerSavingService) loadSmartSocketConfig() model.SmartSocketConfig {
	def := model.DefaultSmartSocketConfig()
	if s == nil || s.cfgMgr == nil {
		return def
	}
	cfg, err := s.cfgMgr.Load()
	if err != nil || cfg == nil {
		return def
	}
	ss := cfg.SmartSocket
	if ss.PowerSavingDashboardWakeSec <= 0 {
		ss.PowerSavingDashboardWakeSec = def.PowerSavingDashboardWakeSec
	}
	if ss.PowerUnit == "" {
		ss.PowerUnit = def.PowerUnit
	}
	return ss
}

func smartSocketReady(cfg model.SmartSocketConfig) bool {
	return strings.TrimSpace(cfg.BaseURL) != "" &&
		strings.TrimSpace(cfg.Token) != "" &&
		strings.TrimSpace(cfg.SwitchEntity) != ""
}
