package service

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/django1982/ankerctl/internal/db"
	mqttclient "github.com/django1982/ankerctl/internal/mqtt/client"
	"github.com/django1982/ankerctl/internal/mqtt/protocol"
)

// TestMqttQueueFinishesAtFullProgress verifies that a print which reaches 100%
// is marked finished even though the M5C stays in the "printing" state and
// never reports idle — and that subsequent "printing" reports are ignored
// (the completion latch) until a new print resets progress.
func TestMqttQueueFinishesAtFullProgress(t *testing.T) {
	historyDB, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open history db: %v", err)
	}
	defer historyDB.Close()

	client := &fakeMQTTClient{
		queue: [][]mqttclient.DecodedMessage{
			{{Objects: []map[string]any{{"commandType": int(protocol.MqttCmdEventNotify), "value": mqttStatePrinting}}}},
			{{Objects: []map[string]any{{"commandType": int(protocol.MqttCmdPrintSchedule), "name": "cube.gcode", "progress": 40}}}},
			{{Objects: []map[string]any{{"commandType": int(protocol.MqttCmdPrintSchedule), "progress": 100}}}},
			// M5C keeps reporting "printing" after the model finishes — must be ignored.
			{{Objects: []map[string]any{{"commandType": int(protocol.MqttCmdEventNotify), "value": mqttStatePrinting}}}},
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
		log:                slog.Default(),
		homeAssistant:      ha,
		timelapse:          timelapse,
	}
	q.BindHooks(q)
	if err := q.WorkerStart(); err != nil {
		t.Fatalf("WorkerStart: %v", err)
	}
	defer q.WorkerStop()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
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
	if rows[0].Status != "finished" {
		t.Fatalf("history status = %q, want finished", rows[0].Status)
	}
	if rows[0].Progress != 100 {
		t.Fatalf("history progress = %d, want 100", rows[0].Progress)
	}

	// The printer keeps reporting "printing" at 100%, but the latch must keep us idle.
	if q.IsPrinting() {
		t.Fatal("IsPrinting = true after completion, want false (completion latch should hold)")
	}

	sawIdle := false
	for _, evt := range ha.got {
		m, ok := evt.(map[string]any)
		if !ok || m["event"] != "print_state" {
			continue
		}
		if s, ok := m["state"].(int); ok && s == mqttStateIdle {
			sawIdle = true
		}
	}
	if !sawIdle {
		t.Fatalf("expected a print_state idle event on completion, got %v", ha.got)
	}
}

// TestMqttQueueDoesNotFinishBelowFullProgress guards against a premature finish:
// a mid-print progress update must not mark the print complete.
func TestMqttQueueDoesNotFinishBelowFullProgress(t *testing.T) {
	historyDB, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open history db: %v", err)
	}
	defer historyDB.Close()

	client := &fakeMQTTClient{
		queue: [][]mqttclient.DecodedMessage{
			{{Objects: []map[string]any{{"commandType": int(protocol.MqttCmdEventNotify), "value": mqttStatePrinting}}}},
			{{Objects: []map[string]any{{"commandType": int(protocol.MqttCmdPrintSchedule), "name": "cube.gcode", "progress": 99}}}},
		},
	}

	q := &MqttQueue{
		BaseWorker:         NewBaseWorker("mqttqueue"),
		history:            historyDB,
		clientFactory:      func(context.Context) (mqttClient, error) { return client, nil },
		queryInterval:      time.Hour,
		pollInterval:       5 * time.Millisecond,
		currentPrinterStat: -1,
		log:                slog.Default(),
		homeAssistant:      &captureSink{},
		timelapse:          &captureSink{},
	}
	q.BindHooks(q)
	if err := q.WorkerStart(); err != nil {
		t.Fatalf("WorkerStart: %v", err)
	}
	defer q.WorkerStop()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	if err := q.WorkerRun(ctx); err != nil {
		t.Fatalf("WorkerRun: %v", err)
	}

	if !q.IsPrinting() {
		t.Fatal("IsPrinting = false at 99%, want true (must not finish below 100%)")
	}
	rows, err := historyDB.GetHistory(10, 0)
	if err != nil {
		t.Fatalf("GetHistory: %v", err)
	}
	if len(rows) != 1 || rows[0].Status != "started" {
		t.Fatalf("history = %+v, want one 'started' row", rows)
	}
}
