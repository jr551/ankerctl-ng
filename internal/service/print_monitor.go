package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/django1982/ankerctl/internal/config"
	"github.com/django1982/ankerctl/internal/db"
	"github.com/django1982/ankerctl/internal/model"
)

type PrintMonitorResult struct {
	At                  time.Time      `json:"at"`
	Filename            string         `json:"filename,omitempty"`
	ProviderURL         string         `json:"provider_url,omitempty"`
	Model               string         `json:"model,omitempty"`
	Prompt              string         `json:"prompt,omitempty"`
	FrameCount          int            `json:"frame_count,omitempty"`
	FrameSpacingSec     int            `json:"frame_spacing_sec,omitempty"`
	ContactSheet        string         `json:"contact_sheet,omitempty"`
	ReferenceImage      bool           `json:"reference_image"`
	Metadata            map[string]any `json:"metadata,omitempty"`
	HTTPStatus          int            `json:"http_status,omitempty"`
	RawResponse         string         `json:"raw_response,omitempty"`
	ModelFailing        bool           `json:"model_failing"`
	Failing             bool           `json:"failing"`
	ThresholdPassed     bool           `json:"threshold_passed"`
	Confidence          float64        `json:"confidence,omitempty"`
	ConfidenceThreshold float64        `json:"confidence_threshold,omitempty"`
	Reason              string         `json:"reason,omitempty"`
	Error               string         `json:"error,omitempty"`
	Manual              bool           `json:"manual,omitempty"`
	EvidenceRelpath     string         `json:"evidence_relpath,omitempty"`
	EvidenceExpiresAt   *time.Time     `json:"evidence_expires_at,omitempty"`
}

type PrintMonitorStatus struct {
	Configured bool                `json:"configured"`
	Active     bool                `json:"active"`
	Running    bool                `json:"running"`
	LastCheck  *time.Time          `json:"last_check,omitempty"`
	NextCheck  *time.Time          `json:"next_check,omitempty"`
	LastResult *PrintMonitorResult `json:"last_result,omitempty"`
}

type printMonitorConfigCmd struct {
	Config model.PrintMonitorConfig
}

type printMonitorRunCmd struct{}

type PrintMonitorService struct {
	BaseWorker

	mu          sync.Mutex
	log         *slog.Logger
	cfgMgr      *config.Manager
	history     *db.DB
	archiver    *GCodeArchiver
	snapshotter SnapshotOnly
	httpClient  *http.Client

	cfg           model.PrintMonitorConfig
	active        bool
	filename      string
	lastTelemetry map[string]any
	checkRunning  bool
	lastCheck     *time.Time
	nextCheck     *time.Time
	lastResult    *PrintMonitorResult

	cmdCh chan any
}

func NewPrintMonitorService(cfgMgr *config.Manager, cfg model.PrintMonitorConfig, snapshotter SnapshotOnly) *PrintMonitorService {
	s := &PrintMonitorService{
		BaseWorker:  NewBaseWorker("printmonitor"),
		log:         slog.With("service", "printmonitor"),
		cfgMgr:      cfgMgr,
		snapshotter: snapshotter,
		httpClient:  &http.Client{Timeout: 90 * time.Second},
		cfg:         normalizePrintMonitorConfig(cfg),
		cmdCh:       make(chan any, 8),
	}
	s.BindHooks(s)
	return s
}

func (s *PrintMonitorService) WithReferenceArchive(history *db.DB, archiver *GCodeArchiver) *PrintMonitorService {
	s.history = history
	s.archiver = archiver
	return s
}

func (s *PrintMonitorService) Configure(cfg model.PrintMonitorConfig) {
	cmd := printMonitorConfigCmd{Config: normalizePrintMonitorConfig(cfg)}
	select {
	case s.cmdCh <- cmd:
	default:
		s.mu.Lock()
		s.cfg = cmd.Config
		s.mu.Unlock()
	}
}

func (s *PrintMonitorService) RunOnce() {
	select {
	case s.cmdCh <- printMonitorRunCmd{}:
	default:
	}
}

func (s *PrintMonitorService) RunOnceResult(ctx context.Context) (PrintMonitorResult, bool) {
	s.mu.Lock()
	if s.checkRunning {
		s.mu.Unlock()
		return PrintMonitorResult{At: time.Now(), Error: "print monitor check already running"}, false
	}
	cfg := s.cfg
	filename := s.filename
	s.checkRunning = true
	s.mu.Unlock()

	result := s.runCheck(ctx, cfg, filename, true)
	s.finishCheck(ctx, cfg, result)
	return result, true
}

func (s *PrintMonitorService) Status() PrintMonitorStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return PrintMonitorStatus{
		Configured: s.cfg.Enabled && strings.TrimSpace(s.cfg.OpenRouterKey) != "" && strings.TrimSpace(s.cfg.Model) != "",
		Active:     s.active,
		Running:    s.checkRunning,
		LastCheck:  cloneTimePtr(s.lastCheck),
		NextCheck:  cloneTimePtr(s.nextCheck),
		LastResult: clonePrintMonitorResult(s.lastResult),
	}
}

func (s *PrintMonitorService) Notify(data any) {
	s.BaseWorker.Notify(data)
	payload, ok := data.(map[string]any)
	if !ok {
		return
	}

	if _, hasCommand := payload["commandType"]; hasCommand || payload["event"] == "print_state" {
		s.mu.Lock()
		telemetry := cloneMapAny(s.lastTelemetry)
		if telemetry == nil {
			telemetry = map[string]any{}
		}
		for k, v := range payload {
			telemetry[k] = v
		}
		s.lastTelemetry = telemetry
		s.mu.Unlock()
	}

	if payload["event"] != "print_state" {
		return
	}
	state, ok := asIntIface(payload["state"])
	if !ok {
		return
	}
	filename, _ := payload["filename"].(string)
	s.mu.Lock()
	defer s.mu.Unlock()
	switch state {
	case mqttStatePrinting:
		s.active = true
		if filename != "" {
			s.filename = filename
		}
		if s.nextCheck == nil {
			now := time.Now()
			s.nextCheck = &now
		}
	case mqttStateIdle, mqttStatePaused, mqttStateAborted:
		s.active = false
		s.nextCheck = nil
	}
}

func (s *PrintMonitorService) WorkerStart() error { return nil }

func (s *PrintMonitorService) WorkerRun(ctx context.Context) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case raw := <-s.cmdCh:
			s.handleCommand(ctx, raw)
		case now := <-ticker.C:
			s.tick(ctx, now)
		}
	}
}

func (s *PrintMonitorService) WorkerStop() {}

func (s *PrintMonitorService) handleCommand(ctx context.Context, raw any) {
	switch cmd := raw.(type) {
	case printMonitorConfigCmd:
		s.mu.Lock()
		s.cfg = normalizePrintMonitorConfig(cmd.Config)
		if !s.cfg.Enabled {
			s.nextCheck = nil
		}
		s.mu.Unlock()
	case printMonitorRunCmd:
		s.startCheck(ctx, true)
	}
}

func (s *PrintMonitorService) tick(ctx context.Context, now time.Time) {
	s.mu.Lock()
	cfg := s.cfg
	active := s.active
	running := s.checkRunning
	next := s.nextCheck
	if !cfg.Enabled || !active || running || next == nil || now.Before(*next) {
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	s.startCheck(ctx, false)
}

func (s *PrintMonitorService) startCheck(ctx context.Context, manual bool) {
	s.mu.Lock()
	if s.checkRunning {
		s.mu.Unlock()
		return
	}
	cfg := s.cfg
	filename := s.filename
	if !manual && (!cfg.Enabled || !s.active) {
		s.mu.Unlock()
		return
	}
	s.checkRunning = true
	s.mu.Unlock()

	go func() {
		result := s.runCheck(ctx, cfg, filename, manual)
		s.finishCheck(ctx, cfg, result)
	}()
}

func (s *PrintMonitorService) runCheck(ctx context.Context, cfg model.PrintMonitorConfig, filename string, manual bool) PrintMonitorResult {
	result := PrintMonitorResult{
		At:                  time.Now(),
		Filename:            filename,
		ProviderURL:         printMonitorChatCompletionsURL(cfg.OpenRouterURL),
		Model:               cfg.Model,
		Prompt:              cfg.Prompt,
		FrameCount:          cfg.FrameCount,
		FrameSpacingSec:     cfg.FrameSpacingSec,
		ConfidenceThreshold: cfg.ConfidenceThreshold,
		Manual:              manual,
	}
	if strings.TrimSpace(cfg.OpenRouterKey) == "" {
		result.Error = "AI provider API key is not configured"
		return result
	}
	if s.snapshotter == nil {
		result.Error = "camera snapshotter is unavailable"
		return result
	}

	dir, err := os.MkdirTemp("", "ankerctl-print-monitor-*")
	if err != nil {
		result.Error = err.Error()
		return result
	}
	defer os.RemoveAll(dir)

	paths := make([]string, 0, cfg.FrameCount)
	for i := 0; i < cfg.FrameCount; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				result.Error = ctx.Err().Error()
				return result
			case <-time.After(time.Duration(cfg.FrameSpacingSec) * time.Second):
			}
		}
		p := filepath.Join(dir, fmt.Sprintf("frame-%02d.jpg", i+1))
		if err := s.snapshotter.CaptureSnapshot(ctx, p); err != nil {
			result.Error = err.Error()
			return result
		}
		paths = append(paths, p)
	}

	sheet, err := buildContactSheet(paths)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	if relpath, expiresAt, err := s.storeEvidence(sheet); err == nil {
		result.EvidenceRelpath = relpath
		result.EvidenceExpiresAt = expiresAt
	}
	result.ContactSheet = "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(sheet)
	result.Metadata = s.printMonitorMetadata(ctx, cfg, filename)
	referenceImage := s.referenceThumbnail(filename)
	result.ReferenceImage = len(referenceImage) > 0
	failing, confidence, reason, rawResponse, status, err := s.callOpenRouter(ctx, cfg, sheet, referenceImage, result.Metadata)
	result.RawResponse = rawResponse
	result.HTTPStatus = status
	if err != nil {
		result.Error = err.Error()
		return result
	}
	result.ModelFailing = failing
	result.Confidence = confidence
	result.ThresholdPassed = confidence >= cfg.ConfidenceThreshold
	result.Failing = failing && result.ThresholdPassed
	result.Reason = reason
	return result
}

func (s *PrintMonitorService) finishCheck(ctx context.Context, cfg model.PrintMonitorConfig, result PrintMonitorResult) {
	now := time.Now()
	s.mu.Lock()
	s.checkRunning = false
	s.lastCheck = &now
	s.lastResult = &result
	if cfg.Enabled && s.active {
		next := now.Add(time.Duration(cfg.IntervalSec) * time.Second)
		s.nextCheck = &next
	} else {
		s.nextCheck = nil
	}
	s.mu.Unlock()
	s.Notify(map[string]any{"type": "print_monitor.result", "result": result})
	s.recordHistoryResult(result)
	if result.Failing {
		s.maybeAutoOff(ctx)
	}
}

func (s *PrintMonitorService) callOpenRouter(ctx context.Context, cfg model.PrintMonitorConfig, imageJPEG []byte, referencePNG []byte, metadata map[string]any) (bool, float64, string, string, int, error) {
	imageURL := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(imageJPEG)
	metaJSON, _ := json.MarshalIndent(metadata, "", "  ")
	userContent := []map[string]any{
		{"type": "text", "text": "Inspect the live camera contact sheet and metadata. If a reference slicer thumbnail is present, use it as the expected shape/layout. Also inspect any visible filament path into the toolhead and treat a missing, snapped, kinked, misrouted, or obviously non-feeding filament path as a failure signal when supported by the image. Reply with strict JSON only.\n\nMetadata:\n" + string(metaJSON)},
		{"type": "image_url", "image_url": map[string]string{"url": imageURL}},
	}
	if len(referencePNG) > 0 {
		referenceURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(referencePNG)
		userContent = append(userContent, map[string]any{"type": "image_url", "image_url": map[string]string{"url": referenceURL}})
	}
	payload := map[string]any{
		"model": cfg.Model,
		"messages": []map[string]any{
			{
				"role":    "system",
				"content": cfg.Prompt,
			},
			{
				"role":    "user",
				"content": userContent,
			},
		},
		"response_format": map[string]string{"type": "json_object"},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return false, 0, "", "", 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, printMonitorChatCompletionsURL(cfg.OpenRouterURL), bytes.NewReader(body))
	if err != nil {
		return false, 0, "", "", 0, err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.OpenRouterKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("HTTP-Referer", "https://github.com/Django1982/ankerctl_go_remake")
	req.Header.Set("X-Title", "ankerctl")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return false, 0, "", "", 0, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	rawBody := strings.TrimSpace(string(data))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, 0, "", rawBody, resp.StatusCode, fmt.Errorf("AI provider returned HTTP %d: %s", resp.StatusCode, rawBody)
	}
	var apiResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &apiResp); err != nil {
		return false, 0, "", rawBody, resp.StatusCode, err
	}
	if len(apiResp.Choices) == 0 {
		return false, 0, "", rawBody, resp.StatusCode, fmt.Errorf("AI provider returned no choices")
	}
	var parsed struct {
		Failing    bool    `json:"failing"`
		Confidence float64 `json:"confidence"`
		Reason     string  `json:"reason"`
	}
	content := strings.TrimSpace(apiResp.Choices[0].Message.Content)
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		return false, 0, "", content, resp.StatusCode, fmt.Errorf("AI provider returned non-JSON content: %w", err)
	}
	return parsed.Failing, parsed.Confidence, parsed.Reason, content, resp.StatusCode, nil
}

func (s *PrintMonitorService) printMonitorMetadata(ctx context.Context, cfg model.PrintMonitorConfig, filename string) map[string]any {
	s.mu.Lock()
	telemetry := cloneMapAny(s.lastTelemetry)
	active := s.active
	s.mu.Unlock()

	meta := map[string]any{
		"filename": filename,
		"active":   active,
		"monitor": map[string]any{
			"interval_sec":      cfg.IntervalSec,
			"frame_count":       cfg.FrameCount,
			"frame_spacing_sec": cfg.FrameSpacingSec,
			"model":             cfg.Model,
			"provider_url":      printMonitorChatCompletionsURL(cfg.OpenRouterURL),
		},
	}
	if len(telemetry) > 0 {
		meta["printer_telemetry"] = telemetry
	}
	if s.cfgMgr != nil {
		if appCfg, err := s.cfgMgr.Load(); err == nil && appCfg != nil {
			meta["camera"] = map[string]any{
				"configured": appCfg.Camera.PerPrinter != nil,
			}
			if appCfg.SmartSocket.Enabled {
				socket := map[string]any{
					"enabled":              appCfg.SmartSocket.Enabled,
					"switch_entity":        appCfg.SmartSocket.SwitchEntity,
					"power_entity":         appCfg.SmartSocket.PowerEntity,
					"power_saving_enabled": appCfg.SmartSocket.PowerSavingEnabled,
				}
				if appCfg.SmartSocket.PowerEntity != "" {
					client := NewHomeAssistantClient(appCfg.SmartSocket.BaseURL, appCfg.SmartSocket.Token)
					if state, err := client.State(ctx, appCfg.SmartSocket.PowerEntity); err == nil {
						socket["power"] = state.State
						if unit, ok := state.Attributes["unit_of_measurement"].(string); ok {
							socket["power_unit"] = unit
						}
					}
				}
				if appCfg.SmartSocket.SwitchEntity != "" {
					client := NewHomeAssistantClient(appCfg.SmartSocket.BaseURL, appCfg.SmartSocket.Token)
					if state, err := client.State(ctx, appCfg.SmartSocket.SwitchEntity); err == nil {
						socket["state"] = state.State
						if !state.LastChanged.IsZero() {
							socket["last_changed"] = state.LastChanged
						}
					}
				}
				meta["smart_socket"] = socket
			}
		}
	}
	return meta
}

func (s *PrintMonitorService) referenceThumbnail(filename string) []byte {
	if s == nil || s.history == nil || s.archiver == nil || strings.TrimSpace(filename) == "" {
		return nil
	}
	records, err := s.history.GetHistory(100, 0)
	if err != nil {
		return nil
	}
	for _, rec := range records {
		if rec.Filename != filename || rec.ArchiveRelpath == nil || *rec.ArchiveRelpath == "" {
			continue
		}
		data, err := s.archiver.ReadThumbnail(*rec.ArchiveRelpath)
		if err == nil && len(data) > 0 {
			return data
		}
	}
	return nil
}

func (s *PrintMonitorService) maybeAutoOff(ctx context.Context) {
	if s.cfgMgr == nil {
		return
	}
	cfg, err := s.cfgMgr.Load()
	if err != nil || cfg == nil || !cfg.SmartSocket.Enabled || !cfg.SmartSocket.AutoOffOnFail {
		return
	}
	if strings.TrimSpace(cfg.SmartSocket.SwitchEntity) == "" {
		return
	}
	client := NewHomeAssistantClient(cfg.SmartSocket.BaseURL, cfg.SmartSocket.Token)
	if err := client.CallService(ctx, "switch", "turn_off", cfg.SmartSocket.SwitchEntity); err != nil && s.log != nil {
		s.log.Warn("failed to turn off smart socket after print monitor failure", "err", err)
	}
}

func buildContactSheet(paths []string) ([]byte, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("no frames captured")
	}
	images := make([]image.Image, 0, len(paths))
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			return nil, err
		}
		img, _, err := image.Decode(f)
		_ = f.Close()
		if err != nil {
			return nil, err
		}
		images = append(images, img)
	}
	cellW, cellH := 320, 180
	sheet := image.NewRGBA(image.Rect(0, 0, cellW*len(images), cellH))
	for i, img := range images {
		dst := image.Rect(i*cellW, 0, (i+1)*cellW, cellH)
		scaleNearest(sheet, dst, img)
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, sheet, &jpeg.Options{Quality: 85}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func scaleNearest(dst *image.RGBA, rect image.Rectangle, src image.Image) {
	b := src.Bounds()
	srcW := b.Dx()
	srcH := b.Dy()
	dstW := rect.Dx()
	dstH := rect.Dy()
	if srcW <= 0 || srcH <= 0 || dstW <= 0 || dstH <= 0 {
		return
	}
	for y := 0; y < dstH; y++ {
		sy := b.Min.Y + (y*srcH)/dstH
		for x := 0; x < dstW; x++ {
			sx := b.Min.X + (x*srcW)/dstW
			dst.Set(rect.Min.X+x, rect.Min.Y+y, src.At(sx, sy))
		}
	}
}

func normalizePrintMonitorConfig(cfg model.PrintMonitorConfig) model.PrintMonitorConfig {
	def := model.DefaultPrintMonitorConfig()
	if cfg.IntervalSec <= 0 {
		cfg.IntervalSec = def.IntervalSec
	}
	if cfg.IntervalSec < 30 {
		cfg.IntervalSec = 30
	}
	if cfg.FrameCount <= 0 {
		cfg.FrameCount = def.FrameCount
	}
	if cfg.FrameCount > 5 {
		cfg.FrameCount = 5
	}
	if cfg.FrameSpacingSec <= 0 {
		cfg.FrameSpacingSec = def.FrameSpacingSec
	}
	if cfg.FrameSpacingSec > 10 {
		cfg.FrameSpacingSec = 10
	}
	if strings.TrimSpace(cfg.OpenRouterURL) == "" {
		cfg.OpenRouterURL = def.OpenRouterURL
	}
	if strings.TrimSpace(cfg.Model) == "" {
		cfg.Model = def.Model
	}
	if strings.TrimSpace(cfg.Prompt) == "" {
		cfg.Prompt = def.Prompt
	}
	if cfg.ConfidenceThreshold <= 0 {
		cfg.ConfidenceThreshold = def.ConfidenceThreshold
	}
	if cfg.ConfidenceThreshold > 1 {
		cfg.ConfidenceThreshold = 1
	}
	return cfg
}

func (s *PrintMonitorService) recordHistoryResult(result PrintMonitorResult) {
	if s == nil || s.history == nil {
		return
	}
	entry := db.HistoryAIResult{
		At:                  result.At,
		Manual:              result.Manual,
		ProviderURL:         result.ProviderURL,
		Model:               result.Model,
		Prompt:              result.Prompt,
		FrameCount:          result.FrameCount,
		FrameSpacingSec:     result.FrameSpacingSec,
		ReferenceImage:      result.ReferenceImage,
		ModelFailing:        result.ModelFailing,
		Failing:             result.Failing,
		ThresholdPassed:     result.ThresholdPassed,
		Confidence:          result.Confidence,
		ConfidenceThreshold: result.ConfidenceThreshold,
		Reason:              result.Reason,
		Error:               result.Error,
		HTTPStatus:          result.HTTPStatus,
		RawResponse:         result.RawResponse,
		Metadata:            result.Metadata,
		EvidenceRelpath:     result.EvidenceRelpath,
		EvidenceExpiresAt:   result.EvidenceExpiresAt,
	}
	if err := s.history.AppendAIResult(result.Filename, "", entry); err != nil && s.log != nil {
		s.log.Warn("failed to append print monitor result to history", "filename", result.Filename, "err", err)
	}
}

func (s *PrintMonitorService) storeEvidence(sheet []byte) (string, *time.Time, error) {
	if s == nil || s.cfgMgr == nil || len(sheet) == 0 {
		return "", nil, nil
	}
	dir := filepath.Join(s.cfgMgr.ConfigDir(), "print-monitor-history")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nil, err
	}
	if err := pruneOldEvidence(dir, time.Now().UTC()); err != nil && s.log != nil {
		s.log.Warn("failed pruning old print monitor evidence", "err", err)
	}
	now := time.Now().UTC()
	expires := now.Add(24 * time.Hour)
	name := fmt.Sprintf("%s-%d.jpg", now.Format("20060102-150405"), now.UnixNano())
	fullPath := filepath.Join(dir, name)
	if err := os.WriteFile(fullPath, sheet, 0o600); err != nil {
		return "", nil, err
	}
	return name, &expires, nil
}

func pruneOldEvidence(dir string, now time.Time) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if now.Sub(info.ModTime()) > 24*time.Hour {
			_ = os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
	return nil
}

func printMonitorChatCompletionsURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return raw
	}
	path := strings.TrimRight(u.Path, "/")
	if strings.HasSuffix(path, "/chat/completions") {
		return u.String()
	}
	u.Path = path + "/chat/completions"
	return u.String()
}

func cloneTimePtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	v := *t
	return &v
}

func clonePrintMonitorResult(r *PrintMonitorResult) *PrintMonitorResult {
	if r == nil {
		return nil
	}
	v := *r
	return &v
}

func cloneMapAny(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	dst := make(map[string]any, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}
