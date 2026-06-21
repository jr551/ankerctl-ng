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
	AnimalDetected      bool           `json:"animal_detected,omitempty"`
	Animal              string         `json:"animal,omitempty"`
	Error               string         `json:"error,omitempty"`
	Manual              bool           `json:"manual,omitempty"`
	EvidenceRelpath     string         `json:"evidence_relpath,omitempty"`
	EvidenceExpiresAt   *time.Time     `json:"evidence_expires_at,omitempty"`
}

type PrintMonitorStatus struct {
	Configured       bool                `json:"configured"`
	Active           bool                `json:"active"`
	Running          bool                `json:"running"`
	LastCheck        *time.Time          `json:"last_check,omitempty"`
	NextCheck        *time.Time          `json:"next_check,omitempty"`
	LastResult       *PrintMonitorResult `json:"last_result,omitempty"`
	PendingOffAt     *time.Time          `json:"pending_off_at,omitempty"`     // power will cut at this time unless cancelled
	PendingOffReason string              `json:"pending_off_reason,omitempty"` // why the failure power-off is pending
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
	notifier      AnimalStopNotifier

	// Graceful auto-off: on a failing check we notify the user and start a grace
	// countdown; power is cut at pendingOffAt unless the user cancels (which
	// bumps pendingOffGen, invalidating the in-flight timer).
	pendingOffAt     *time.Time
	pendingOffReason string
	pendingOffGen    int

	cmdCh chan any
}

const autoOffGraceSec = 120 // user has 2 minutes to cancel a failure power-off

// AnimalStopNotifier sends user-facing alerts (with an optional camera frame):
// the animal-detection emergency stop, and the "print may be failing — power
// will cut soon" warning. It is satisfied by the notifications service; kept as
// an interface here so the service package does not import notifications (which
// would be an import cycle).
type AnimalStopNotifier interface {
	NotifyAnimalEmergencyStop(ctx context.Context, payload map[string]any, attachments []string)
	NotifyPrintFailing(ctx context.Context, payload map[string]any, attachments []string)
}

func NewPrintMonitorService(cfgMgr *config.Manager, cfg model.PrintMonitorConfig, snapshotter SnapshotOnly) *PrintMonitorService {
	s := &PrintMonitorService{
		BaseWorker:  NewBaseWorker("printmonitor"),
		log:         slog.With("service", "printmonitor"),
		cfgMgr:      cfgMgr,
		snapshotter: snapshotter,
		httpClient:  &http.Client{Timeout: 180 * time.Second},
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

// WithNotifier wires an optional notifier used to alert the user when the
// animal-detection emergency stop cuts printer power.
func (s *PrintMonitorService) WithNotifier(n AnimalStopNotifier) *PrintMonitorService {
	s.notifier = n
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
		Configured:       s.cfg.Enabled && strings.TrimSpace(s.cfg.OpenRouterKey) != "" && strings.TrimSpace(s.cfg.Model) != "",
		Active:           s.active,
		Running:          s.checkRunning,
		LastCheck:        cloneTimePtr(s.lastCheck),
		NextCheck:        cloneTimePtr(s.nextCheck),
		LastResult:       clonePrintMonitorResult(s.lastResult),
		PendingOffAt:     cloneTimePtr(s.pendingOffAt),
		PendingOffReason: s.pendingOffReason,
	}
}

// CancelAutoOff aborts a pending failure power-off (user pressed the cancel
// button during the grace countdown). Bumping the generation invalidates the
// in-flight timer.
func (s *PrintMonitorService) CancelAutoOff() bool {
	s.mu.Lock()
	had := s.pendingOffAt != nil
	s.pendingOffAt = nil
	s.pendingOffReason = ""
	s.pendingOffGen++
	s.mu.Unlock()
	if had {
		if s.log != nil {
			s.log.Info("failure auto-off cancelled by user")
		}
		s.Notify(map[string]any{"type": "print_monitor.autooff_cancelled"})
	}
	return had
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

	// First layer down → bring an AI check forward to catch bed-adhesion
	// failures early (corners lifting, not sticking, dragging).
	if payload["event"] == "first_layer" {
		s.mu.Lock()
		if s.cfg.Enabled && s.active {
			now := time.Now()
			s.nextCheck = &now
		}
		if fn, _ := payload["filename"].(string); fn != "" {
			s.filename = fn
		}
		s.mu.Unlock()
		return
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
		// Print ended/paused — clear any pending failure power-off (the timer also
		// guards on s.active, so it won't fire; this just clears the UI state).
		s.pendingOffAt = nil
		s.pendingOffReason = ""
		s.pendingOffGen++
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
	verdict, err := s.callOpenRouter(ctx, cfg, sheet, referenceImage, result.Metadata)
	result.RawResponse = verdict.Raw
	result.HTTPStatus = verdict.HTTPStatus
	if err != nil {
		result.Error = err.Error()
		return result
	}
	result.ModelFailing = verdict.Failing
	result.Confidence = verdict.Confidence
	result.ThresholdPassed = verdict.Confidence >= cfg.ConfidenceThreshold
	result.Failing = verdict.Failing && result.ThresholdPassed
	result.Reason = verdict.Reason
	result.AnimalDetected = cfg.EmergencyStopOnAnimal && verdict.AnimalDetected
	result.Animal = verdict.Animal
	if result.AnimalDetected {
		note := "⚠ Non-human animal detected near the printer"
		if result.Animal != "" {
			note += " (" + result.Animal + ")"
		}
		note += " — emergency stop: cutting printer power."
		if result.Reason != "" {
			result.Reason = note + " " + result.Reason
		} else {
			result.Reason = note
		}
	}
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
	if result.AnimalDetected && cfg.EmergencyStopOnAnimal {
		s.emergencyStopForAnimal(ctx, result) // safety stop — always instant
	} else if result.Failing {
		s.scheduleGracefulOff(ctx, result) // failure — 2 min countdown the user can cancel
	} else {
		s.CancelAutoOff() // a healthy check clears any pending failure power-off
	}
}

// emergencyStopForAnimal cuts mains power to the printer immediately when a
// non-human animal is detected in frame. Unlike maybeAutoOff (print-quality
// failures, gated by AutoOffOnFail), this is a safety stop: it fires whenever
// the smart socket is configured, regardless of AutoOffOnFail, and alerts the
// user with the camera frame.
func (s *PrintMonitorService) emergencyStopForAnimal(ctx context.Context, result PrintMonitorResult) {
	if s.log != nil {
		s.log.Warn("EMERGENCY STOP: non-human animal detected near printer — cutting power",
			"animal", result.Animal, "filename", result.Filename)
	}
	powerCut := false
	if s.cfgMgr != nil {
		if cfg, err := s.cfgMgr.Load(); err == nil && cfg != nil {
			if cfg.SmartSocket.Enabled && strings.TrimSpace(cfg.SmartSocket.SwitchEntity) != "" {
				client := NewHomeAssistantClient(cfg.SmartSocket.BaseURL, cfg.SmartSocket.Token)
				if err := client.CallService(ctx, "switch", "turn_off", cfg.SmartSocket.SwitchEntity); err != nil {
					if s.log != nil {
						s.log.Error("failed to cut printer power after animal detection", "err", err)
					}
				} else {
					powerCut = true
				}
			} else if s.log != nil {
				s.log.Warn("animal-detected emergency stop requested but smart socket is not configured; cannot cut power")
			}
		}
	}
	s.notifyAnimalStop(ctx, result, powerCut)
}

// notifyAnimalStop sends a best-effort user alert (with the camera contact
// sheet attached) describing the animal-detection emergency stop.
func (s *PrintMonitorService) notifyAnimalStop(ctx context.Context, result PrintMonitorResult, powerCut bool) {
	if s.notifier == nil {
		return
	}
	animal := strings.TrimSpace(result.Animal)
	if animal == "" {
		animal = "an animal"
	}
	reason := "🐾 Non-human animal detected near the printer (" + animal + "). "
	if powerCut {
		reason += "Emergency stop triggered: printer power has been cut."
	} else {
		reason += "Emergency stop requested but the smart socket is not configured, so power could not be cut."
	}
	payload := map[string]any{
		"filename": result.Filename,
		"reason":   reason,
	}
	var attachments []string
	if result.ContactSheet != "" {
		attachments = []string{result.ContactSheet}
	}
	s.notifier.NotifyAnimalEmergencyStop(ctx, payload, attachments)
}

// aiVerdict is the parsed result of a print-monitor AI check.
type aiVerdict struct {
	Failing        bool
	Confidence     float64
	Reason         string
	AnimalDetected bool
	Animal         string
	Raw            string
	HTTPStatus     int
}

func (s *PrintMonitorService) callOpenRouter(ctx context.Context, cfg model.PrintMonitorConfig, imageJPEG []byte, referencePNG []byte, metadata map[string]any) (aiVerdict, error) {
	imageURL := "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(imageJPEG)
	metaJSON, _ := json.MarshalIndent(metadata, "", "  ")
	base := "Inspect the live camera contact sheet and metadata. If a reference slicer thumbnail is present, use it as the expected shape/layout. Also inspect any visible filament path into the toolhead and treat a missing, snapped, kinked, misrouted, or obviously non-feeding filament path as a failure signal when supported by the image.\n\nIMPORTANT — avoid false alarms: a small print, or one in its early layers, can look tiny or sparse on a large bed, and a mostly-empty-looking bed early in a print is NORMAL. Do NOT report failing just because the bed looks empty/sparse, the object is small, or there is a single stray strand. Only report failing for a clear, substantial failure: large spaghetti/stringing across the part, a detached or badly shifted part, or a big blob. When unsure, set failing false with low confidence."
	instruction := base + "\n\nReply with strict JSON only: {\"failing\": boolean, \"confidence\": number, \"reason\": string}."
	if cfg.EmergencyStopOnAnimal {
		instruction = base + "\n\nSAFETY CHECK: also look for any real, live non-human animal physically present in the scene (e.g. a pet such as a cat, dog, bird, or rodent). If one is clearly visible, set \"animal_detected\" true and name it in \"animal\". Be VERY conservative — this triggers an immediate power cut: the object on the print bed is the 3D print and is NEVER an animal even if it is shaped like one (a cat/dog/dinosaur model is just a print), so ignore the printed model entirely. Do NOT count 3D-printed models, figurines, toys, photos, screens, or pictures of animals — only a real, living, moving animal that has entered the room. When unsure, set animal_detected false.\n\nReply with strict JSON only: {\"failing\": boolean, \"confidence\": number, \"reason\": string, \"animal_detected\": boolean, \"animal\": string}."
	}
	userContent := []map[string]any{
		{"type": "text", "text": instruction + "\n\nMetadata:\n" + string(metaJSON)},
		{"type": "image_url", "image_url": map[string]string{"url": imageURL}},
	}
	if len(referencePNG) > 0 {
		referenceURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(referencePNG)
		userContent = append(userContent, map[string]any{"type": "image_url", "image_url": map[string]string{"url": referenceURL}})
	}
	messages := []map[string]any{
		{"role": "system", "content": cfg.Prompt},
		{"role": "user", "content": userContent},
	}
	raw, status, err := s.chatCompletion(ctx, cfg, messages, true, 1024*1024)
	if err != nil {
		return aiVerdict{HTTPStatus: status}, err
	}
	var parsed struct {
		Failing        bool    `json:"failing"`
		Confidence     float64 `json:"confidence"`
		Reason         string  `json:"reason"`
		AnimalDetected bool    `json:"animal_detected"`
		Animal         string  `json:"animal"`
	}
	content := extractJSON(raw)
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		return aiVerdict{Raw: content, HTTPStatus: status}, fmt.Errorf("AI provider returned non-JSON content: %w", err)
	}
	return aiVerdict{
		Failing:        parsed.Failing,
		Confidence:     clamp01(parsed.Confidence),
		Reason:         strings.TrimSpace(parsed.Reason),
		AnimalDetected: parsed.AnimalDetected,
		Animal:         strings.TrimSpace(parsed.Animal),
		Raw:            content,
		HTTPStatus:     status,
	}, nil
}

// SliceCheckResult is the verdict from a post-slice AI sanity check.
type SliceCheckResult struct {
	Serious bool   `json:"serious"`
	Issue   string `json:"issue"`
}

// AnalyzeSliceImage sends a rendered slice-preview image (NOT the gcode) to the
// configured vision model and asks for SERIOUS printability problems only.
// Returns ok=false when no AI provider is configured (caller skips silently).
func (s *PrintMonitorService) AnalyzeSliceImage(ctx context.Context, imageDataURI string) (SliceCheckResult, bool, error) {
	s.mu.Lock()
	cfg := s.cfg
	s.mu.Unlock()
	if strings.TrimSpace(cfg.OpenRouterKey) == "" || strings.TrimSpace(cfg.Model) == "" {
		return SliceCheckResult{}, false, nil
	}
	const sys = "You are reviewing a 2D top-down toolpath preview of a model that was just sliced for 3D printing (green lines are extrusion paths on the bed). Reply with strict JSON only: {\"serious\": boolean, \"issue\": string}. Set serious=true ONLY for problems that would clearly ruin the print: there is essentially NO toolpath at all, or the toolpath is cut off / runs off the edge of the plate, or it is grossly distorted or degenerate. A small, simple, or sparse model that only covers part of the bed is completely FINE — do NOT flag sparse, small, or few-part layouts, and do NOT flag normal infill, perimeters, skirts, or gaps between parts. When in doubt, set serious=false. Keep issue to one short sentence; use an empty string when serious is false."
	messages := []map[string]any{
		{"role": "system", "content": sys},
		{"role": "user", "content": []map[string]any{
			{"type": "image_url", "image_url": map[string]string{"url": imageDataURI}},
		}},
	}
	raw, _, err := s.chatCompletion(ctx, cfg, messages, true, 1024*1024)
	if err != nil {
		return SliceCheckResult{}, false, err
	}
	var out SliceCheckResult
	if err := json.Unmarshal([]byte(extractJSON(raw)), &out); err != nil {
		return SliceCheckResult{}, false, fmt.Errorf("AI provider returned non-JSON content")
	}
	out.Issue = strings.TrimSpace(out.Issue)
	return out, true, nil
}

// DetectFilamentColor asks the vision model for the dominant filament colour in
// a camera frame and returns it as a "#RRGGBB" hex string. Returns ok=false when
// no AI provider is configured.
func (s *PrintMonitorService) DetectFilamentColor(ctx context.Context, imageDataURI string) (string, bool, error) {
	s.mu.Lock()
	cfg := s.cfg
	s.mu.Unlock()
	if strings.TrimSpace(cfg.OpenRouterKey) == "" || strings.TrimSpace(cfg.Model) == "" {
		return "", false, nil
	}
	const sys = "Look at this 3D printer camera image. Identify the dominant colour of the filament being printed (the plastic of the model / the extruded material), ignoring the bed, frame and background. Reply with strict JSON only: {\"hex\": \"#RRGGBB\"} using a full 6-digit hex colour. Use a best-effort estimate of the main colour; if you truly cannot tell, use an empty string for hex."
	messages := []map[string]any{
		{"role": "system", "content": sys},
		{"role": "user", "content": []map[string]any{
			{"type": "image_url", "image_url": map[string]string{"url": imageDataURI}},
		}},
	}
	raw, _, err := s.chatCompletion(ctx, cfg, messages, true, 1024*1024)
	if err != nil {
		return "", false, err
	}
	var out struct {
		Hex string `json:"hex"`
	}
	if err := json.Unmarshal([]byte(extractJSON(raw)), &out); err != nil {
		return "", false, fmt.Errorf("AI provider returned non-JSON content")
	}
	return normalizeHexColor(out.Hex), true, nil
}

// EditOpenSCAD asks the AI model to (re)write OpenSCAD source for a
// natural-language request, with optional reference images. Returns the complete
// updated code. ok=false when no AI provider is configured.
func (s *PrintMonitorService) EditOpenSCAD(ctx context.Context, currentSCAD, prompt string, images []string) (string, bool, error) {
	s.mu.Lock()
	cfg := s.cfg
	s.mu.Unlock()
	if strings.TrimSpace(cfg.OpenRouterKey) == "" || strings.TrimSpace(cfg.Model) == "" {
		return "", false, nil
	}
	const sys = "You are an expert in OpenSCAD. You are given the current OpenSCAD source (possibly empty) and a requested change, plus optional reference images. " +
		"Reply with ONLY raw OpenSCAD source code — no markdown code fences, no LaTeX (no $$ and no \\text{}), no language tags, no commentary, no explanation, not even a leading sentence. " +
		"Keep the code COMPACT so it generates fast: factor repeated or symmetric features into modules and use mirror() for left/right symmetry, and use a modest facet count ($fn around 24-48). " +
		"Produce a SINGLE watertight solid (union the parts) that is 3D-printable on a desktop FDM printer with NO supports: it must have a flat base resting on z=0, with no parts floating above the bed or disconnected from the rest of the model, and no feature thinner than about 1.5 mm. " +
		"Use millimetre units and keep the model within roughly a 120 mm cube. Make the requested change while keeping sensible existing structure; do not add example or test calls unless the request asks for them."
	userContent := []map[string]any{
		{"type": "text", "text": "Current OpenSCAD code:\n" + currentSCAD + "\n\nRequested change: " + prompt},
	}
	for _, img := range images {
		if strings.HasPrefix(img, "data:") {
			userContent = append(userContent, map[string]any{"type": "image_url", "image_url": map[string]string{"url": img}})
		}
	}
	messages := []map[string]any{
		{"role": "system", "content": sys},
		{"role": "user", "content": userContent},
	}
	raw, _, err := s.chatCompletion(ctx, cfg, messages, false, 2*1024*1024)
	if err != nil {
		return "", false, err
	}
	code := stripSCADProse(raw)
	if code == "" {
		return "", false, fmt.Errorf("AI returned empty code")
	}
	return code, true, nil
}

// stripSCADProse cleans an OpenSCAD code reply: it removes ``` fences and a
// single leading prose line like "Here is the updated code:" that some models
// prepend despite being told not to.
func stripSCADProse(raw string) string {
	code := strings.TrimSpace(stripCodeFence(raw))
	code = stripSCADWrappers(code)
	// Drop a leading natural-language line if the real code clearly starts later.
	if nl := strings.IndexByte(code, '\n'); nl > 0 {
		first := strings.TrimSpace(code[:nl])
		lower := strings.ToLower(first)
		looksLikeProse := strings.HasSuffix(first, ":") ||
			strings.HasPrefix(lower, "here") || strings.HasPrefix(lower, "sure") ||
			strings.HasPrefix(lower, "certainly") || strings.HasPrefix(lower, "below")
		// Only strip when it doesn't look like OpenSCAD (no code punctuation).
		if looksLikeProse && !strings.ContainsAny(first, "{}();=") {
			code = strings.TrimSpace(code[nl+1:])
		}
	}
	return code
}

// stripSCADWrappers removes LaTeX/markdown artifacts some models wrap code in —
// e.g. a leading "$$\text{openscad}" or a lone "openscad" language tag, or a
// trailing "$$" — none of which are valid OpenSCAD. It deliberately leaves real
// OpenSCAD special variables ($fn, $fa, $fs, …) alone: those start with a single
// '$', whereas the LaTeX artifact starts with a double "$$".
func stripSCADWrappers(code string) string {
	isJunk := func(s string) bool {
		t := strings.TrimSpace(s)
		switch {
		case t == "":
			return true
		case strings.HasPrefix(t, "$$"): // LaTeX display math, not an OpenSCAD $var
			return true
		case strings.HasPrefix(t, `\text`), strings.HasPrefix(t, `\(`), strings.HasPrefix(t, `\)`),
			strings.HasPrefix(t, `\[`), strings.HasPrefix(t, `\]`):
			return true
		}
		switch strings.ToLower(t) {
		case "openscad", "scad", "cad": // stray language tag on its own line
			return true
		}
		return false
	}
	lines := strings.Split(code, "\n")
	for len(lines) > 0 && isJunk(lines[0]) {
		lines = lines[1:]
	}
	for len(lines) > 0 && isJunk(lines[len(lines)-1]) {
		lines = lines[:len(lines)-1]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// stripCodeFence removes a leading ```lang fence and trailing ``` from any
// fenced code block (not just JSON), returning the inner body.
func stripCodeFence(content string) string {
	content = strings.TrimSpace(content)
	if !strings.HasPrefix(content, "```") {
		return content
	}
	rest := strings.TrimPrefix(content, "```")
	lineEnd := strings.IndexByte(rest, '\n')
	if lineEnd < 0 {
		return content
	}
	body := strings.TrimSpace(rest[lineEnd+1:])
	if strings.HasSuffix(body, "```") {
		body = strings.TrimSpace(strings.TrimSuffix(body, "```"))
	}
	return body
}

func stripJSONCodeFence(content string) string {
	content = strings.TrimSpace(content)
	if !strings.HasPrefix(content, "```") {
		return content
	}
	rest := strings.TrimPrefix(content, "```")
	lineEnd := strings.IndexByte(rest, '\n')
	if lineEnd < 0 {
		return content
	}
	fenceInfo := strings.TrimSpace(rest[:lineEnd])
	if fenceInfo != "" && !strings.EqualFold(fenceInfo, "json") {
		return content
	}
	body := strings.TrimSpace(rest[lineEnd+1:])
	if strings.HasSuffix(body, "```") {
		body = strings.TrimSpace(strings.TrimSuffix(body, "```"))
	}
	return body
}

// extractJSON pulls the first balanced JSON object out of model output, after
// stripping any ``` fences. Models sometimes wrap JSON in a sentence of prose
// ("Here is the result: {...}"); this finds the {...} regardless.
func extractJSON(content string) string {
	content = stripJSONCodeFence(content)
	start := strings.IndexByte(content, '{')
	if start < 0 {
		return content
	}
	depth, inStr, esc := 0, false, false
	for i := start; i < len(content); i++ {
		c := content[i]
		if inStr {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return content[start : i+1]
			}
		}
	}
	return content[start:]
}

// clamp01 bounds a confidence score to [0,1] so a misbehaving model can't push
// it out of range.
func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// normalizeHexColor validates and normalizes a model-supplied colour to
// "#RRGGBB" (expanding "#RGB"), returning "" when it is not a usable hex colour.
func normalizeHexColor(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if !strings.HasPrefix(s, "#") {
		s = "#" + s
	}
	hex := s[1:]
	isHex := func(str string) bool {
		for _, c := range str {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
				return false
			}
		}
		return len(str) > 0
	}
	switch len(hex) {
	case 3:
		if !isHex(hex) {
			return ""
		}
		return "#" + strings.ToUpper(string([]byte{hex[0], hex[0], hex[1], hex[1], hex[2], hex[2]}))
	case 6:
		if !isHex(hex) {
			return ""
		}
		return "#" + strings.ToUpper(hex)
	default:
		return ""
	}
}

// snippet trims a possibly-large provider body to something safe to log/return.
func snippet(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}

// chatCompletion performs one OpenAI-compatible chat-completion call with retry
// on transient failures (network errors, HTTP 429, and 5xx), returning the
// assistant message content. It is the single HTTP path shared by every AI
// helper so retry, headers, response_format and error shaping stay consistent.
func (s *PrintMonitorService) chatCompletion(ctx context.Context, cfg model.PrintMonitorConfig, messages []map[string]any, jsonObject bool, bodyLimit int64) (string, int, error) {
	payload := map[string]any{
		"model":    cfg.Model,
		"messages": messages,
	}
	if jsonObject {
		payload["response_format"] = map[string]string{"type": "json_object"}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", 0, err
	}
	if bodyLimit <= 0 {
		bodyLimit = 1024 * 1024
	}
	url := printMonitorChatCompletionsURL(cfg.OpenRouterURL)

	const maxAttempts = 3
	var lastErr error
	var lastStatus int
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "", lastStatus, ctx.Err()
			case <-time.After(time.Duration(attempt) * 750 * time.Millisecond):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return "", 0, err
		}
		req.Header.Set("Authorization", "Bearer "+cfg.OpenRouterKey)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("HTTP-Referer", "https://github.com/jr551/ankerctl-ng")
		req.Header.Set("X-Title", "ankerctl")

		resp, err := s.httpClient.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return "", lastStatus, fmt.Errorf("AI request timed out — try a shorter / more specific instruction")
			}
			lastErr = err
			continue // network error — retry
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, bodyLimit))
		_ = resp.Body.Close()
		lastStatus = resp.StatusCode
		if readErr != nil && ctx.Err() != nil {
			return "", resp.StatusCode, fmt.Errorf("AI request timed out — try a shorter / more specific instruction")
		}
		raw := strings.TrimSpace(string(data))

		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("AI provider returned HTTP %d: %s", resp.StatusCode, snippet(raw))
			continue // transient — retry
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return "", resp.StatusCode, fmt.Errorf("AI provider returned HTTP %d: %s", resp.StatusCode, snippet(raw))
		}

		var apiResp struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(data, &apiResp); err != nil {
			if ctx.Err() != nil {
				return "", resp.StatusCode, fmt.Errorf("AI request timed out — try a shorter / more specific instruction")
			}
			return "", resp.StatusCode, fmt.Errorf("AI provider returned malformed response: %w", err)
		}
		if len(apiResp.Choices) == 0 {
			return "", resp.StatusCode, fmt.Errorf("AI provider returned no choices")
		}
		return apiResp.Choices[0].Message.Content, resp.StatusCode, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("AI provider request failed")
	}
	return "", lastStatus, lastErr
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

// scheduleGracefulOff handles a failing check: if auto-off is enabled and the
// smart socket is configured, it notifies the user and starts a grace countdown
// (default 2 min), then cuts power when it elapses — UNLESS the user cancels via
// the UI (CancelAutoOff bumps the generation, invalidating this timer). Unlike
// the animal stop, this is never instant: the user always gets the countdown.
func (s *PrintMonitorService) scheduleGracefulOff(ctx context.Context, result PrintMonitorResult) {
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
	s.mu.Lock()
	if s.pendingOffAt != nil { // already counting down — don't restack
		s.mu.Unlock()
		return
	}
	deadline := time.Now().Add(autoOffGraceSec * time.Second)
	s.pendingOffAt = &deadline
	s.pendingOffReason = result.Reason
	s.pendingOffGen++
	gen := s.pendingOffGen
	s.mu.Unlock()

	if s.log != nil {
		s.log.Warn("print may be failing — power-off scheduled unless cancelled",
			"grace_sec", autoOffGraceSec, "reason", result.Reason, "filename", result.Filename)
	}
	s.Notify(map[string]any{"type": "print_monitor.pending_off", "at": deadline, "reason": result.Reason})
	if s.notifier != nil {
		msg := fmt.Sprintf("⚠ Your print may be failing — power will be cut in about %d minute(s) unless you cancel it in the ankerctl-ng UI.", autoOffGraceSec/60)
		if r := strings.TrimSpace(result.Reason); r != "" {
			msg += " AI: " + r
		}
		payload := map[string]any{"filename": result.Filename, "reason": msg}
		var attachments []string
		if result.ContactSheet != "" {
			attachments = []string{result.ContactSheet}
		}
		s.notifier.NotifyPrintFailing(ctx, payload, attachments)
	}
	// Cut power at the deadline unless cancelled. Use a fresh context (the check's
	// ctx is gone by then) and re-read socket creds at fire time is unnecessary —
	// capture them now.
	go func(gen int, sw, base, token string) {
		time.Sleep(autoOffGraceSec * time.Second)
		s.mu.Lock()
		fire := s.pendingOffAt != nil && s.pendingOffGen == gen && s.active
		if fire {
			s.pendingOffAt = nil
			s.pendingOffReason = ""
		}
		s.mu.Unlock()
		if !fire {
			return // cancelled, superseded, or print already ended
		}
		cctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		client := NewHomeAssistantClient(base, token)
		if err := client.CallService(cctx, "switch", "turn_off", sw); err != nil {
			if s.log != nil {
				s.log.Error("failed to cut printer power after failure grace period", "err", err)
			}
		} else if s.log != nil {
			s.log.Warn("printer power cut after failure grace period elapsed")
		}
		s.Notify(map[string]any{"type": "print_monitor.powered_off"})
	}(gen, cfg.SmartSocket.SwitchEntity, cfg.SmartSocket.BaseURL, cfg.SmartSocket.Token)
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
		AnimalDetected:      result.AnimalDetected,
		Animal:              result.Animal,
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
