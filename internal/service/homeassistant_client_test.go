package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/django1982/ankerctl/internal/model"
)

func TestHomeAssistantCameraSnapshotFallsBackToStreamProxy(t *testing.T) {
	tmpDir := t.TempDir()
	argsPath := filepath.Join(tmpDir, "ffmpeg.args")
	ffmpegPath := filepath.Join(tmpDir, "ffmpeg")
	script := `#!/bin/sh
: > "$FFMPEG_ARGS_FILE"
last=""
for arg in "$@"; do
  printf '%s\n' "$arg" >> "$FFMPEG_ARGS_FILE"
  last="$arg"
done
printf '\377\330\377\331' > "$last"
`
	if err := os.WriteFile(ffmpegPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake ffmpeg: %v", err)
	}
	t.Setenv("PATH", tmpDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FFMPEG_ARGS_FILE", argsPath)

	var snapshotRequests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/camera_proxy/camera.test" {
			t.Errorf("unexpected HA request path %q", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		snapshotRequests++
		if got, want := r.Header.Get("Authorization"), "Bearer secret-token"; got != want {
			t.Errorf("Authorization header = %q, want %q", got, want)
		}
		http.Error(w, "upstream camera unavailable", http.StatusBadGateway)
	}))
	defer srv.Close()

	outPath := filepath.Join(tmpDir, "snapshot.jpg")
	err := HomeAssistantCameraSnapshot(context.Background(), model.HomeAssistantCameraSettings{
		Enabled:        true,
		BaseURL:        srv.URL,
		Token:          "secret-token",
		CameraEntityID: "camera.test",
	}, outPath)
	if err != nil {
		t.Fatalf("HomeAssistantCameraSnapshot: %v", err)
	}
	if snapshotRequests != 1 {
		t.Fatalf("snapshotRequests = %d, want 1", snapshotRequests)
	}
	if data, err := os.ReadFile(outPath); err != nil {
		t.Fatalf("read fallback snapshot: %v", err)
	} else if len(data) != 4 {
		t.Fatalf("fallback snapshot length = %d, want 4", len(data))
	}

	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("read fake ffmpeg args: %v", err)
	}
	argsText := string(args)
	for _, want := range []string{
		"/api/camera_proxy_stream/camera.test",
		"Authorization: Bearer secret-token",
		"User-Agent: ankerctl",
	} {
		if !strings.Contains(argsText, want) {
			t.Fatalf("fake ffmpeg args missing %q in %q", want, argsText)
		}
	}
}
