package notifications

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/django1982/ankerctl/internal/db"
	"github.com/django1982/ankerctl/internal/model"
	"github.com/django1982/ankerctl/internal/service"
)

const (
	mqttStateIdle     = 0
	mqttStatePrinting = 1
	mqttStatePaused   = 2
	mqttStateAborted  = 8
)

var newClient = NewClient

// ConfigLoader loads runtime config.
type ConfigLoader interface {
	Load() (*model.Config, error)
}

// EventTapSource exposes Tap subscription behavior.
type EventTapSource interface {
	Tap(handler func(any)) func()
}

// SnapshotCapturer can capture a JPEG snapshot to disk.
type SnapshotCapturer interface {
	CaptureSnapshot(ctx context.Context, outputPath string) error
}

// NotificationService wires MQTT events to Apprise notifications.
type NotificationService struct {
	service.BaseWorker

	cfg      ConfigLoader
	history  *db.DB
	mqtt     EventTapSource
	snapshot SnapshotCapturer

	mu                   sync.Mutex
	lastState            int
	lastFilename         string
	lastProgress         int
	lastProgressNotified int // last progress % that triggered a notification
	progressSendCount    int // how many progress notifications sent this print
}

// NewNotificationService creates a notification bridge.
func NewNotificationService(cfg ConfigLoader, mqtt EventTapSource, snapshot SnapshotCapturer) *NotificationService {
	s := &NotificationService{
		BaseWorker:           service.NewBaseWorker("notifications"),
		cfg:                  cfg,
		mqtt:                 mqtt,
		snapshot:             snapshot,
		lastState:            -1,
		lastFilename:         "-",
		lastProgressNotified: -1,
	}
	s.BindHooks(s)
	return s
}

func (s *NotificationService) WithHistory(history *db.DB) *NotificationService {
	s.history = history
	return s
}

func (s *NotificationService) WorkerInit() {}

func (s *NotificationService) WorkerStart() error {
	if s.mqtt == nil {
		return fmt.Errorf("notifications: mqtt source is nil")
	}
	return nil
}

// WorkerRun subscribes to MQTT events and sends notifications until ctx is cancelled.
func (s *NotificationService) WorkerRun(ctx context.Context) error {
	events := make(chan any, 128)
	unsub := s.mqtt.Tap(func(v any) {
		select {
		case events <- v:
		default:
		}
	})
	defer unsub()

	for {
		select {
		case <-ctx.Done():
			return nil
		case evt := <-events:
			s.handleEvent(ctx, evt)
		}
	}
}

func (s *NotificationService) WorkerStop() {}

// SendTestNotification sends a plain test message using a config snapshot.
func SendTestNotification(ctx context.Context, apprise model.AppriseConfig, snapshot SnapshotCapturer) (bool, string) {
	resolved := ResolveAppriseEnv(apprise)
	client := newClient(resolved)
	if !client.IsConfigured() {
		return false, "Apprise server URL or key missing"
	}
	want := resolved.Progress.IncludeImage || envBool("APPRISE_ATTACH")
	attachments := maybeSnapshotAttachment(ctx, snapshot, want)
	return client.Post(ctx, "Ankerctl Test", "Test notification sent from ankerctl settings page.", "info", attachments)
}

func SendTestAnnouncement(ctx context.Context, cfg model.HomeAnnouncementConfig) (bool, string) {
	payload := map[string]any{
		"filename": "test.gcode",
		"body":     "Test announcement sent from ankerctl settings page.",
	}
	res := SendHomeAnnouncement(ctx, cfg, EventPrintFinished, payload)
	return res.OK, res.Message
}

func (s *NotificationService) handleEvent(ctx context.Context, evt any) {
	payload, ok := evt.(map[string]any)
	if !ok {
		return
	}

	if fn := extractFilename(payload); fn != "" {
		s.mu.Lock()
		s.lastFilename = fn
		s.mu.Unlock()
	}
	if p, ok := extractProgress(payload); ok {
		s.mu.Lock()
		s.lastProgress = p
		s.mu.Unlock()
		// Check if we should fire a progress notification.
		s.maybeProgressNotification(ctx, p)
	}

	eventName, _ := payload["event"].(string)
	if eventName != "print_state" {
		return
	}
	state, ok := asInt(payload["state"])
	if !ok {
		return
	}
	s.handleStateTransition(ctx, state)
}

func (s *NotificationService) maybeProgressNotification(ctx context.Context, progress int) {
	client := s.currentClient()
	if client == nil || !client.IsEventEnabled(EventPrintProgress) {
		return
	}

	interval := client.settings.Progress.IntervalPercent
	if interval < 1 {
		interval = 1
	}
	if interval > 100 {
		interval = 100
	}

	s.mu.Lock()
	lastNotified := s.lastProgressNotified
	state := s.lastState
	filename := s.lastFilename
	sendCount := s.progressSendCount
	maxValue := client.settings.Progress.MaxValue
	s.mu.Unlock()

	// Only send during an active print.
	if state != mqttStatePrinting {
		return
	}

	// Determine if we crossed an interval threshold.
	nextThreshold := lastNotified + interval
	if lastNotified < 0 {
		nextThreshold = interval
		// Ignore stale high-progress values that arrive right at print start
		// (printer re-sends last progress from previous print on ct=1001).
		if progress >= 100 {
			return
		}
	}
	if progress < nextThreshold {
		return
	}

	// Check max_value cap.
	if maxValue > 0 && sendCount >= maxValue {
		return
	}

	s.mu.Lock()
	s.lastProgressNotified = progress
	s.progressSendCount++
	s.mu.Unlock()

	payload := map[string]any{
		"filename": filename,
		"percent":  progress,
	}
	s.send(ctx, EventPrintProgress, payload)
}

func (s *NotificationService) handleStateTransition(ctx context.Context, state int) {
	s.mu.Lock()
	prev := s.lastState
	s.lastState = state
	filename := s.lastFilename
	progress := s.lastProgress
	s.mu.Unlock()

	payload := map[string]any{
		"filename": filename,
		"percent":  progress,
		"duration": "",
		"reason":   "",
	}

	switch state {
	case mqttStatePrinting:
		if prev == mqttStatePaused {
			s.send(ctx, EventPrintResumed, payload)
			return
		}
		if prev != mqttStatePrinting {
			// Reset progress tracking for new print.
			s.mu.Lock()
			s.lastProgressNotified = -1
			s.progressSendCount = 0
			s.mu.Unlock()
			s.send(ctx, EventPrintStarted, payload)
		}
	case mqttStatePaused:
		if prev == mqttStatePrinting {
			s.send(ctx, EventPrintPaused, payload)
		}
	case mqttStateIdle:
		if prev == mqttStatePrinting || prev == mqttStatePaused {
			// Reset progress tracking.
			s.mu.Lock()
			s.lastProgressNotified = -1
			s.progressSendCount = 0
			s.mu.Unlock()
			s.send(ctx, EventPrintFinished, payload)
		}
	case mqttStateAborted:
		if prev == mqttStatePrinting || prev == mqttStatePaused {
			payload["reason"] = "aborted"
			// Reset progress tracking.
			s.mu.Lock()
			s.lastProgressNotified = -1
			s.progressSendCount = 0
			s.mu.Unlock()
			s.send(ctx, EventPrintFailed, payload)
		}
	}
}

func (s *NotificationService) send(ctx context.Context, event string, payload map[string]any) {
	if client := s.currentClient(); client != nil {
		// Attach a snapshot when the user enabled "include image" (or the
		// APPRISE_ATTACH env override). This is what puts a photo of the
		// finished print in the notification / email.
		want := client.settings.Progress.IncludeImage || envBool("APPRISE_ATTACH")
		attachments := maybeSnapshotAttachment(ctx, s.snapshot, want)
		result := client.SendEventDetailed(ctx, event, payload, attachments)
		s.recordDelivery(extractFilename(payload), event, result)
	}
	if announcement := s.currentAnnouncement(); announcement != nil && announcement.Enabled {
		result := SendHomeAnnouncement(ctx, *announcement, event, payload)
		s.recordDelivery(extractFilename(payload), event, result)
	}
}

func (s *NotificationService) currentClient() *Client {
	if s.cfg == nil {
		return nil
	}
	cfg, err := s.cfg.Load()
	if err != nil || cfg == nil {
		return nil
	}
	resolved := ResolveAppriseEnv(cfg.Notifications.Apprise)
	return newClient(resolved)
}

func (s *NotificationService) currentAnnouncement() *model.HomeAnnouncementConfig {
	if s.cfg == nil {
		return nil
	}
	cfg, err := s.cfg.Load()
	if err != nil || cfg == nil {
		return nil
	}
	announcement := cfg.Notifications.Announcement
	return &announcement
}

func (s *NotificationService) recordDelivery(filename, event string, result DeliveryResult) {
	if s.history == nil {
		return
	}
	entry := db.HistoryNotificationResult{
		At:          result.At,
		Event:       event,
		OK:          result.OK,
		Message:     result.Message,
		Transport:   result.Transport,
		Target:      result.Target,
		StatusCode:  result.StatusCode,
		Title:       result.Title,
		ResponseRaw: result.ResponseRaw,
	}
	if err := s.history.AppendNotificationResult(filename, "", entry); err != nil {
		slog.Warn("failed to append notification result to history", "filename", filename, "event", event, "err", err)
	}
}

func maybeSnapshotAttachment(ctx context.Context, snapshot SnapshotCapturer, want bool) []string {
	if !want || snapshot == nil {
		return nil
	}
	tmp, err := os.CreateTemp("", "apprise-*.jpg")
	if err != nil {
		return nil
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	defer os.Remove(tmpPath)

	snapCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	if err := snapshot.CaptureSnapshot(snapCtx, tmpPath); err != nil {
		return nil
	}
	data, err := os.ReadFile(tmpPath)
	if err != nil || len(data) == 0 {
		return nil
	}
	encoded := base64.StdEncoding.EncodeToString(data)
	return []string{"data:image/jpeg;base64," + encoded}
}

func envBool(key string) bool {
	raw := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	if raw == "" {
		return false
	}
	v, err := strconv.ParseBool(raw)
	if err == nil {
		return v
	}
	switch raw {
	case "yes", "y", "on", "t", "1":
		return true
	case "no", "n", "off", "f", "0":
		return false
	default:
		return false
	}
}

func extractFilename(payload map[string]any) string {
	for _, key := range []string{"name", "fileName", "filename", "file_name", "gcode", "gcode_name", "filePath"} {
		v, ok := payload[key].(string)
		if !ok || strings.TrimSpace(v) == "" {
			continue
		}
		if key == "filePath" {
			return filepath.Base(v)
		}
		return strings.TrimSpace(v)
	}
	return ""
}

func extractProgress(payload map[string]any) (int, bool) {
	if v, ok := asInt(payload["progress"]); ok {
		if v < 0 {
			return 0, true
		}
		if v > 100 {
			if v <= 10000 {
				return v / 100, true
			}
			return 100, true
		}
		return v, true
	}
	return 0, false
}

func asInt(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case int8:
		return int(x), true
	case int16:
		return int(x), true
	case int32:
		return int(x), true
	case int64:
		return int(x), true
	case uint:
		return int(x), true
	case uint8:
		return int(x), true
	case uint16:
		return int(x), true
	case uint32:
		return int(x), true
	case uint64:
		return int(x), true
	case float32:
		return int(x), true
	case float64:
		return int(x), true
	default:
		return 0, false
	}
}
