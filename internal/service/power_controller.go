package service

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"time"

	"github.com/django1982/ankerctl/internal/config"
)

// PrinterPowerController can power-cycle the printer's smart socket to
// recover from a stuck PPPP state (printer accepts handshake but
// immediately drops the session).
type PrinterPowerController interface {
	PowerCycle(ctx context.Context) error
}

// smartSocketPowerController toggles the printer's smart socket via
// Home Assistant to power-cycle the printer.
type smartSocketPowerController struct {
	cfgMgr *config.Manager
	log    *slog.Logger
}

// NewSmartSocketPowerController creates a PrinterPowerController backed
// by the configured Home Assistant smart socket.
func NewSmartSocketPowerController(cfgMgr *config.Manager) PrinterPowerController {
	return &smartSocketPowerController{
		cfgMgr: cfgMgr,
		log:    slog.With("component", "power-controller"),
	}
}

// PowerCycle turns the smart socket off, waits, then turns it back on.
// The caller is responsible for waiting for the printer to finish booting
// before attempting to reconnect.
func (c *smartSocketPowerController) PowerCycle(ctx context.Context) error {
	cfg, err := c.cfgMgr.Load()
	if err != nil {
		return fmt.Errorf("power-controller: load config: %w", err)
	}
	if cfg == nil || !cfg.SmartSocket.Enabled {
		return fmt.Errorf("power-controller: smart socket not enabled")
	}
	ss := cfg.SmartSocket
	if ss.BaseURL == "" || ss.Token == "" || ss.SwitchEntity == "" {
		return fmt.Errorf("power-controller: smart socket not fully configured")
	}

	client := NewHomeAssistantClient(ss.BaseURL, ss.Token)

	c.log.Warn("power-controller: turning printer socket OFF")
	if err := client.CallService(ctx, "switch", "turn_off", ss.SwitchEntity); err != nil {
		return fmt.Errorf("power-controller: turn off: %w", err)
	}

	select {
	case <-time.After(10 * time.Second):
	case <-ctx.Done():
		return ctx.Err()
	}

	c.log.Warn("power-controller: turning printer socket ON")
	if err := client.CallService(ctx, "switch", "turn_on", ss.SwitchEntity); err != nil {
		return fmt.Errorf("power-controller: turn on: %w", err)
	}

	return nil
}

// waitForPrinterRecovery polls the printer IP until it responds to TCP
// dial on the PPPP discovery port or the deadline expires. The printer
// takes ~30-60 seconds to boot and start its PPPP daemon.
func waitForPrinterRecovery(ctx context.Context, printerIP string, deadline time.Time) error {
	if printerIP == "" {
		return fmt.Errorf("no printer IP to check")
	}
	addr := net.JoinHostPort(printerIP, "32108")
	interval := 3 * time.Second

	for {
		if time.Now().After(deadline) {
			return fmt.Errorf("printer did not become reachable before deadline")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		conn, err := net.DialTimeout("udp4", addr, 2*time.Second)
		if err == nil {
			_ = conn.Close()
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}

// ResolvePrinterIP fetches the current printer IP from config or DB cache.
func ResolvePrinterIP(cfgMgr *config.Manager, printerIndex int) string {
	if cfgMgr == nil {
		return ""
	}
	cfg, err := cfgMgr.Load()
	if err != nil || cfg == nil {
		return ""
	}
	if printerIndex < 0 || printerIndex >= len(cfg.Printers) {
		return ""
	}
	return cfg.Printers[printerIndex].IPAddr
}

// noopPowerController is a no-op implementation used when no smart socket
// is configured. PowerCycle returns an error so callers know recovery
// is unavailable.
type noopPowerController struct{}

func (noopPowerController) PowerCycle(_ context.Context) error {
	return fmt.Errorf("no power controller configured")
}

var _ PrinterPowerController = (*smartSocketPowerController)(nil)
var _ PrinterPowerController = noopPowerController{}

// PrinterPowerControllerFromConfig returns a smart-socket-backed power
// controller when enabled, or a no-op controller otherwise.
func PrinterPowerControllerFromConfig(cfgMgr *config.Manager) PrinterPowerController {
	if cfgMgr != nil {
		if cfg, err := cfgMgr.Load(); err == nil && cfg != nil {
			if cfg.SmartSocket.Enabled && smartSocketReady(cfg.SmartSocket) {
				return NewSmartSocketPowerController(cfgMgr)
			}
		}
	}
	return noopPowerController{}
}
