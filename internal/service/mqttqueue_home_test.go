package service

import (
	"context"
	"testing"

	"github.com/django1982/ankerctl/internal/mqtt/protocol"
)

// TestMqttQueue_SendHome validates that SendHome builds the correct MQTT
// payload for ZZ_MQTT_CMD_MOVE_ZERO (ct=1026) and maps axes to the values
// observed in the Python reference (web/service/mqtt.py):
//
//	HOME_MOVE_ZERO_VALUE_BY_AXIS = {"xy": 0, "z": 2}
//
// "all" is routed through "z" (value=2) because the M5 firmware homes XY
// as part of the Z homing sequence.
func TestMqttQueue_SendHome(t *testing.T) {
	cases := []struct {
		name     string
		axis     string
		wantCT   int
		wantVal  int
		wantErr  bool
	}{
		{name: "all axes (default)", axis: "all", wantCT: int(protocol.MqttCmdMoveZero), wantVal: 2},
		{name: "empty string = all", axis: "", wantCT: int(protocol.MqttCmdMoveZero), wantVal: 2},
		{name: "z only", axis: "z", wantCT: int(protocol.MqttCmdMoveZero), wantVal: 2},
		{name: "xy only", axis: "xy", wantCT: int(protocol.MqttCmdMoveZero), wantVal: 0},
		{name: "uppercase Z", axis: "Z", wantCT: int(protocol.MqttCmdMoveZero), wantVal: 2},
		{name: "uppercase XY", axis: "XY", wantCT: int(protocol.MqttCmdMoveZero), wantVal: 0},
		{name: "invalid axis", axis: "x", wantErr: true},
		{name: "invalid xyz", axis: "xyz", wantErr: true},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			client := &fakeMQTTClient{}
			q := &MqttQueue{
				BaseWorker: NewBaseWorker("test-home"),
				client:     client,
			}

			err := q.SendHome(context.Background(), tc.axis)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("SendHome(%q) expected error, got nil", tc.axis)
				}
				// No command must have been sent on error.
				if len(client.commands) != 0 {
					t.Errorf("SendHome(%q) sent command despite error: %v", tc.axis, client.commands)
				}
				return
			}
			if err != nil {
				t.Fatalf("SendHome(%q) unexpected error: %v", tc.axis, err)
			}
			if len(client.commands) != 1 {
				t.Fatalf("SendHome(%q) expected 1 command, got %d", tc.axis, len(client.commands))
			}

			cmd := client.commands[0]

			gotCT, ok := cmd["commandType"].(int)
			if !ok {
				t.Fatalf("commandType is %T, want int", cmd["commandType"])
			}
			if gotCT != tc.wantCT {
				t.Errorf("commandType = %d, want %d", gotCT, tc.wantCT)
			}

			gotVal, ok := cmd["value"].(int)
			if !ok {
				t.Fatalf("value is %T, want int", cmd["value"])
			}
			if gotVal != tc.wantVal {
				t.Errorf("value = %d, want %d", gotVal, tc.wantVal)
			}

			// Verify no extra keys are present (payload must be minimal).
			if len(cmd) != 2 {
				t.Errorf("command has %d keys, want exactly 2: %v", len(cmd), cmd)
			}
		})
	}
}

// TestMqttQueue_SendHome_Disconnected verifies that SendHome returns an error
// when no MQTT client is connected, without panicking.
func TestMqttQueue_SendHome_Disconnected(t *testing.T) {
	q := &MqttQueue{
		BaseWorker: NewBaseWorker("test-home-disconnected"),
		// client is nil — simulates disconnected state
	}
	err := q.SendHome(context.Background(), "all")
	if err == nil {
		t.Fatal("expected error when client is nil, got nil")
	}
}
