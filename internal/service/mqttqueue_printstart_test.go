package service

import (
	"context"
	"log/slog"
	"testing"

	"github.com/django1982/ankerctl/internal/db"
	"github.com/django1982/ankerctl/internal/mqtt/protocol"
)

// TestMqttQueueMarkPrintStartedOnStartSequence proves that a print-start MQTT
// sequence (printing state followed by the model-download event) stamps
// print_started_at on the history row, distinguishing "printing" from "queued".
func TestMqttQueueMarkPrintStartedOnStartSequence(t *testing.T) {
	historyDB, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open history db: %v", err)
	}
	defer historyDB.Close()

	q := &MqttQueue{
		BaseWorker:         NewBaseWorker("mqttqueue"),
		history:            historyDB,
		currentPrinterStat: -1,
		log:                slog.Default(),
	}
	q.BindHooks(q)

	q.handlePayload(map[string]any{
		"commandType": int(protocol.MqttCmdEventNotify),
		"value":       mqttStatePrinting,
	})
	q.handlePayload(map[string]any{
		"commandType": int(protocol.MqttCmdModelDLProcess),
		"filePath":    "/tmp/benchy.gcode",
	})

	rows, err := historyDB.GetHistory(10, 0)
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("history rows = %d, want 1", len(rows))
	}
	if rows[0].Filename != "benchy.gcode" {
		t.Fatalf("history filename = %q, want benchy.gcode", rows[0].Filename)
	}
	if rows[0].PrintStartedAt == nil {
		t.Fatal("print_started_at = nil, want set after print-start sequence")
	}
}

// TestMqttQueuePendingStoredFileStartAccessors proves the pending stored-file
// start is observable after StartStoredFile and can be cleared exactly once.
func TestMqttQueuePendingStoredFileStartAccessors(t *testing.T) {
	client := &fakeMQTTClient{}
	q := &MqttQueue{
		BaseWorker:             NewBaseWorker("mqttqueue"),
		client:                 client,
		storedFilePreviewCache: make(map[string]string),
		log:                    slog.Default(),
	}
	q.BindHooks(q)

	const filePath = "/tmp/benchy.gcode"

	go func() {
		// First model-download event satisfies the selection wait.
		waitForMQTTCommandCount(t, client, 2)
		q.handlePayload(map[string]any{
			"commandType": int(protocol.MqttCmdModelDLProcess),
			"filePath":    filePath,
		})
		// Second one satisfies the start confirmation without clearing the
		// pending start (unlike a printing-state event).
		waitForMQTTCommandCount(t, client, 3)
		q.handlePayload(map[string]any{
			"commandType": int(protocol.MqttCmdModelDLProcess),
			"filePath":    filePath,
		})
	}()

	started, err := q.StartStoredFile(context.Background(), filePath, "user@example.com", "user-1")
	if err != nil {
		t.Fatalf("StartStoredFile: %v", err)
	}
	if !started {
		t.Fatal("StartStoredFile returned false, want true")
	}

	path, since, ok := q.PendingStoredFileStart()
	if !ok {
		t.Fatal("PendingStoredFileStart ok = false, want true")
	}
	if path != filePath {
		t.Fatalf("pending path = %q, want %q", path, filePath)
	}
	if since.IsZero() {
		t.Fatal("pending since is zero, want non-zero")
	}

	if !q.ClearPendingStoredFileStart() {
		t.Fatal("first ClearPendingStoredFileStart = false, want true")
	}
	if q.ClearPendingStoredFileStart() {
		t.Fatal("second ClearPendingStoredFileStart = true, want false")
	}
	if _, _, ok := q.PendingStoredFileStart(); ok {
		t.Fatal("PendingStoredFileStart ok = true after clear, want false")
	}
}
