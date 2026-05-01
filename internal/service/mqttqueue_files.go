package service

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/django1982/ankerctl/internal/mqtt/protocol"
)

const (
	storedFileSourceOnboard = "onboard"
	storedFileSourceUSB     = "usb"

	storedFileSourceValueUSB     = 0
	storedFileSourceValueOnboard = 1

	storedFileListPageSize = 47

	storedFileSelectionTimeout        = 2 * time.Second
	storedFilePreviewTimeout          = 2500 * time.Millisecond
	storedFileStartConfirmTimeout     = 12 * time.Second
	storedFileOnboardStartTimeout     = 20 * time.Second
	storedFileSelectionRequestDelay   = 120 * time.Millisecond
	defaultStoredFileListProbeTimeout = 5 * time.Second
	defaultStoredFileCollectWindow    = time.Second
)

const (
	storedFilePathOnboardRoot = "/usr/data/local/model/"
	storedFilePathUSBRoot     = "/tmp/udisk/"
)

// StoredFile describes one GCode file listed from printer storage.
type StoredFile struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Timestamp *int64 `json:"timestamp"`
	Source    string `json:"source"`
}

// StoredFileListResult contains the parsed ct=1009 list probe result.
type StoredFileListResult struct {
	ReplyCount  int
	SourceValue int
	Files       []StoredFile
}

// InferStoredFileSourceFromPath returns the printer storage backing a path.
func InferStoredFileSourceFromPath(path string) string {
	switch {
	case strings.HasPrefix(path, storedFilePathUSBRoot):
		return storedFileSourceUSB
	case strings.HasPrefix(path, storedFilePathOnboardRoot):
		return storedFileSourceOnboard
	default:
		return ""
	}
}

func isStoredFileSourcePath(path string) bool {
	return InferStoredFileSourceFromPath(path) != ""
}

func isPreprintPreviewPath(path string) bool {
	path = strings.TrimSpace(path)
	return strings.HasPrefix(path, "/tmp/") && !strings.HasPrefix(path, storedFilePathUSBRoot)
}

func resolveStoredFileSourceValue(source string, value *int) (int, error) {
	if value != nil {
		return *value, nil
	}

	switch strings.ToLower(strings.TrimSpace(source)) {
	case "", storedFileSourceOnboard:
		return storedFileSourceValueOnboard, nil
	case storedFileSourceUSB:
		return storedFileSourceValueUSB, nil
	default:
		return 0, fmt.Errorf("unsupported file-list source: %q", source)
	}
}

func extractPreviewURL(payload map[string]any) string {
	for _, key := range []string{
		"preview_url",
		"previewUrl",
		"previewImageUrl",
		"preview_image_url",
		"image_url",
		"imageUrl",
		"img",
		"img_url",
		"imgUrl",
		"url",
	} {
		value, _ := payload[key].(string)
		value = strings.TrimSpace(value)
		if strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") {
			return value
		}
	}

	for key, raw := range payload {
		value, ok := raw.(string)
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if strings.Contains(strings.ToLower(key), "preview") && (strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://")) {
			return value
		}
	}

	return ""
}

func asStoredFileTimestamp(value any) *int64 {
	switch raw := value.(type) {
	case string:
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return nil
		}
		if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
			v := parsed.Unix()
			return &v
		}
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil {
			return &parsed
		}
	}

	if n, ok := asInt(value); ok {
		v := int64(n)
		return &v
	}
	return nil
}

func parseStoredFileListReplies(replies []map[string]any, requestedSource string) []StoredFile {
	files := make([]StoredFile, 0)
	for _, reply := range replies {
		rawList, ok := reply["fileLists"].([]any)
		if !ok {
			continue
		}
		for _, rawEntry := range rawList {
			entry, ok := rawEntry.(map[string]any)
			if !ok {
				continue
			}
			path, _ := entry["path"].(string)
			source := InferStoredFileSourceFromPath(path)
			if source == "" {
				source = requestedSource
			}
			if requestedSource != "" && source != "" && source != requestedSource {
				continue
			}
			name, _ := entry["name"].(string)
			files = append(files, StoredFile{
				Name:      name,
				Path:      path,
				Timestamp: asStoredFileTimestamp(entry["timestamp"]),
				Source:    source,
			})
		}
	}
	return files
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (q *MqttQueue) command(ctx context.Context, msg map[string]any) error {
	q.mu.Lock()
	c := q.client
	q.mu.Unlock()
	if c == nil {
		return fmt.Errorf("mqttqueue: mqtt client not connected")
	}
	if err := c.Command(ctx, msg); err != nil {
		return fmt.Errorf("mqttqueue: send command: %w", err)
	}
	return nil
}

func buildStoredFileListSelectionPayload(filePath, userID string) map[string]any {
	sourceValue, _ := resolveStoredFileSourceValue(InferStoredFileSourceFromPath(filePath), nil)
	payload := map[string]any{
		"commandType": int(protocol.MqttCmdFileListRequest),
		"value":       sourceValue,
		"isFirst":     1,
		"index":       1,
		"num":         storedFileListPageSize,
	}
	if strings.TrimSpace(userID) != "" {
		payload["userId"] = strings.TrimSpace(userID)
	}
	return payload
}

func buildStoredFileRequestPayload(filePath, userID string) map[string]any {
	payload := map[string]any{
		"commandType": int(protocol.MqttCmdGcodeFileRequest),
		"filePath":    filePath,
		"type":        0,
	}
	if strings.TrimSpace(userID) != "" {
		payload["userId"] = strings.TrimSpace(userID)
	}
	return payload
}

func deriveStoredFileControlDisplayName(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if localPart, _, ok := strings.Cut(value, "@"); ok && strings.TrimSpace(localPart) != "" {
		return strings.TrimSpace(localPart)
	}
	return value
}

func buildStoredFileStartControlPayload(filePath, userName, userID string) map[string]any {
	payload := map[string]any{
		"commandType": int(protocol.MqttCmdPrintControl),
		"value":       1,
		"printMode":   1,
		"filePath":    filePath,
	}
	if displayName := deriveStoredFileControlDisplayName(userName); displayName != "" {
		payload["userName"] = displayName
	}
	if strings.TrimSpace(userID) != "" {
		payload["userId"] = strings.TrimSpace(userID)
	}
	return payload
}

func (q *MqttQueue) cacheStoredFilePreview(filePath, previewURL string) {
	filePath = strings.TrimSpace(filePath)
	previewURL = strings.TrimSpace(previewURL)
	if filePath == "" || previewURL == "" {
		return
	}
	q.mu.Lock()
	if q.storedFilePreviewCache == nil {
		q.storedFilePreviewCache = make(map[string]string)
	}
	q.storedFilePreviewCache[filePath] = previewURL
	q.mu.Unlock()
}

// GetCachedStoredFilePreviewURL returns the last cached preview URL for a stored file.
func (q *MqttQueue) GetCachedStoredFilePreviewURL(filePath string) string {
	filePath = strings.TrimSpace(filePath)
	if filePath == "" {
		return ""
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.storedFilePreviewCache[filePath]
}

// HasPendingStoredFileStart reports whether a stored-file start is still in-flight.
func (q *MqttQueue) HasPendingStoredFileStart() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return strings.TrimSpace(q.pendingStoredFilePath) != ""
}

func (q *MqttQueue) clearPendingStoredFileStart(filePath string) {
	q.mu.Lock()
	if filePath == "" || q.pendingStoredFilePath == filePath {
		q.pendingStoredFilePath = ""
	}
	q.mu.Unlock()
}

func storedFileStartConfirmedByPayload(payload map[string]any, targetPath string) bool {
	ct, ok := asInt(payload["commandType"])
	if !ok {
		return false
	}

	targetName := filepath.Base(strings.TrimSpace(targetPath))
	switch ct {
	case int(protocol.MqttCmdEventNotify):
		state, ok := asInt(payload["value"])
		return ok && (state == mqttStatePrinting || state == mqttStatePaused)
	case int(protocol.MqttCmdPrintSchedule):
		name := extractFilename(payload)
		return name != "" && filepath.Base(name) == targetName
	case int(protocol.MqttCmdModelDLProcess):
		filePath, _ := payload["filePath"].(string)
		return filePath != "" && isPreprintPreviewPath(filePath) && filepath.Base(filePath) == targetName
	default:
		return false
	}
}

func storedFileStartTimeout(filePath string) time.Duration {
	if strings.HasPrefix(strings.TrimSpace(filePath), storedFilePathOnboardRoot) {
		return storedFileOnboardStartTimeout
	}
	return storedFileStartConfirmTimeout
}

// ProbeStoredFiles collects ct=1009 responses and parses them into file entries.
func (q *MqttQueue) ProbeStoredFiles(ctx context.Context, source string, sourceValue *int, timeout, collectWindow time.Duration) (StoredFileListResult, error) {
	if timeout <= 0 {
		timeout = defaultStoredFileListProbeTimeout
	}
	if collectWindow <= 0 {
		collectWindow = defaultStoredFileCollectWindow
	}

	resolvedValue, err := resolveStoredFileSourceValue(source, sourceValue)
	if err != nil {
		return StoredFileListResult{}, err
	}

	requestedSource := storedFileSourceUSB
	if resolvedValue == storedFileSourceValueOnboard {
		requestedSource = storedFileSourceOnboard
	}

	replyCh := make(chan map[string]any, 16)
	unsub := q.Tap(func(v any) {
		payload, ok := v.(map[string]any)
		if !ok {
			return
		}
		ct, ok := asInt(payload["commandType"])
		if !ok || ct != int(protocol.MqttCmdFileListRequest) {
			return
		}
		select {
		case replyCh <- cloneMap(payload):
		default:
		}
	})
	defer unsub()

	if err := q.command(ctx, map[string]any{
		"commandType": int(protocol.MqttCmdFileListRequest),
		"value":       resolvedValue,
	}); err != nil {
		return StoredFileListResult{}, err
	}

	replies := make([]map[string]any, 0, 4)

	firstTimer := time.NewTimer(timeout)
	defer firstTimer.Stop()

waitFirst:
	for len(replies) == 0 {
		select {
		case <-ctx.Done():
			return StoredFileListResult{}, ctx.Err()
		case <-firstTimer.C:
			break waitFirst
		case reply := <-replyCh:
			replies = append(replies, reply)
		}
	}

	if len(replies) > 0 {
		windowTimer := time.NewTimer(collectWindow)
		defer windowTimer.Stop()
		for {
			select {
			case <-ctx.Done():
				return StoredFileListResult{}, ctx.Err()
			case <-windowTimer.C:
				goto done
			case reply := <-replyCh:
				replies = append(replies, reply)
			}
		}
	}

done:
	return StoredFileListResult{
		ReplyCount:  len(replies),
		SourceValue: resolvedValue,
		Files:       parseStoredFileListReplies(replies, requestedSource),
	}, nil
}

// GetStoredFilePreviewURL returns the preview URL for a stored GCode file.
func (q *MqttQueue) GetStoredFilePreviewURL(ctx context.Context, filePath, userID string, allowProbe bool) (string, error) {
	filePath = strings.TrimSpace(filePath)
	if filePath == "" {
		return "", fmt.Errorf("stored file path is required")
	}

	cachedURL := q.GetCachedStoredFilePreviewURL(filePath)
	if cachedURL != "" || !allowProbe {
		return cachedURL, nil
	}

	previewCh := make(chan string, 1)
	unsub := q.Tap(func(v any) {
		payload, ok := v.(map[string]any)
		if !ok {
			return
		}
		ct, ok := asInt(payload["commandType"])
		if !ok || ct != int(protocol.MqttCmdModelDLProcess) {
			return
		}
		replyPath, _ := payload["filePath"].(string)
		if strings.TrimSpace(replyPath) != filePath {
			return
		}
		previewURL := extractPreviewURL(payload)
		if previewURL == "" {
			return
		}
		q.cacheStoredFilePreview(filePath, previewURL)
		select {
		case previewCh <- previewURL:
		default:
		}
	})
	defer unsub()

	if err := q.command(ctx, buildStoredFileListSelectionPayload(filePath, userID)); err != nil {
		return "", err
	}
	if err := sleepContext(ctx, storedFileSelectionRequestDelay); err != nil {
		return "", err
	}
	if err := q.command(ctx, buildStoredFileRequestPayload(filePath, userID)); err != nil {
		return "", err
	}

	timer := time.NewTimer(storedFilePreviewTimeout)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-timer.C:
		return q.GetCachedStoredFilePreviewURL(filePath), nil
	case previewURL := <-previewCh:
		return previewURL, nil
	}
}

// StartStoredFile selects a stored printer file and starts a print job from it.
func (q *MqttQueue) StartStoredFile(ctx context.Context, filePath, userName, userID string) (bool, error) {
	filePath = strings.TrimSpace(filePath)
	if filePath == "" {
		return false, fmt.Errorf("stored file path is required")
	}

	selectionCh := make(chan struct{}, 1)
	selectionUnsub := q.Tap(func(v any) {
		payload, ok := v.(map[string]any)
		if !ok {
			return
		}
		ct, ok := asInt(payload["commandType"])
		if !ok || ct != int(protocol.MqttCmdModelDLProcess) {
			return
		}
		replyPath, _ := payload["filePath"].(string)
		replyPath = strings.TrimSpace(replyPath)
		if replyPath == "" || replyPath != filePath {
			return
		}
		if previewURL := extractPreviewURL(payload); previewURL != "" {
			q.cacheStoredFilePreview(filePath, previewURL)
		}
		select {
		case selectionCh <- struct{}{}:
		default:
		}
	})

	if err := q.command(ctx, buildStoredFileListSelectionPayload(filePath, userID)); err != nil {
		selectionUnsub()
		return false, err
	}
	if err := sleepContext(ctx, storedFileSelectionRequestDelay); err != nil {
		selectionUnsub()
		return false, err
	}
	if err := q.command(ctx, buildStoredFileRequestPayload(filePath, userID)); err != nil {
		selectionUnsub()
		return false, err
	}

	selectionTimer := time.NewTimer(storedFileSelectionTimeout)
	select {
	case <-ctx.Done():
		selectionTimer.Stop()
		selectionUnsub()
		return false, ctx.Err()
	case <-selectionTimer.C:
	case <-selectionCh:
		if !selectionTimer.Stop() {
			<-selectionTimer.C
		}
	}
	selectionUnsub()

	q.mu.Lock()
	q.pendingStoredFilePath = filePath
	q.lastFilename = filepath.Base(filePath)
	q.mu.Unlock()

	startCh := make(chan struct{}, 1)
	startUnsub := q.Tap(func(v any) {
		payload, ok := v.(map[string]any)
		if !ok {
			return
		}
		if storedFileStartConfirmedByPayload(payload, filePath) {
			select {
			case startCh <- struct{}{}:
			default:
			}
		}
	})
	defer startUnsub()

	if q.IsPrinting() {
		return true, nil
	}

	if err := q.command(ctx, buildStoredFileStartControlPayload(filePath, userName, userID)); err != nil {
		q.clearPendingStoredFileStart(filePath)
		return false, err
	}

	startTimer := time.NewTimer(storedFileStartTimeout(filePath))
	defer startTimer.Stop()

	for {
		if q.IsPrinting() {
			return true, nil
		}

		select {
		case <-ctx.Done():
			q.clearPendingStoredFileStart(filePath)
			return false, ctx.Err()
		case <-startTimer.C:
			confirmed := q.IsPrinting()
			if !confirmed {
				q.clearPendingStoredFileStart(filePath)
			}
			return confirmed, nil
		case <-startCh:
			return true, nil
		}
	}
}
