package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/django1982/ankerctl/internal/config"
	"github.com/django1982/ankerctl/internal/model"
)

type SnapshotOnly interface {
	CaptureSnapshot(ctx context.Context, outputPath string) error
}

type ConfigCameraSnapshotter struct {
	cfg          *config.Manager
	printerIndex int
	printer      SnapshotOnly
}

func NewConfigCameraSnapshotter(cfg *config.Manager, printerIndex int, printer SnapshotOnly) *ConfigCameraSnapshotter {
	return &ConfigCameraSnapshotter{cfg: cfg, printerIndex: printerIndex, printer: printer}
}

func (s *ConfigCameraSnapshotter) CaptureSnapshot(ctx context.Context, outputPath string) error {
	if s == nil || s.cfg == nil {
		return fmt.Errorf("camera snapshotter is not configured")
	}
	cfg, err := s.cfg.Load()
	if err != nil {
		return fmt.Errorf("load camera config: %w", err)
	}
	if cfg == nil || len(cfg.Printers) == 0 {
		return fmt.Errorf("no printers configured")
	}
	idx := s.printerIndex
	if idx < 0 || idx >= len(cfg.Printers) {
		idx = cfg.ActivePrinterIndex
	}
	if idx < 0 || idx >= len(cfg.Printers) {
		idx = 0
	}
	printer := cfg.Printers[idx]
	entry := configCameraEntryForPrinter(cfg, printer.SN)
	source := normalizeConfigCameraSource(entry.Source, model.CameraSourcePrinter)
	external := normalizeConfigExternalCamera(entry.External)
	externalConfigured := external.StreamURL != "" || external.SnapshotURL != "" || HomeAssistantCameraConfigured(external.HomeAssistant)
	printerSupported := model.PrinterSupportsCamera(printer.Model)

	switch {
	case source == model.CameraSourcePrinter && printerSupported:
		if s.printer == nil {
			return fmt.Errorf("printer camera service is unavailable")
		}
		return s.printer.CaptureSnapshot(ctx, outputPath)
	case source == model.CameraSourceExternal && externalConfigured:
		return captureExternalCamera(ctx, external, outputPath)
	case source == model.CameraSourcePrinter && !printerSupported && externalConfigured:
		return captureExternalCamera(ctx, external, outputPath)
	default:
		return fmt.Errorf("no camera source is available")
	}
}

func captureExternalCamera(ctx context.Context, external model.ExternalCameraSettings, outputPath string) error {
	if HomeAssistantCameraConfigured(external.HomeAssistant) {
		return HomeAssistantCameraSnapshot(ctx, external.HomeAssistant, outputPath)
	}
	input := strings.TrimSpace(external.SnapshotURL)
	if input == "" {
		input = strings.TrimSpace(external.StreamURL)
	}
	if input == "" {
		return fmt.Errorf("external camera URL is not configured")
	}
	return SnapshotExternal(ctx, input, outputPath)
}

func configCameraEntryForPrinter(cfg *model.Config, sn string) model.PrinterCameraEntry {
	if cfg != nil && sn != "" && cfg.Camera.PerPrinter != nil {
		if entry, ok := cfg.Camera.PerPrinter[sn]; ok {
			return entry
		}
	}
	return model.PrinterCameraEntry{
		Source:   model.CameraSourcePrinter,
		External: model.DefaultExternalCameraSettings(),
	}
}

func normalizeConfigCameraSource(val, fallback string) string {
	v := strings.ToLower(strings.TrimSpace(val))
	switch v {
	case model.CameraSourcePrinter, model.CameraSourceExternal:
		return v
	}
	if fallback == model.CameraSourcePrinter || fallback == model.CameraSourceExternal {
		return fallback
	}
	return model.CameraSourcePrinter
}

func normalizeConfigExternalCamera(e model.ExternalCameraSettings) model.ExternalCameraSettings {
	if e.RefreshSec < 1 {
		e.RefreshSec = 3
	}
	if e.RefreshSec > 30 {
		e.RefreshSec = 30
	}
	e.StreamURL = strings.TrimSpace(e.StreamURL)
	e.SnapshotURL = strings.TrimSpace(e.SnapshotURL)
	e.HomeAssistant.BaseURL = strings.TrimRight(strings.TrimSpace(e.HomeAssistant.BaseURL), "/")
	e.HomeAssistant.Token = strings.TrimSpace(e.HomeAssistant.Token)
	e.HomeAssistant.CameraEntityID = strings.TrimSpace(e.HomeAssistant.CameraEntityID)
	return e
}
