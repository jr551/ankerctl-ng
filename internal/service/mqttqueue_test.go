package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/django1982/ankerctl/internal/db"
	mqttclient "github.com/django1982/ankerctl/internal/mqtt/client"
	"github.com/django1982/ankerctl/internal/mqtt/protocol"
)

type fakeMQTTClient struct {
	mu       sync.Mutex
	queue    [][]mqttclient.DecodedMessage
	commands []map[string]any
	queries  []map[string]any
}

func (f *fakeMQTTClient) Fetch() []mqttclient.DecodedMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.queue) == 0 {
		return nil
	}
	out := f.queue[0]
	f.queue = f.queue[1:]
	return out
}

func (f *fakeMQTTClient) Command(_ context.Context, msg any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commands = append(f.commands, msg.(map[string]any))
	return nil
}

func (f *fakeMQTTClient) Query(_ context.Context, msg any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries = append(f.queries, msg.(map[string]any))
	return nil
}

func (f *fakeMQTTClient) Disconnect(_ time.Duration) {}

type captureSink struct {
	mu   sync.Mutex
	got  []any
	last any
}

func (c *captureSink) Notify(data any) {
	c.mu.Lock()
	c.got = append(c.got, data)
	c.last = data
	c.mu.Unlock()
}

func TestMqttQueue_StateMachineDeferredHistoryStart(t *testing.T) {
	historyDB, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open history db: %v", err)
	}
	defer historyDB.Close()

	client := &fakeMQTTClient{
		queue: [][]mqttclient.DecodedMessage{
			{{
				Objects: []map[string]any{
					{"commandType": int(protocol.MqttCmdEventNotify), "value": 1},
				},
			}},
			{{
				Objects: []map[string]any{
					{"commandType": int(protocol.MqttCmdModelDLProcess), "filePath": "/tmp/benchy.gcode"},
				},
			}},
		},
	}

	ha := &captureSink{}
	timelapse := &captureSink{}
	q := &MqttQueue{
		BaseWorker:         NewBaseWorker("mqttqueue"),
		history:            historyDB,
		clientFactory:      func(context.Context) (mqttClient, error) { return client, nil },
		queryInterval:      time.Hour,
		pollInterval:       5 * time.Millisecond,
		currentPrinterStat: -1,
		homeAssistant:      ha,
		timelapse:          timelapse,
	}
	q.BindHooks(q)
	if err := q.WorkerStart(); err != nil {
		t.Fatalf("WorkerStart: %v", err)
	}
	defer q.WorkerStop()

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	if err := q.WorkerRun(ctx); err != nil {
		t.Fatalf("WorkerRun: %v", err)
	}

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
	if rows[0].Status != "started" {
		t.Fatalf("history status = %q, want started", rows[0].Status)
	}
	if len(ha.got) == 0 || len(timelapse.got) == 0 {
		t.Fatalf("expected forwarded events to HA/timelapse, got ha=%d timelapse=%d", len(ha.got), len(timelapse.got))
	}
}

func TestNormalizeProgressFromMQTTScale(t *testing.T) {
	tests := []struct {
		in   int
		want int
	}{
		{in: -1, want: 0},
		{in: 0, want: 0},
		{in: 42, want: 42},
		{in: 9999, want: 99},
		{in: 10000, want: 100},
		{in: 5000, want: 50},
		{in: 12000, want: 100},
	}
	for _, tc := range tests {
		if got := normalizeProgress(tc.in); got != tc.want {
			t.Fatalf("normalizeProgress(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestExtractProgress(t *testing.T) {
	tests := []struct {
		name    string
		payload map[string]any
		want    int
		ok      bool
	}{
		{
			name:    "top level progress",
			payload: map[string]any{"progress": 42},
			want:    42,
			ok:      true,
		},
		{
			name:    "top level progress variant",
			payload: map[string]any{"printProgress": 73},
			want:    73,
			ok:      true,
		},
		{
			name:    "nested progress",
			payload: map[string]any{"job": map[string]any{"progress": 88}},
			want:    88,
			ok:      true,
		},
		{
			name:    "ignores non numeric variant before nested exact progress",
			payload: map[string]any{"progressState": "unknown", "job": map[string]any{"progress": 19}},
			want:    19,
			ok:      true,
		},
		{
			name:    "missing progress",
			payload: map[string]any{"value": 1},
			ok:      false,
		},
	}

	for _, tc := range tests {
		got, ok := extractProgress(tc.payload)
		if ok != tc.ok {
			t.Fatalf("%s: ok=%v, want %v", tc.name, ok, tc.ok)
		}
		if got != tc.want {
			t.Fatalf("%s: progress=%d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestMqttQueueSendGCode_SplitsLinesAndCmdLen(t *testing.T) {
	client := &fakeMQTTClient{}
	q := &MqttQueue{client: client}

	if err := q.SendGCode(context.Background(), "M104 S200\n\nG28\n"); err != nil {
		t.Fatalf("SendGCode: %v", err)
	}

	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.commands) != 2 {
		t.Fatalf("commands=%d, want 2", len(client.commands))
	}
	first := client.commands[0]
	second := client.commands[1]
	if first["cmdData"] != "M104 S200" || first["cmdLen"] != len("M104 S200") {
		t.Fatalf("first command mismatch: %#v", first)
	}
	if second["cmdData"] != "G28" || second["cmdLen"] != len("G28") {
		t.Fatalf("second command mismatch: %#v", second)
	}
}

func TestMqttQueueSendAutoLeveling_UsesValueZero(t *testing.T) {
	client := &fakeMQTTClient{}
	q := &MqttQueue{client: client}

	if err := q.SendAutoLeveling(context.Background()); err != nil {
		t.Fatalf("SendAutoLeveling: %v", err)
	}

	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.commands) != 1 {
		t.Fatalf("commands=%d, want 1", len(client.commands))
	}
	if got := client.commands[0]["value"]; got != 0 {
		t.Fatalf("autolevel value=%v, want 0", got)
	}
}

func TestMqttQueueHandlersTrackScheduleSpeedAndLayer(t *testing.T) {
	ha := &captureSink{}
	timelapse := &captureSink{}
	q := &MqttQueue{
		BaseWorker:    NewBaseWorker("mqttqueue"),
		homeAssistant: ha,
		timelapse:     timelapse,
	}
	q.BindHooks(q)

	q.handlePayload(map[string]any{
		"commandType": int(protocol.MqttCmdPrintSchedule),
		"progress":    4200,
		"fileName":    "benchy.gcode",
		"elapsed":     120,
		"remaining":   60,
	})
	q.handlePayload(map[string]any{
		"commandType": int(protocol.MqttCmdPrintSpeed),
		"value":       250,
	})
	q.handlePayload(map[string]any{
		"commandType":      int(protocol.MqttCmdModelLayer),
		"real_print_layer": 42,
		"total_layer":      200,
	})

	snap := q.SnapshotState()
	if got := snap["last_filename"]; got != "benchy.gcode" {
		t.Fatalf("last_filename = %v, want benchy.gcode", got)
	}
	if got := snap["print_progress"]; got != 42 {
		t.Fatalf("print_progress = %v, want 42", got)
	}
	if got := snap["time_elapsed"]; got != 120 {
		t.Fatalf("time_elapsed = %v, want 120", got)
	}
	if got := snap["time_remaining"]; got != 60 {
		t.Fatalf("time_remaining = %v, want 60", got)
	}
	if got := snap["print_speed"]; got != 250 {
		t.Fatalf("print_speed = %v, want 250", got)
	}
	if got := snap["current_layer"]; got != 42 {
		t.Fatalf("current_layer = %v, want 42", got)
	}
	if got := snap["total_layers"]; got != 200 {
		t.Fatalf("total_layers = %v, want 200", got)
	}
	if got := snap["print_layer"]; got != "42/200" {
		t.Fatalf("print_layer = %v, want 42/200", got)
	}

	if len(ha.got) != 3 {
		t.Fatalf("HA forwards = %d, want 3", len(ha.got))
	}
	if len(timelapse.got) != 3 {
		t.Fatalf("timelapse forwards = %d, want 3", len(timelapse.got))
	}
}

func TestMqttQueuePrintScheduleCompletesDeferredHistoryStart(t *testing.T) {
	historyDB, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open history db: %v", err)
	}
	defer historyDB.Close()

	q := &MqttQueue{
		BaseWorker:         NewBaseWorker("mqttqueue"),
		history:            historyDB,
		currentPrinterStat: -1,
	}
	q.BindHooks(q)

	q.handlePayload(map[string]any{
		"commandType": int(protocol.MqttCmdEventNotify),
		"value":       1,
	})
	q.handlePayload(map[string]any{
		"commandType": int(protocol.MqttCmdPrintSchedule),
		"progress":    1,
		"fileName":    "benchy.gcode",
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
	if rows[0].Status != "started" {
		t.Fatalf("history status = %q, want started", rows[0].Status)
	}
}

func TestMqttQueueFilamentRunoutHandlers(t *testing.T) {
	q := &MqttQueue{
		BaseWorker:         NewBaseWorker("mqttqueue"),
		currentPrinterStat: -1,
	}
	q.BindHooks(q)

	runout := map[string]any{
		"commandType": int(protocol.MqttCmdFilamentRunout),
		"errorCode":   filamentRunoutErrorCode,
	}
	jam := map[string]any{
		"commandType": int(protocol.MqttCmdFilamentJam),
		"errorCode":   filamentRunoutErrorCode,
	}

	q.handlePayload(runout)
	snap := q.SnapshotState()
	filament, ok := snap["filament"].(map[string]any)
	if !ok {
		t.Fatalf("filament snapshot missing after runout: %#v", snap)
	}
	if got := filament["runout_pending"]; got != true {
		t.Fatalf("runout_pending = %v, want true", got)
	}

	q.handlePayload(jam)
	snap = q.SnapshotState()
	if _, ok := snap["filament"]; ok {
		t.Fatalf("filament snapshot should clear pending jam ack, got %#v", snap["filament"])
	}

	q.handlePayload(runout)
	q.handlePayload(map[string]any{
		"commandType": int(protocol.MqttCmdEventNotify),
		"value":       2,
	})
	snap = q.SnapshotState()
	filament, ok = snap["filament"].(map[string]any)
	if !ok {
		t.Fatalf("filament snapshot missing after pause promotion: %#v", snap)
	}
	if got := filament["runout_pending"]; got != false {
		t.Fatalf("runout_pending = %v, want false", got)
	}
	if got := filament["issue"]; got != filamentIssueRunout {
		t.Fatalf("issue = %v, want %q", got, filamentIssueRunout)
	}
	if got := filament["issue_code"]; got != filamentRunoutErrorCode {
		t.Fatalf("issue_code = %v, want %q", got, filamentRunoutErrorCode)
	}

	q.handlePayload(jam)
	snap = q.SnapshotState()
	filament, ok = snap["filament"].(map[string]any)
	if !ok || filament["issue"] != filamentIssueRunout {
		t.Fatalf("active runout should survive jam ack, got %#v", snap["filament"])
	}

	q.handlePayload(map[string]any{
		"commandType": int(protocol.MqttCmdEventNotify),
		"value":       0,
	})
	snap = q.SnapshotState()
	if _, ok := snap["filament"]; ok {
		t.Fatalf("filament snapshot should clear on idle, got %#v", snap["filament"])
	}
}
