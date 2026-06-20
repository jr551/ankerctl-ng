package model

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestExternalCamera_BackwardCompatLoad ensures that an old config.json camera
// entry (only name/stream_url/snapshot_url/refresh_sec, no kind/fields) loads
// unchanged and reports an empty Kind (which behaves as custom).
func TestExternalCamera_BackwardCompatLoad(t *testing.T) {
	oldJSON := `{
		"per_printer": {
			"SN001": {
				"source": "external",
				"external": {
					"name": "Garage cam",
					"stream_url": "http://cam.local/stream",
					"snapshot_url": "http://cam.local/snap.jpg",
					"refresh_sec": 5
				}
			}
		}
	}`

	var cam CameraConfig
	if err := json.Unmarshal([]byte(oldJSON), &cam); err != nil {
		t.Fatalf("unmarshal old config: %v", err)
	}
	entry, ok := cam.PerPrinter["SN001"]
	if !ok {
		t.Fatal("SN001 entry missing")
	}
	if entry.Source != CameraSourceExternal {
		t.Errorf("source = %q, want %q", entry.Source, CameraSourceExternal)
	}
	if entry.External.StreamURL != "http://cam.local/stream" {
		t.Errorf("stream_url = %q", entry.External.StreamURL)
	}
	if entry.External.SnapshotURL != "http://cam.local/snap.jpg" {
		t.Errorf("snapshot_url = %q", entry.External.SnapshotURL)
	}
	if entry.External.RefreshSec != 5 {
		t.Errorf("refresh_sec = %d, want 5", entry.External.RefreshSec)
	}
	if entry.External.Kind != "" {
		t.Errorf("kind = %q, want empty for legacy config", entry.External.Kind)
	}
	if entry.External.Fields != nil {
		t.Errorf("fields = %v, want nil for legacy config", entry.External.Fields)
	}
}

// TestExternalCamera_LegacyOmitsKindAndFields ensures marshalling a legacy-style
// entry (no kind/fields) does not introduce the new keys, keeping the on-disk
// JSON shape stable for users who never touch the new presets.
func TestExternalCamera_LegacyOmitsKindAndFields(t *testing.T) {
	e := ExternalCameraSettings{
		Name:        "cam",
		StreamURL:   "http://cam.local/stream",
		SnapshotURL: "http://cam.local/snap.jpg",
		RefreshSec:  3,
	}
	data, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(data)
	if strings.Contains(s, `"kind"`) {
		t.Errorf("legacy marshal should omit kind, got %s", s)
	}
	if strings.Contains(s, `"fields"`) {
		t.Errorf("legacy marshal should omit fields, got %s", s)
	}
}

// TestExternalCamera_PresetRoundTrip checks that a preset entry (kind + fields)
// survives a marshal/unmarshal cycle.
func TestExternalCamera_PresetRoundTrip(t *testing.T) {
	orig := ExternalCameraSettings{
		Name:        "Frigate front door",
		StreamURL:   "http://frigate.local:5000/api/front_door",
		SnapshotURL: "http://frigate.local:5000/api/front_door/latest.jpg",
		RefreshSec:  3,
		Kind:        CameraKindFrigate,
		Fields: map[string]string{
			"base_url": "http://frigate.local:5000",
			"camera":   "front_door",
		},
	}
	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got ExternalCameraSettings
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Kind != CameraKindFrigate {
		t.Errorf("kind = %q, want %q", got.Kind, CameraKindFrigate)
	}
	if got.Fields["camera"] != "front_door" {
		t.Errorf("fields[camera] = %q, want front_door", got.Fields["camera"])
	}
	if got.StreamURL != orig.StreamURL || got.SnapshotURL != orig.SnapshotURL {
		t.Errorf("URLs not preserved: %+v", got)
	}
}

func TestNormalizeCameraKind(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", CameraKindCustom},
		{"custom", CameraKindCustom},
		{"FRIGATE", CameraKindFrigate},
		{"  go2rtc ", CameraKindGo2RTC},
		{"mjpeg", CameraKindMJPEG},
		{"octoprint", CameraKindOctoPrint},
		{"reolink", CameraKindReolink},
		{"rtsp", CameraKindRTSP},
		{"bogus", CameraKindCustom},
	}
	for _, tc := range cases {
		if got := NormalizeCameraKind(tc.in); got != tc.want {
			t.Errorf("NormalizeCameraKind(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestDeriveExternalCameraURLs(t *testing.T) {
	cases := []struct {
		name         string
		kind         string
		fields       map[string]string
		wantStream   string
		wantSnapshot string
	}{
		{
			name:       "mjpeg",
			kind:       CameraKindMJPEG,
			fields:     map[string]string{"stream_url": "http://cam.local:8080/video"},
			wantStream: "http://cam.local:8080/video",
		},
		{
			name:       "rtsp passthrough",
			kind:       CameraKindRTSP,
			fields:     map[string]string{"stream_url": "rtsp://cam.local:554/live"},
			wantStream: "rtsp://cam.local:554/live",
		},
		{
			name:         "octoprint trims trailing slash",
			kind:         CameraKindOctoPrint,
			fields:       map[string]string{"base_url": "http://octopi.local/"},
			wantStream:   "http://octopi.local/webcam/?action=stream",
			wantSnapshot: "http://octopi.local/webcam/?action=snapshot",
		},
		{
			name:         "frigate",
			kind:         CameraKindFrigate,
			fields:       map[string]string{"base_url": "http://frigate.local:5000", "camera": "garage"},
			wantStream:   "http://frigate.local:5000/api/garage",
			wantSnapshot: "http://frigate.local:5000/api/garage/latest.jpg",
		},
		{
			name:         "go2rtc",
			kind:         CameraKindGo2RTC,
			fields:       map[string]string{"base_url": "http://go2rtc.local:1984", "stream": "printer"},
			wantStream:   "http://go2rtc.local:1984/api/stream.mjpeg?src=printer",
			wantSnapshot: "http://go2rtc.local:1984/api/frame.jpeg?src=printer",
		},
		{
			name:         "reolink with creds default channel",
			kind:         CameraKindReolink,
			fields:       map[string]string{"host": "cam.local", "user": "admin", "password": "pw"},
			wantStream:   "http://cam.local/flv?port=1935&app=bcs&stream=channel0_main.bcs&user=admin&password=pw",
			wantSnapshot: "http://cam.local/cgi-bin/api.cgi?cmd=Snap&channel=0&rs=ankerctl&user=admin&password=pw",
		},
		{
			name:         "reolink no creds custom channel keeps scheme",
			kind:         CameraKindReolink,
			fields:       map[string]string{"host": "https://cam.local", "channel": "2"},
			wantStream:   "https://cam.local/flv?port=1935&app=bcs&stream=channel2_main.bcs",
			wantSnapshot: "https://cam.local/cgi-bin/api.cgi?cmd=Snap&channel=2&rs=ankerctl",
		},
		{
			name:   "custom returns empty (use direct URLs)",
			kind:   CameraKindCustom,
			fields: map[string]string{"stream_url": "ignored"},
		},
		{
			name:   "incomplete frigate returns empty",
			kind:   CameraKindFrigate,
			fields: map[string]string{"base_url": "http://frigate.local:5000"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotStream, gotSnapshot := DeriveExternalCameraURLs(tc.kind, tc.fields)
			if gotStream != tc.wantStream {
				t.Errorf("stream = %q, want %q", gotStream, tc.wantStream)
			}
			if gotSnapshot != tc.wantSnapshot {
				t.Errorf("snapshot = %q, want %q", gotSnapshot, tc.wantSnapshot)
			}
		})
	}
}
