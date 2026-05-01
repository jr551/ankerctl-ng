package service

import (
	"context"
	"testing"
	"time"

	"github.com/django1982/ankerctl/internal/mqtt/protocol"
)

func waitForMQTTCommandCount(t *testing.T, client *fakeMQTTClient, want int) []map[string]any {
	t.Helper()

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		client.mu.Lock()
		got := append([]map[string]any(nil), client.commands...)
		client.mu.Unlock()
		if len(got) >= want {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}

	client.mu.Lock()
	defer client.mu.Unlock()
	t.Fatalf("commands = %d, want at least %d", len(client.commands), want)
	return nil
}

func TestMqttQueueProbeStoredFiles(t *testing.T) {
	client := &fakeMQTTClient{}
	q := &MqttQueue{
		BaseWorker:             NewBaseWorker("mqttqueue"),
		client:                 client,
		storedFilePreviewCache: make(map[string]string),
	}
	q.BindHooks(q)

	go func() {
		waitForMQTTCommandCount(t, client, 1)
		q.handlePayload(map[string]any{
			"commandType": int(protocol.MqttCmdFileListRequest),
			"fileLists": []any{
				map[string]any{
					"name":      "benchy.gcode",
					"path":      "/usr/data/local/model/benchy.gcode",
					"timestamp": float64(1710000000),
				},
				map[string]any{
					"name":      "usb.gcode",
					"path":      "/tmp/udisk/usb.gcode",
					"timestamp": "1710001234",
				},
			},
		})
	}()

	result, err := q.ProbeStoredFiles(context.Background(), "onboard", nil, 200*time.Millisecond, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("ProbeStoredFiles: %v", err)
	}
	if result.SourceValue != 1 {
		t.Fatalf("SourceValue = %d, want 1", result.SourceValue)
	}
	if result.ReplyCount != 1 {
		t.Fatalf("ReplyCount = %d, want 1", result.ReplyCount)
	}
	if len(result.Files) != 1 {
		t.Fatalf("files len = %d, want 1", len(result.Files))
	}
	if result.Files[0].Name != "benchy.gcode" {
		t.Fatalf("name = %q, want benchy.gcode", result.Files[0].Name)
	}
	if result.Files[0].Path != "/usr/data/local/model/benchy.gcode" {
		t.Fatalf("path = %q, want onboard path", result.Files[0].Path)
	}
	if result.Files[0].Source != "onboard" {
		t.Fatalf("source = %q, want onboard", result.Files[0].Source)
	}
	if result.Files[0].Timestamp == nil || *result.Files[0].Timestamp != 1710000000 {
		t.Fatalf("timestamp = %v, want 1710000000", result.Files[0].Timestamp)
	}

	commands := waitForMQTTCommandCount(t, client, 1)
	if got := commands[0]["commandType"]; got != int(protocol.MqttCmdFileListRequest) {
		t.Fatalf("commandType = %v, want %d", got, int(protocol.MqttCmdFileListRequest))
	}
	if got := commands[0]["value"]; got != 1 {
		t.Fatalf("value = %v, want 1", got)
	}
}

func TestMqttQueueGetStoredFilePreviewURL(t *testing.T) {
	client := &fakeMQTTClient{}
	q := &MqttQueue{
		BaseWorker:             NewBaseWorker("mqttqueue"),
		client:                 client,
		storedFilePreviewCache: make(map[string]string),
	}
	q.BindHooks(q)

	const (
		filePath   = "/usr/data/local/model/benchy.gcode"
		previewURL = "http://printer.local/preview.png"
	)

	go func() {
		waitForMQTTCommandCount(t, client, 2)
		q.handlePayload(map[string]any{
			"commandType": int(protocol.MqttCmdModelDLProcess),
			"filePath":    filePath,
			"previewUrl":  previewURL,
		})
	}()

	got, err := q.GetStoredFilePreviewURL(context.Background(), filePath, "user-1", true)
	if err != nil {
		t.Fatalf("GetStoredFilePreviewURL: %v", err)
	}
	if got != previewURL {
		t.Fatalf("preview URL = %q, want %q", got, previewURL)
	}
	if cached := q.GetCachedStoredFilePreviewURL(filePath); cached != previewURL {
		t.Fatalf("cached preview URL = %q, want %q", cached, previewURL)
	}

	commands := waitForMQTTCommandCount(t, client, 2)
	if got := commands[0]["commandType"]; got != int(protocol.MqttCmdFileListRequest) {
		t.Fatalf("first commandType = %v, want %d", got, int(protocol.MqttCmdFileListRequest))
	}
	if got := commands[0]["userId"]; got != "user-1" {
		t.Fatalf("first userId = %v, want user-1", got)
	}
	if got := commands[0]["isFirst"]; got != 1 {
		t.Fatalf("first isFirst = %v, want 1", got)
	}
	if got := commands[1]["commandType"]; got != int(protocol.MqttCmdGcodeFileRequest) {
		t.Fatalf("second commandType = %v, want %d", got, int(protocol.MqttCmdGcodeFileRequest))
	}
	if got := commands[1]["filePath"]; got != filePath {
		t.Fatalf("second filePath = %v, want %q", got, filePath)
	}
}

func TestMqttQueueStartStoredFile(t *testing.T) {
	client := &fakeMQTTClient{}
	q := &MqttQueue{
		BaseWorker:             NewBaseWorker("mqttqueue"),
		client:                 client,
		storedFilePreviewCache: make(map[string]string),
	}
	q.BindHooks(q)

	const (
		filePath   = "/usr/data/local/model/benchy.gcode"
		previewURL = "http://printer.local/preview.png"
	)

	go func() {
		waitForMQTTCommandCount(t, client, 2)
		q.handlePayload(map[string]any{
			"commandType": int(protocol.MqttCmdModelDLProcess),
			"filePath":    filePath,
			"previewUrl":  previewURL,
		})
		waitForMQTTCommandCount(t, client, 3)
		q.handlePayload(map[string]any{
			"commandType": int(protocol.MqttCmdEventNotify),
			"value":       1,
		})
	}()

	started, err := q.StartStoredFile(context.Background(), filePath, "user@example.com", "user-1")
	if err != nil {
		t.Fatalf("StartStoredFile: %v", err)
	}
	if !started {
		t.Fatal("StartStoredFile returned false, want true")
	}

	commands := waitForMQTTCommandCount(t, client, 3)
	if got := commands[2]["commandType"]; got != int(protocol.MqttCmdPrintControl) {
		t.Fatalf("start commandType = %v, want %d", got, int(protocol.MqttCmdPrintControl))
	}
	if got := commands[2]["value"]; got != 1 {
		t.Fatalf("start value = %v, want 1", got)
	}
	if got := commands[2]["printMode"]; got != 1 {
		t.Fatalf("start printMode = %v, want 1", got)
	}
	if got := commands[2]["filePath"]; got != filePath {
		t.Fatalf("start filePath = %v, want %q", got, filePath)
	}
	if got := commands[2]["userName"]; got != "user" {
		t.Fatalf("start userName = %v, want user", got)
	}
	if got := commands[2]["userId"]; got != "user-1" {
		t.Fatalf("start userId = %v, want user-1", got)
	}
}
