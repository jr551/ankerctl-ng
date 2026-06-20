package service

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// scaleFilter
// ---------------------------------------------------------------------------

func TestScaleFilter(t *testing.T) {
	tests := []struct {
		name        string
		scale       MJPEGScale
		wantEmpty   bool
		wantContain []string
	}{
		{
			name:      "zero width disables filter",
			scale:     MJPEGScale{0, 720},
			wantEmpty: true,
		},
		{
			name:      "zero height disables filter",
			scale:     MJPEGScale{1280, 0},
			wantEmpty: true,
		},
		{
			name:      "both zero disables filter",
			scale:     MJPEGScale{0, 0},
			wantEmpty: true,
		},
		{
			name:  "normal dimensions produce valid expression",
			scale: MJPEGScale{1280, 720},
			wantContain: []string{
				"scale=1280:720",
				"force_original_aspect_ratio=decrease",
				"pad=1280:720",
			},
		},
		{
			name:  "small dimensions still include all required components",
			scale: MJPEGScale{640, 480},
			wantContain: []string{
				"scale=640:480",
				"force_original_aspect_ratio=decrease",
				"pad=640:480",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := scaleFilter(tt.scale)
			if tt.wantEmpty {
				if got != "" {
					t.Errorf("scaleFilter(%v) = %q, want empty", tt.scale, got)
				}
				return
			}
			for _, want := range tt.wantContain {
				if !strings.Contains(got, want) {
					t.Errorf("scaleFilter(%v) = %q, missing %q", tt.scale, got, want)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// PrinterMJPEGCmd
// ---------------------------------------------------------------------------

func TestPrinterMJPEGCmd(t *testing.T) {
	ctx := context.Background()
	const videoURL = "http://127.0.0.1:4470/video?for_timelapse=1"

	tests := []struct {
		name       string
		apiKey     string
		fps        int
		quality    int
		scale      MJPEGScale
		wantArgs   []string
		wantAbsent []string
	}{
		{
			name:     "fps 0 defaults to 5",
			fps:      0,
			quality:  3,
			scale:    MJPEGScale{0, 0},
			wantArgs: []string{"-r", "5"},
		},
		{
			name:     "quality 0 defaults to 5",
			fps:      3,
			quality:  0,
			scale:    MJPEGScale{0, 0},
			wantArgs: []string{"-q:v", "5"},
		},
		{
			name:     "apiKey non-empty adds headers arg",
			fps:      5,
			quality:  5,
			scale:    MJPEGScale{0, 0},
			apiKey:   "MY_SECRET_KEY",
			wantArgs: []string{"-headers", "X-Api-Key: MY_SECRET_KEY\r\n"},
		},
		{
			name:       "apiKey empty omits headers arg",
			fps:        5,
			quality:    5,
			scale:      MJPEGScale{0, 0},
			apiKey:     "",
			wantAbsent: []string{"-headers"},
		},
		{
			name:     "non-zero scale adds -vf arg",
			fps:      5,
			quality:  5,
			scale:    MJPEGScale{1280, 720},
			wantArgs: []string{"-vf"},
		},
		{
			name:       "zero scale omits -vf arg",
			fps:        5,
			quality:    5,
			scale:      MJPEGScale{0, 0},
			wantAbsent: []string{"-vf"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := PrinterMJPEGCmd(ctx, videoURL, tt.apiKey, tt.fps, tt.quality, tt.scale)
			if cmd == nil {
				t.Fatal("PrinterMJPEGCmd returned nil")
			}
			args := cmd.Args // includes the executable name at [0]

			for _, want := range tt.wantArgs {
				if !argsContain(args, want) {
					t.Errorf("expected arg %q in %v", want, args)
				}
			}
			for _, absent := range tt.wantAbsent {
				if argsContain(args, absent) {
					t.Errorf("unexpected arg %q in %v", absent, args)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ExternalMJPEGCmd
// ---------------------------------------------------------------------------

func TestExternalMJPEGCmd(t *testing.T) {
	ctx := context.Background()
	scale := MJPEGScale{0, 0} // scale doesn't affect RTSP detection

	tests := []struct {
		name       string
		inputURL   string
		wantArgs   []string
		wantAbsent []string
	}{
		{
			name:     "rtsp:// adds rtsp_transport tcp and nobuffer args",
			inputURL: "rtsp://192.168.1.100/stream",
			wantArgs: []string{"-rtsp_transport", "tcp", "-fflags", "nobuffer"},
		},
		{
			name:       "http:// does not add rtsp_transport",
			inputURL:   "http://192.168.1.100/stream",
			wantAbsent: []string{"-rtsp_transport"},
		},
		{
			name:       "https:// does not add rtsp_transport",
			inputURL:   "https://192.168.1.100/stream",
			wantAbsent: []string{"-rtsp_transport"},
		},
		{
			name:     "RTSP:// uppercase is recognised",
			inputURL: "RTSP://192.168.1.100/stream",
			wantArgs: []string{"-rtsp_transport", "tcp"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := ExternalMJPEGCmd(ctx, tt.inputURL, scale)
			if cmd == nil {
				t.Fatal("ExternalMJPEGCmd returned nil")
			}
			args := cmd.Args

			for _, want := range tt.wantArgs {
				if !argsContain(args, want) {
					t.Errorf("expected arg %q in %v", want, args)
				}
			}
			for _, absent := range tt.wantAbsent {
				if argsContain(args, absent) {
					t.Errorf("unexpected arg %q in %v", absent, args)
				}
			}
		})
	}
}

func TestFFmpegHeadersArg(t *testing.T) {
	got := ffmpegHeadersArg(map[string]string{
		"Authorization": "Bearer secret",
		"User-Agent":    "ankerctl",
		"Bad:Header":    "ignored",
		"BadValue":      "ignored\r\nInjected: yes",
		"Empty":         "",
	})

	for _, want := range []string{
		"Authorization: Bearer secret\r\n",
		"User-Agent: ankerctl\r\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("ffmpegHeadersArg missing %q in %q", want, got)
		}
	}
	for _, bad := range []string{"Bad:Header", "Injected: yes", "Empty:"} {
		if strings.Contains(got, bad) {
			t.Fatalf("ffmpegHeadersArg included unsafe header %q in %q", bad, got)
		}
	}
}

// ---------------------------------------------------------------------------
// ReadMJPEGFrames
// ---------------------------------------------------------------------------

// makeJPEGData returns a minimal but structurally valid JPEG: SOI + payload + EOI.
func makeJPEGData(payload []byte) []byte {
	frame := make([]byte, 0, 2+len(payload)+2)
	frame = append(frame, 0xff, 0xd8) // SOI
	frame = append(frame, payload...)
	frame = append(frame, 0xff, 0xd9) // EOI
	return frame
}

func TestReadMJPEGFrames_NilCmd(t *testing.T) {
	ctx := context.Background()
	ch, err := ReadMJPEGFrames(ctx, nil)
	if err == nil {
		t.Error("expected error for nil cmd, got nil")
	}
	if ch != nil {
		t.Error("expected nil channel for nil cmd")
	}
}

func TestReadMJPEGFrames_TwoFrames(t *testing.T) {
	// Build a temp file containing two concatenated JPEG frames.
	frame1 := makeJPEGData([]byte("frame-one-payload"))
	frame2 := makeJPEGData([]byte("frame-two-payload"))
	data := append(frame1, frame2...)

	tmp, err := os.CreateTemp(t.TempDir(), "mjpeg_test_*.jpg")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	if _, err := tmp.Write(data); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	_ = tmp.Close()

	ctx := context.Background()
	cmd := exec.CommandContext(ctx, "cat", tmp.Name())

	frames, err := ReadMJPEGFrames(ctx, cmd)
	if err != nil {
		t.Fatalf("ReadMJPEGFrames: %v", err)
	}

	var got [][]byte
	for f := range frames {
		got = append(got, f)
	}

	if len(got) != 2 {
		t.Fatalf("expected 2 frames, got %d", len(got))
	}
	if len(got[0]) != len(frame1) {
		t.Errorf("frame[0] length: got %d, want %d", len(got[0]), len(frame1))
	}
	if len(got[1]) != len(frame2) {
		t.Errorf("frame[1] length: got %d, want %d", len(got[1]), len(frame2))
	}
}

func TestReadMJPEGFrames_CtxCancel(t *testing.T) {
	// Write a large number of frames so the reader goroutine keeps going.
	frame := makeJPEGData(make([]byte, 512))
	var data []byte
	for i := 0; i < 1000; i++ {
		data = append(data, frame...)
	}

	tmp, err := os.CreateTemp(t.TempDir(), "mjpeg_cancel_*.jpg")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	if _, err := tmp.Write(data); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	_ = tmp.Close()

	ctx, cancel := context.WithCancel(context.Background())

	cmd := exec.CommandContext(ctx, "cat", tmp.Name())
	frames, err := ReadMJPEGFrames(ctx, cmd)
	if err != nil {
		t.Fatalf("ReadMJPEGFrames: %v", err)
	}

	// Read one frame to confirm the pipeline is alive, then cancel.
	_, ok := <-frames
	if !ok {
		t.Fatal("channel closed before receiving any frame")
	}
	cancel()

	// After cancellation the channel must eventually close (no goroutine leak).
	for range frames {
		// drain; the loop exits when channel is closed
	}
	// If we reach here the goroutine exited — pass.
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// argsContain reports whether any element of args equals needle.
func argsContain(args []string, needle string) bool {
	for _, a := range args {
		if a == needle {
			return true
		}
	}
	return false
}
