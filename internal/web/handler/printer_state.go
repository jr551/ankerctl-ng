package handler

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/django1982/ankerctl/internal/model"
	"github.com/django1982/ankerctl/internal/mqtt/protocol"
	"github.com/django1982/ankerctl/internal/service"
)

const (
	defaultPrinterAlertsLimit = 20
	printerAlertBufferSize    = 100
	filamentIssueRunoutValue  = "runout"
)

type printerReportSpec struct {
	Label  string
	GCode  string
	Window time.Duration
	Drain  int
}

type printerReportResult struct {
	Name          string
	Label         string
	GCode         string
	RawOutput     string
	CleanedOutput string
	Chunks        []string
	ChunkCount    int
	Err           error
}

type gcodeResponseChunk struct {
	text   string
	resLen int
}

type printerAlertBuffer struct {
	mu         sync.Mutex
	entries    []map[string]any
	nextID     int
	recentKeys map[string]time.Time
	maxEntries int
}

var (
	ansiEscapePattern  = regexp.MustCompile(`\x1b\[[0-9;]*m`)
	reportCommandRE    = regexp.MustCompile(`(M(?:92|145|201|203|204|205|206|218|290|301|420|425|665|851|907)\b[^\r\n+]*)`)
	printerReportSpecs = map[string]printerReportSpec{
		"settings": {
			Label:  "Stored Settings",
			GCode:  "M503",
			Window: 4 * time.Second,
			Drain:  8,
		},
		"probe_offset": {
			Label:  "Probe Offset",
			GCode:  "M851",
			Window: 2 * time.Second,
			Drain:  2,
		},
		"babystep": {
			Label:  "Babystep / Z-Offset",
			GCode:  "M290 R",
			Window: 2 * time.Second,
			Drain:  2,
		},
	}
	summaryCommandGroups = map[string][]string{
		"leveling": {"M851", "M420", "M290"},
		"motion":   {"M201", "M203", "M204", "M205", "M206"},
		"thermal":  {"M301", "M145"},
		"motors":   {"M907"},
		"tooling":  {"M218"},
	}
	activePrinterAlerts = newPrinterAlertBuffer(printerAlertBufferSize)
)

func newPrinterAlertBuffer(maxEntries int) *printerAlertBuffer {
	if maxEntries < 1 {
		maxEntries = 1
	}
	return &printerAlertBuffer{
		nextID:     1,
		recentKeys: make(map[string]time.Time),
		maxEntries: maxEntries,
	}
}

func (b *printerAlertBuffer) append(printerIndex int, printerName, alertType, title, message, level string, cooldown time.Duration) int {
	message = strings.TrimSpace(message)
	if message == "" {
		return 0
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = message
	}
	level = strings.TrimSpace(level)
	if level == "" {
		level = "warning"
	}

	now := time.Now()
	key := fmt.Sprintf("%d:%s:%s:%s", printerIndex, alertType, title, message)

	b.mu.Lock()
	defer b.mu.Unlock()

	if cooldown > 0 {
		if lastSeen, ok := b.recentKeys[key]; ok && now.Sub(lastSeen) < cooldown {
			return 0
		}
	}
	b.recentKeys[key] = now

	staleBefore := now.Add(-maxDuration(cooldown*4, time.Minute))
	for existingKey, seenAt := range b.recentKeys {
		if seenAt.Before(staleBefore) {
			delete(b.recentKeys, existingKey)
		}
	}

	entry := map[string]any{
		"id":            b.nextID,
		"created_at":    float64(now.UnixNano()) / 1e9,
		"printer_index": printerIndex,
		"printer_name":  printerName,
		"type":          alertType,
		"title":         title,
		"message":       message,
		"level":         level,
	}
	b.nextID++

	if len(b.entries) >= b.maxEntries {
		copy(b.entries, b.entries[1:])
		b.entries[len(b.entries)-1] = entry
		return entry["id"].(int)
	}
	b.entries = append(b.entries, entry)
	return entry["id"].(int)
}

func (b *printerAlertBuffer) snapshot(limit int, afterID *int) map[string]any {
	if limit < 1 {
		limit = 1
	}
	if limit > b.maxEntries {
		limit = b.maxEntries
	}

	b.mu.Lock()
	entries := make([]map[string]any, len(b.entries))
	copy(entries, b.entries)
	b.mu.Unlock()

	firstID := 0
	lastID := 0
	if len(entries) > 0 {
		firstID, _ = intFromAny(entries[0]["id"])
		lastID, _ = intFromAny(entries[len(entries)-1]["id"])
	}

	truncated := false
	selected := entries
	if afterID == nil {
		if len(selected) > limit {
			selected = selected[len(selected)-limit:]
		}
	} else {
		if len(entries) > 0 && *afterID < firstID-1 {
			truncated = true
		}
		selected = selected[:0]
		for _, entry := range entries {
			id, _ := intFromAny(entry["id"])
			if id > *afterID {
				selected = append(selected, entry)
			}
		}
		if len(selected) > limit {
			truncated = true
			selected = selected[len(selected)-limit:]
		}
	}

	return map[string]any{
		"entries":     selected,
		"first_id":    firstID,
		"last_id":     lastID,
		"next_after":  lastID,
		"truncated":   truncated,
		"max_entries": b.maxEntries,
	}
}

// PrinterRuntimeState returns the current structured printer runtime state.
// GET /api/printer/runtime-state
func (h *Handler) PrinterRuntimeState(w http.ResponseWriter, r *http.Request) {
	mqtt, ok := h.mqttQueue()
	if !ok {
		h.writeError(w, http.StatusServiceUnavailable, "Service unavailable")
		return
	}

	cfg, _ := h.loadConfig()
	if cfg == nil {
		cfg = model.NewConfig(nil, nil)
	}
	_, printerIndex, _ := h.activePrinter(cfg)

	snapshot := mqtt.SnapshotState()
	printState, pauseReason, pauseReasonLabel, lastFilename := buildRuntimePrintState(snapshot)

	h.writeJSON(w, http.StatusOK, map[string]any{
		"status":        "ok",
		"print":         printState,
		"temperature":   buildRuntimeTemperature(snapshot),
		"z_offset":      buildRuntimeZOffsetState(mqtt, "cached"),
		"filament":      buildRuntimeFilamentState(snapshot, pauseReason, pauseReasonLabel),
		"debug_logging": false,
		"timelapse":     buildRuntimeTimelapseState(h, cfg, lastFilename),
		"camera":        buildRuntimeCameraState(cfg, printerIndex),
	})
}

// PrinterSettingsSummary returns a concise settings summary matching the Python
// endpoint shape, using live MQTT report queries where possible.
// GET /api/printer/settings-summary
func (h *Handler) PrinterSettingsSummary(w http.ResponseWriter, r *http.Request) {
	mqtt, err := h.borrowMqttQueueSafe()
	if err != nil {
		h.writeError(w, http.StatusServiceUnavailable, "Service unavailable")
		return
	}
	defer h.svc.Return("mqttqueue")

	liveZOffset := buildSummaryZOffsetState(mqtt, r.Context())

	reports := make(map[string]printerReportResult, 3)
	for _, name := range []string{"settings", "probe_offset", "babystep"} {
		reports[name] = readPrinterReport(r.Context(), mqtt, name)
		if r.Context().Err() != nil {
			return
		}
	}

	commands := extractReportCommands(
		reports["settings"].CleanedOutput,
		reports["probe_offset"].CleanedOutput,
		reports["babystep"].CleanedOutput,
	)

	highlights := make([]map[string]any, 0, 4)
	if available, _ := liveZOffset["available"].(bool); available {
		if mm, ok := liveZOffset["mm"].(float64); ok {
			highlights = append(highlights, map[string]any{
				"label":   "Live Z-Offset",
				"command": "MQTT 1021",
				"value":   fmt.Sprintf("%.2f mm", mm),
			})
		}
	}
	if value, ok := commands["M851"]; ok {
		highlights = append(highlights, map[string]any{
			"label":   "Stored Probe Offset",
			"command": "M851",
			"value":   value,
		})
	}
	if value, ok := commands["M420"]; ok {
		highlights = append(highlights, map[string]any{
			"label":   "Bed Leveling",
			"command": "M420",
			"value":   value,
		})
	}
	if value, ok := commands["M301"]; ok {
		highlights = append(highlights, map[string]any{
			"label":   "Hotend PID",
			"command": "M301",
			"value":   value,
		})
	}

	groups := make(map[string]any, len(summaryCommandGroups))
	for name, keys := range summaryCommandGroups {
		groups[name] = buildCommandGroup(commands, keys)
	}

	reportPayload := make(map[string]any, len(reports))
	for name, report := range reports {
		reportPayload[name] = map[string]any{
			"name":      report.Name,
			"label":     report.Label,
			"gcode":     report.GCode,
			"available": report.Err == nil,
			"error":     errorString(report.Err),
		}
	}

	h.writeJSON(w, http.StatusOK, map[string]any{
		"status":        "ok",
		"live_z_offset": liveZOffset,
		"highlights":    highlights,
		"groups":        groups,
		"reports":       reportPayload,
	})
}

// PrinterAlerts returns the rolling printer alert snapshot envelope used by the
// Python API. The Go rewrite currently synthesizes entries from active runtime
// issues and keeps them in an in-process ring buffer with cooldown.
// GET /api/printer/alerts
func (h *Handler) PrinterAlerts(w http.ResponseWriter, r *http.Request) {
	limit := parseClampedInt(r.URL.Query().Get("limit"), defaultPrinterAlertsLimit, 1, printerAlertBufferSize)
	var afterID *int
	if raw := strings.TrimSpace(r.URL.Query().Get("after")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			afterID = &parsed
		} else {
			zero := 0
			afterID = &zero
		}
	}

	cfg, _ := h.loadConfig()
	printerName := "Printer 1"
	printerIndex := 0
	if cfg != nil {
		if printer, idx, ok := h.activePrinter(cfg); ok {
			printerIndex = idx
			if printer != nil && strings.TrimSpace(printer.Name) != "" {
				printerName = printer.Name
			} else {
				printerName = fmt.Sprintf("Printer %d", idx+1)
			}
		}
	}

	if mqtt, ok := h.mqttQueue(); ok {
		snapshot := mqtt.SnapshotState()
		filament := mapFromAny(snapshot["filament"])
		issue := strings.TrimSpace(stringFromAny(filament["issue"]))
		runoutPending, _ := filament["runout_pending"].(bool)
		if issue == filamentIssueRunoutValue || runoutPending {
			activePrinterAlerts.append(
				printerIndex,
				printerName,
				"filament_runout",
				"Filament runout",
				"Filament runout or break detected.",
				"warning",
				45*time.Second,
			)
		}
	}

	h.writeJSON(w, http.StatusOK, activePrinterAlerts.snapshot(limit, afterID))
}

func buildRuntimePrintState(snapshot map[string]any) (map[string]any, string, string, string) {
	rawState, _ := intFromAny(snapshot["state"])
	active, _ := snapshot["is_printing"].(bool)
	lastFilename := strings.TrimSpace(stringFromAny(snapshot["last_filename"]))
	progressValue, progressKnown := intFromAny(snapshot["print_progress"])
	var progress any
	if progressKnown {
		progress = progressValue
	}

	filament := mapFromAny(snapshot["filament"])
	issue := strings.TrimSpace(stringFromAny(filament["issue"]))
	runoutPending, _ := filament["runout_pending"].(bool)

	pauseReason := ""
	pauseReasonLabel := ""
	if rawState == 2 && (issue == filamentIssueRunoutValue || runoutPending) {
		pauseReason = "filament_runout"
		pauseReasonLabel = "Filament runout"
	}

	printState := "idle"
	switch rawState {
	case 1:
		if progressKnown && progressValue == 0 {
			printState = "pre_print"
		} else {
			printState = "printing"
		}
	case 2:
		printState = "paused"
	case 8:
		printState = "preparing"
	default:
		if active {
			printState = "printing"
		}
	}

	return map[string]any{
		"print_state":         printState,
		"active":              printState == "pre_print" || printState == "printing" || printState == "paused",
		"in_pre_print_window": printState == "pre_print",
		"pending_start":       printState == "preparing",
		"state":               rawState,
		"state_label":         mqttStateLabel(rawState),
		"preparing":           printState == "preparing",
		"started_at":          nil,
		"last_progress":       progress,
		"last_filename":       lastFilename,
		"last_task_id":        nil,
		"failure_sent":        false,
		"preview_url":         nil,
		"pause_reason":        emptyToNil(pauseReason),
		"pause_reason_label":  emptyToNil(pauseReasonLabel),
	}, pauseReason, pauseReasonLabel, lastFilename
}

func buildRuntimeTemperature(snapshot map[string]any) map[string]any {
	temperature := mapFromAny(snapshot["temperature"])
	return map[string]any{
		"nozzle":        optionalIntValue(temperature["nozzle"]),
		"nozzle_target": optionalIntValue(temperature["nozzle_target"]),
		"bed":           optionalIntValue(temperature["bed"]),
		"bed_target":    optionalIntValue(temperature["bed_target"]),
	}
}

func buildRuntimeZOffsetState(mqtt *service.MqttQueue, source string) map[string]any {
	state := map[string]any{
		"available":  false,
		"steps":      nil,
		"mm":         nil,
		"updated_at": nil,
		"source":     source,
	}

	steps := mqtt.ZAxisRecoup()
	if steps == nil {
		return state
	}

	state["available"] = true
	state["steps"] = *steps
	state["mm"] = float64(*steps) * 0.01
	if updatedAt := mqtt.LastMessageTime(); !updatedAt.IsZero() {
		state["updated_at"] = float64(updatedAt.UnixNano()) / 1e9
	}
	return state
}

func buildSummaryZOffsetState(mqtt *service.MqttQueue, ctx context.Context) map[string]any {
	source := "cached"
	state := buildRuntimeZOffsetState(mqtt, source)
	available, _ := state["available"].(bool)
	if !available {
		if liveMM, err := mqtt.RefreshZOffset(ctx); err == nil {
			source = "live"
			state = map[string]any{
				"available":  true,
				"steps":      int(math.Round(liveMM / 0.01)),
				"mm":         liveMM,
				"updated_at": float64(time.Now().UnixNano()) / 1e9,
				"source":     source,
			}
		}
	}

	if mm, ok := state["mm"].(float64); ok {
		state["display"] = fmt.Sprintf("%.2f mm", mm)
	} else {
		state["display"] = "unknown"
	}
	return state
}

func buildRuntimeFilamentState(snapshot map[string]any, pauseReason, pauseReasonLabel string) map[string]any {
	filament := mapFromAny(snapshot["filament"])
	issue := strings.TrimSpace(stringFromAny(filament["issue"]))
	if issue == "" {
		issue = ""
	}
	issueCode := strings.TrimSpace(stringFromAny(filament["issue_code"]))
	runoutPending, _ := filament["runout_pending"].(bool)

	state := "unknown"
	label := "Unknown"
	var loaded any
	var detail any
	if issue == filamentIssueRunoutValue || runoutPending {
		state = "not_loaded"
		label = "Not Loaded"
		if pauseReason == "filament_runout" {
			detail = "Paused: Filament runout. Reload filament to continue."
		} else {
			detail = "Filament runout or break detected."
		}
	}

	return map[string]any{
		"state":              state,
		"label":              label,
		"loaded":             loaded,
		"issue":              emptyToNil(issue),
		"issue_label":        issueLabel(issue),
		"issue_code":         emptyToNil(issueCode),
		"detail":             detail,
		"pause_reason":       emptyToNil(pauseReason),
		"pause_reason_label": emptyToNil(pauseReasonLabel),
		"raw_value":          nil,
		"progress":           nil,
		"step_len":           nil,
	}
}

func buildRuntimeTimelapseState(h *Handler, cfg *model.Config, lastFilename string) map[string]any {
	enabled := false
	if cfg != nil {
		enabled = cfg.Timelapse.Enabled
	}

	state := map[string]any{
		"enabled":            enabled,
		"capturing":          false,
		"active_capture":     false,
		"paused":             false,
		"pause_reason":       nil,
		"manual_paused":      false,
		"recovering":         false,
		"recovery_reason":    nil,
		"resume_available":   false,
		"resume_filename":    nil,
		"resume_frame_count": nil,
		"detail":             nil,
		"prompt_start":       false,
		"prompt_filename":    emptyToNil(lastFilename),
	}

	tl, ok := h.timelapse()
	if !ok {
		return state
	}

	status := tl.Status()
	switch status.State {
	case "capturing":
		state["capturing"] = true
		state["active_capture"] = true
		if status.Filename != "" {
			state["prompt_filename"] = status.Filename
		}
	case "paused":
		state["paused"] = true
		state["active_capture"] = true
		state["pause_reason"] = "manual"
		state["manual_paused"] = true
		state["resume_available"] = true
		state["resume_filename"] = status.Filename
		state["resume_frame_count"] = status.Frames
		state["detail"] = "Paused manually."
		state["prompt_filename"] = status.Filename
	}

	return state
}

func buildRuntimeCameraState(cfg *model.Config, printerIndex int) map[string]any {
	if cfg == nil {
		cfg = model.NewConfig(nil, nil)
	}
	resolved := resolveCameraSettings(cfg, printerIndex)
	return map[string]any{
		"source":                  resolved.Source,
		"effective_source":        resolved.EffectiveSource,
		"printer_supported":       resolved.PrinterSupported,
		"feature_available":       resolved.FeatureAvailable,
		"detail":                  resolved.Detail,
		"external_name":           emptyToNil(resolved.External.Name),
		"external_configured":     resolved.External.Configured,
		"external_refresh_sec":    resolved.External.RefreshSec,
		"external_stream_preview": strings.TrimSpace(resolved.External.StreamURL) != "",
	}
}

func readPrinterReport(ctx context.Context, mqtt *service.MqttQueue, name string) printerReportResult {
	spec, ok := printerReportSpecs[name]
	if !ok {
		return printerReportResult{
			Name:  name,
			Label: name,
			Err:   fmt.Errorf("unknown report %q", name),
		}
	}

	rawOutput, cleanedOutput, chunks, err := collectPrinterGCodeOutput(ctx, mqtt, spec.GCode, spec.Window, spec.Drain)
	return printerReportResult{
		Name:          name,
		Label:         spec.Label,
		GCode:         spec.GCode,
		RawOutput:     rawOutput,
		CleanedOutput: cleanedOutput,
		Chunks:        chunks,
		ChunkCount:    len(chunks),
		Err:           err,
	}
}

func collectPrinterGCodeOutput(ctx context.Context, mqtt *service.MqttQueue, gcode string, window time.Duration, drain int) (string, string, []string, error) {
	chunksCh := make(chan gcodeResponseChunk, 64)
	unsub := mqtt.Tap(func(v any) {
		msg, ok := v.(map[string]any)
		if !ok {
			return
		}
		commandType, ok := intFromAny(msg["commandType"])
		if !ok || commandType != int(protocol.MqttCmdGcodeCommand) {
			return
		}
		resData := strings.TrimSpace(stringFromAny(msg["resData"]))
		if resData == "" {
			resData = strings.TrimSpace(stringFromAny(msg["cmdResult"]))
		}
		if resData == "" {
			return
		}
		resLen, _ := intFromAny(msg["resLen"])
		select {
		case chunksCh <- gcodeResponseChunk{text: resData, resLen: resLen}:
		default:
		}
	})
	defer unsub()

	if err := mqtt.SendGCode(ctx, gcode); err != nil {
		return "", "", nil, fmt.Errorf("send %s: %w", gcode, err)
	}

	collected, err := collectGCodeChunks(ctx, chunksCh, window)
	if err != nil {
		return "", "", nil, err
	}
	if len(collected) == 0 {
		return "", "", nil, fmt.Errorf("no response from printer for %s", gcode)
	}

	for probeNum := 0; probeNum < drain; probeNum++ {
		select {
		case <-ctx.Done():
			return "", "", nil, ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}

		if err := mqtt.SendGCode(ctx, "M114"); err != nil {
			break
		}

		probeChunks, err := collectGCodeChunks(ctx, chunksCh, time.Second)
		if err != nil {
			return "", "", nil, err
		}
		if len(probeChunks) == 0 {
			break
		}

		stopDraining := false
		collected = append(collected, probeChunks...)
		for _, probeChunk := range probeChunks {
			if probeNum > 0 && probeChunk.resLen > 0 && probeChunk.resLen <= 64 && !strings.Contains(probeChunk.text, "echo:") && !strings.Contains(probeChunk.text, "z1:") {
				stopDraining = true
			}
		}
		if stopDraining {
			break
		}
	}

	rawChunks := make([]string, 0, len(collected))
	var rawBuilder strings.Builder
	for _, chunk := range collected {
		rawChunks = append(rawChunks, chunk.text)
		rawBuilder.WriteString(chunk.text)
	}

	rawOutput := rawBuilder.String()
	return rawOutput, cleanPrinterReportOutput(rawOutput), rawChunks, nil
}

func collectGCodeChunks(ctx context.Context, chunksCh <-chan gcodeResponseChunk, window time.Duration) ([]gcodeResponseChunk, error) {
	if window <= 0 {
		return nil, nil
	}

	timer := time.NewTimer(window)
	defer timer.Stop()

	var chunks []gcodeResponseChunk
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case chunk := <-chunksCh:
			chunks = append(chunks, chunk)
		case <-timer.C:
			return chunks, nil
		}
	}
}

func cleanPrinterReportOutput(rawOutput string) string {
	if rawOutput == "" {
		return ""
	}

	text := ansiEscapePattern.ReplaceAllString(rawOutput, "")
	text = strings.ReplaceAll(text, "\r", "\n")

	lines := make([]string, 0)
	for _, line := range strings.Split(text, "\n") {
		stripped := strings.TrimSpace(line)
		switch {
		case stripped == "":
			continue
		case stripped == "ok":
			continue
		case strings.HasPrefix(stripped, "+ringbuf"):
			continue
		case stripped == "+rin":
			continue
		case strings.HasPrefix(stripped, "Unknown com"):
			continue
		case strings.HasPrefix(stripped, "X:") && strings.Contains(stripped, "Count"):
			continue
		}
		lines = append(lines, stripped)
	}
	return strings.Join(lines, "\n")
}

func extractReportCommands(texts ...string) map[string]string {
	commands := make(map[string]string)
	for _, text := range texts {
		if text == "" {
			continue
		}
		for _, match := range reportCommandRE.FindAllString(text, -1) {
			command := strings.TrimSpace(match)
			fields := strings.Fields(command)
			if len(fields) == 0 {
				continue
			}
			prefix := fields[0]
			if _, exists := commands[prefix]; !exists {
				commands[prefix] = command
			}
		}
	}
	return commands
}

func buildCommandGroup(commands map[string]string, keys []string) []map[string]string {
	group := make([]map[string]string, 0, len(keys))
	for _, key := range keys {
		if value, ok := commands[key]; ok {
			group = append(group, map[string]string{
				"command": key,
				"value":   value,
			})
		}
	}
	return group
}

func mqttStateLabel(state int) string {
	switch state {
	case 0:
		return "idle"
	case 1:
		return "printing"
	case 2:
		return "paused"
	case 3:
		return "resume_ack"
	case 8:
		return "preparing_or_aborted"
	default:
		return "unknown"
	}
}

func issueLabel(issue string) any {
	switch issue {
	case filamentIssueRunoutValue:
		return "Filament runout"
	default:
		return nil
	}
}

func errorString(err error) any {
	if err == nil {
		return nil
	}
	return err.Error()
}

func emptyToNil(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}

func optionalIntValue(v any) any {
	switch value := v.(type) {
	case nil:
		return nil
	case *int:
		if value == nil {
			return nil
		}
		return *value
	case int:
		return value
	case int8:
		return int(value)
	case int16:
		return int(value)
	case int32:
		return int(value)
	case int64:
		return int(value)
	case float64:
		return int(value)
	default:
		return nil
	}
}

func intFromAny(v any) (int, bool) {
	switch value := v.(type) {
	case int:
		return value, true
	case int8:
		return int(value), true
	case int16:
		return int(value), true
	case int32:
		return int(value), true
	case int64:
		return int(value), true
	case float32:
		return int(value), true
	case float64:
		return int(value), true
	case *int:
		if value == nil {
			return 0, false
		}
		return *value, true
	default:
		return 0, false
	}
}

func mapFromAny(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

func (h *Handler) borrowMqttQueueSafe() (*service.MqttQueue, error) {
	if h == nil || h.svc == nil {
		return nil, fmt.Errorf("mqttqueue unavailable")
	}
	svc, err := h.svc.Borrow("mqttqueue")
	if err != nil {
		return nil, err
	}
	mqtt, ok := svc.(*service.MqttQueue)
	if !ok {
		h.svc.Return("mqttqueue")
		return nil, fmt.Errorf("service type mismatch")
	}
	return mqtt, nil
}
