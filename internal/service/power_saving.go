package service

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/django1982/ankerctl/internal/config"
	"github.com/django1982/ankerctl/internal/model"
)

type powerSavingDashboardCmd struct{}

type PowerSavingStatus struct {
	Configured       bool       `json:"configured"`
	Enabled          bool       `json:"enabled"`
	PrintActive      bool       `json:"print_active"`
	IdleSince        *time.Time `json:"idle_since,omitempty"`
	IdleOffAt        *time.Time `json:"idle_off_at,omitempty"`
	IdleOffSec       int        `json:"idle_off_sec,omitempty"`
	DashboardWakeSec int        `json:"dashboard_wake_sec,omitempty"`
	AwakeUntil       *time.Time `json:"awake_until,omitempty"`
	LastAction       string     `json:"last_action,omitempty"`
	LastError        string     `json:"last_error,omitempty"`
}

type PowerSavingService struct {
	BaseWorker

	mu          sync.Mutex
	log         *slog.Logger
	cfgMgr      *config.Manager
	printActive bool
	idleSince   *time.Time
	awakeUntil  *time.Time
	lastAction  string
	lastError   string

	cmdCh chan any

	// Idle turn_off gating: consecutive probe/call failures and the earliest
	// time the next idle turn_off attempt may run (zero = no backoff pending).
	failStreak  int
	nextAttempt time.Time
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
	printActive := s.printActive
	idleSince := cloneTimePtr(s.idleSince)
	awakeUntil := cloneTimePtr(s.awakeUntil)
	lastAction := s.lastAction
	lastError := s.lastError
	s.mu.Unlock()

	idleOffSec := cfg.PowerSavingIdleOffSec
	if idleOffSec <= 0 {
		idleOffSec = model.DefaultSmartSocketConfig().PowerSavingIdleOffSec
	}
	wakeSec := cfg.PowerSavingDashboardWakeSec
	if wakeSec <= 0 {
		wakeSec = model.DefaultSmartSocketConfig().PowerSavingDashboardWakeSec
	}
	var idleOffAt *time.Time
	if cfg.Enabled && cfg.PowerSavingEnabled && !printActive && idleSince != nil {
		t := idleSince.Add(time.Duration(idleOffSec) * time.Second)
		idleOffAt = &t
	}
	return PowerSavingStatus{
		Configured:       smartSocketReady(cfg),
		Enabled:          cfg.Enabled && cfg.PowerSavingEnabled,
		PrintActive:      printActive,
		IdleSince:        idleSince,
		IdleOffAt:        idleOffAt,
		IdleOffSec:       idleOffSec,
		DashboardWakeSec: wakeSec,
		AwakeUntil:       awakeUntil,
		LastAction:       lastAction,
		LastError:        lastError,
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
	alreadyAwake := s.awakeUntil != nil && time.Now().Before(*s.awakeUntil)
	s.awakeUntil = &until
	if s.idleSince == nil {
		now := time.Now()
		s.idleSince = &now
	}
	s.mu.Unlock()
	if !alreadyAwake {
		s.setSocket(ctx, cfg, true, "dashboard wake")
	}
}

func (s *PowerSavingService) handlePrintState(ctx context.Context, state int) {
	cfg := s.loadSmartSocketConfig()
	if !cfg.Enabled || !cfg.PowerSavingEnabled || !smartSocketReady(cfg) {
		return
	}
	switch state {
	case mqttStatePrinting:
		s.mu.Lock()
		wasActive := s.printActive
		s.printActive = true
		s.idleSince = nil
		s.mu.Unlock()
		if !wasActive {
			s.setSocket(ctx, cfg, true, "print started")
		}
	case mqttStateIdle, mqttStateAborted:
		now := time.Now()
		s.mu.Lock()
		s.printActive = false
		s.idleSince = &now
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
	idleSince := cloneTimePtr(s.idleSince)
	awakeUntil := cloneTimePtr(s.awakeUntil)
	if awakeUntil != nil && time.Now().After(*awakeUntil) {
		s.awakeUntil = nil
		awakeUntil = nil
	}
	s.mu.Unlock()
	if printActive || awakeUntil != nil {
		return
	}
	idleOffSec := cfg.PowerSavingIdleOffSec
	if idleOffSec <= 0 {
		idleOffSec = model.DefaultSmartSocketConfig().PowerSavingIdleOffSec
	}
	if idleSince == nil {
		return
	}
	if time.Since(*idleSince) < time.Duration(idleOffSec)*time.Second {
		return
	}
	// Backoff gate: after repeated probe/call failures the ticker keeps
	// running but the next attempt is deferred (1m, 2m, 4m … capped 15m) so a
	// dead plug or an unreachable HA cannot turn this 15s tick into a retry
	// storm.
	s.mu.Lock()
	if !s.nextAttempt.IsZero() && time.Now().Before(s.nextAttempt) {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	// State gate: only issue turn_off when the entity is actually "on". An
	// already-off socket needs nothing; "unavailable"/"unknown" or a failed
	// probe must not call the service at all (HA answers 200 for those, which
	// previously caused an endless re-issue loop).
	client := NewHomeAssistantClient(cfg.BaseURL, cfg.Token)
	st, err := client.State(ctx, cfg.SwitchEntity)
	if err != nil {
		s.noteFailure(err, "state probe failed")
		return
	}
	switch strings.ToLower(strings.TrimSpace(st.State)) {
	case "on":
		s.noteSuccess()
		if err := s.setSocket(ctx, cfg, false, "idle cooldown expired"); err != nil {
			s.noteFailure(err, "turn_off failed")
		}
	case "off":
		// Latched: the socket is already off, nothing to do.
		s.noteSuccess()
	default: // unavailable, unknown, …
		s.noteFailure(fmt.Errorf("entity %s is %s", cfg.SwitchEntity, st.State), "entity not controllable")
	}
}

// noteFailure records a failed state probe or socket call and defers the next
// idle turn_off attempt by an exponentially growing delay. It logs at DEBUG:
// no service call was issued, so a WARN would only re-emit the same non-action.
func (s *PowerSavingService) noteFailure(err error, reason string) {
	s.mu.Lock()
	s.failStreak++
	delay := powerSavingBackoff(s.failStreak)
	s.nextAttempt = time.Now().Add(delay)
	s.lastError = err.Error()
	s.mu.Unlock()
	if s.log != nil {
		s.log.Debug("power saving turn_off deferred", "reason", reason, "err", err, "retry_in", delay)
	}
}

// noteSuccess clears the failure backoff after HA answered a state probe (or,
// via setSocket, after a successful service call).
func (s *PowerSavingService) noteSuccess() {
	s.mu.Lock()
	s.failStreak = 0
	s.nextAttempt = time.Time{}
	s.lastError = ""
	s.mu.Unlock()
}

// powerSavingBackoff maps a consecutive-failure count to the delay before the
// next attempt: 1m, 2m, 4m, 8m … capped at 15m.
func powerSavingBackoff(failures int) time.Duration {
	const (
		base = time.Minute
		max  = 15 * time.Minute
	)
	if failures <= 0 {
		return 0
	}
	if failures > 16 {
		failures = 16 // 2^15 min already exceeds the cap; avoid overflow
	}
	d := base << (failures - 1)
	if d > max {
		return max
	}
	return d
}

func (s *PowerSavingService) setSocket(ctx context.Context, cfg model.SmartSocketConfig, on bool, action string) error {
	client := NewHomeAssistantClient(cfg.BaseURL, cfg.Token)
	serviceName := "turn_off"
	if on {
		serviceName = "turn_on"
	}
	// Use the domain-agnostic homeassistant.turn_on/turn_off service: it works
	// for switch.*, light.*, input_boolean.*, etc. Calling switch.turn_off on a
	// non-switch entity makes HA return HTTP 400.
	err := client.CallService(ctx, "homeassistant", serviceName, cfg.SwitchEntity)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastAction = action
	if err != nil {
		s.lastError = err.Error()
		if s.log != nil {
			s.log.Warn("power saving socket action failed", "action", action, "err", err)
		}
		return err
	}
	s.lastError = ""
	s.failStreak = 0
	s.nextAttempt = time.Time{}
	return nil
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
	if ss.PowerSavingIdleOffSec <= 0 {
		ss.PowerSavingIdleOffSec = def.PowerSavingIdleOffSec
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
