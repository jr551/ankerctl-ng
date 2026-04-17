package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type mockVideoController struct {
	mu       sync.Mutex
	start    int
	stop     int
	lastMode int
}

func (m *mockVideoController) StartLive(ctx context.Context, mode int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.start++
	m.lastMode = mode
	return nil
}
func (m *mockVideoController) StopLive(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stop++
	return nil
}
func (m *mockVideoController) SetVideoMode(ctx context.Context, mode int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lastMode = mode
	return nil
}

type mockLightController struct {
	mu    sync.Mutex
	calls []bool
}

func (m *mockLightController) SetLight(ctx context.Context, on bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, on)
	return nil
}

func TestVideoQueueStallTriggersRestart(t *testing.T) {
	controller := &mockVideoController{}
	q := NewVideoQueue(controller, nil)
	q.stallTimeout = 20 * time.Millisecond
	q.checkInterval = 5 * time.Millisecond
	q.SetVideoEnabled(true)
	unsub := q.Tap(func(any) {})
	defer unsub()

	if err := q.WorkerStart(); err != nil {
		t.Fatalf("WorkerStart: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	err := q.WorkerRun(ctx)
	if !errors.Is(err, ErrServiceRestartSignal) {
		t.Fatalf("WorkerRun err = %v, want restart signal", err)
	}
}

func TestVideoQueueCaptureSnapshotTurnsOnLight(t *testing.T) {
	light := &mockLightController{}
	q := NewVideoQueue(nil, light)

	runs := 0
	q.runFFmpeg = func(ctx context.Context, args []string) error {
		runs++
		out := args[len(args)-1]
		return os.WriteFile(out, []byte("jpg"), 0o644)
	}

	dir := t.TempDir()
	out := filepath.Join(dir, "snap.jpg")
	if err := q.CaptureSnapshot(context.Background(), out); err != nil {
		t.Fatalf("CaptureSnapshot: %v", err)
	}

	if runs == 0 {
		t.Fatal("expected ffmpeg runner to be called")
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("snapshot file missing: %v", err)
	}
	if len(light.calls) == 0 || !light.calls[0] {
		t.Fatalf("expected light ON command before snapshot, got %v", light.calls)
	}
}

func TestVideoQueueSetProfile(t *testing.T) {
	controller := &mockVideoController{}
	q := NewVideoQueue(controller, nil)
	if err := q.SetProfile("sd"); err != nil {
		t.Fatalf("SetProfile(sd): %v", err)
	}
	controller.mu.Lock()
	mode := controller.lastMode
	controller.mu.Unlock()
	if mode != 0 {
		t.Fatalf("last mode = %d, want 0", mode)
	}
}

func TestVideoQueueEnableRecoversDormantRunningWorker(t *testing.T) {
	controller := &mockVideoController{}
	q := NewVideoQueue(controller, nil)
	t.Cleanup(q.Shutdown)

	q.Start(context.Background())
	waitForVideoState(t, q, StateRunning, 500*time.Millisecond)

	controller.mu.Lock()
	starts := controller.start
	controller.mu.Unlock()
	if starts != 0 {
		t.Fatalf("start count before enable = %d, want 0", starts)
	}

	q.SetVideoEnabled(true)
	waitForVideoControllerStart(t, controller, 1500*time.Millisecond)
}

func waitForVideoState(t *testing.T, q *VideoQueue, want RunState, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if q.State() == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("state = %v, want %v", q.State(), want)
}

func waitForVideoControllerStart(t *testing.T, controller *mockVideoController, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		controller.mu.Lock()
		starts := controller.start
		controller.mu.Unlock()
		if starts > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	controller.mu.Lock()
	starts := controller.start
	controller.mu.Unlock()
	t.Fatalf("start count = %d, want > 0", starts)
}

func TestScrubURLCredentials(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		contains []string // strings that MUST be present in the output
		missing  []string // strings that MUST NOT be present in the output
	}{
		{
			name:     "rtsp userinfo is redacted",
			input:    `open rtsp://alice:s3cret@cam.local:554/live failed`,
			contains: []string{"rtsp://", "cam.local", "/live", "***"},
			missing:  []string{"alice", "s3cret"},
		},
		{
			name:     "apikey query param is redacted",
			input:    `ffmpeg error opening http://127.0.0.1:4470/video?for_timelapse=1&apikey=SECRETTOKEN12345`,
			contains: []string{"http://127.0.0.1:4470/video", "apikey=", "***"},
			missing:  []string{"SECRETTOKEN12345"},
		},
		{
			name:     "token and password params are redacted",
			input:    `https://host/api?token=abc123&password=hunter2&keep=me`,
			contains: []string{"host/api", "keep=me", "***"},
			missing:  []string{"abc123", "hunter2"},
		},
		{
			name:     "non-URL text is untouched",
			input:    "ffmpeg: no such file or directory",
			contains: []string{"ffmpeg: no such file or directory"},
			missing:  nil,
		},
		{
			name:     "empty string is untouched",
			input:    "",
			contains: nil,
			missing:  nil,
		},
		{
			name:     "https userinfo is redacted",
			input:    `GET https://user:pw@example.com/path?x=1 200 OK`,
			contains: []string{"https://", "example.com", "/path", "***"},
			missing:  []string{"user", "pw"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := scrubURLCredentials(tt.input)
			for _, want := range tt.contains {
				if !strings.Contains(got, want) {
					t.Errorf("scrubURLCredentials(%q) missing %q; got %q", tt.input, want, got)
				}
			}
			for _, forbid := range tt.missing {
				if strings.Contains(got, forbid) {
					t.Errorf("scrubURLCredentials(%q) leaked %q; got %q", tt.input, forbid, got)
				}
			}
		})
	}
}
