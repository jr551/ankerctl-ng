package service

import (
	"context"
	"os"
	"testing"
)

type nopSnapshotter struct{}

func (nopSnapshotter) CaptureSnapshot(_ context.Context, path string) error {
	// Write a small dummy file so frame count increments correctly.
	return os.WriteFile(path, []byte("fake"), 0o644)
}
func (nopSnapshotter) SetLight(_ context.Context, _ bool) error { return nil }

func newTestTimelapseService(t *testing.T) *TimelapseService {
	t.Helper()
	dir := t.TempDir()
	svc := NewTimelapseService(dir, nopSnapshotter{})
	return svc
}

func TestTimelapseStatus_Idle(t *testing.T) {
	svc := newTestTimelapseService(t)
	st := svc.Status()
	if st.State != "idle" {
		t.Errorf("expected idle, got %q", st.State)
	}
}

func TestManualStart_Success(t *testing.T) {
	svc := newTestTimelapseService(t)
	// Enable so StartCapture does something.
	svc.enabled = true

	filename, err := svc.ManualStart("test_print.gcode")
	if err != nil {
		t.Fatalf("ManualStart failed: %v", err)
	}
	if filename != "test_print.gcode" {
		t.Errorf("filename = %q, want test_print.gcode", filename)
	}
}

func TestManualStart_AlreadyActiveReturnsError(t *testing.T) {
	svc := newTestTimelapseService(t)
	svc.mu.Lock()
	svc.active = &captureState{Filename: "running.gcode"}
	svc.mu.Unlock()

	_, err := svc.ManualStart("other.gcode")
	if err == nil {
		t.Fatal("expected error when capture already active")
	}
}

func TestManualPause_NothingActive(t *testing.T) {
	svc := newTestTimelapseService(t)
	_, err := svc.ManualPause()
	if err == nil {
		t.Fatal("expected error when nothing active to pause")
	}
}

func TestManualPause_ActiveCapture(t *testing.T) {
	svc := newTestTimelapseService(t)
	svc.mu.Lock()
	svc.active = &captureState{Filename: "printing.gcode", Dir: t.TempDir()}
	svc.mu.Unlock()

	filename, err := svc.ManualPause()
	if err != nil {
		t.Fatalf("ManualPause failed: %v", err)
	}
	if filename != "printing.gcode" {
		t.Errorf("filename = %q, want printing.gcode", filename)
	}
}

func TestManualResume_NoPausedCapture(t *testing.T) {
	svc := newTestTimelapseService(t)
	_, err := svc.ManualResume()
	if err == nil {
		t.Fatal("expected error when nothing paused to resume")
	}
}

func TestManualStop_NothingActive(t *testing.T) {
	svc := newTestTimelapseService(t)
	_, err := svc.ManualStop()
	if err == nil {
		t.Fatal("expected error when nothing active to stop")
	}
}

func TestManualStop_ActiveCapture(t *testing.T) {
	svc := newTestTimelapseService(t)
	svc.mu.Lock()
	svc.active = &captureState{Filename: "stopping.gcode", Dir: t.TempDir()}
	svc.mu.Unlock()

	filename, err := svc.ManualStop()
	if err != nil {
		t.Fatalf("ManualStop failed: %v", err)
	}
	if filename != "stopping.gcode" {
		t.Errorf("filename = %q, want stopping.gcode", filename)
	}
}

func TestManualDismiss_ClearsResume(t *testing.T) {
	svc := newTestTimelapseService(t)
	svc.mu.Lock()
	svc.resume = &resumeState{captureState: captureState{Filename: "paused.gcode"}}
	svc.mu.Unlock()

	svc.ManualDismiss()

	svc.mu.Lock()
	r := svc.resume
	svc.mu.Unlock()

	if r != nil {
		t.Error("ManualDismiss should clear resume state")
	}
}
