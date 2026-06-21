package web

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDumpRenderedPage is a dev helper, not an assertion: when DUMP_PAGE=<path>
// is set it writes the fully-rendered production page (base.html → all tabs) to
// that path, with /static/ rewritten to an absolute file:// URL so a headless
// browser can render it for a visual screenshot. Camera source is selectable
// with DUMP_CAMERA (external|local, default external).
//
//	DUMP_PAGE=/tmp/page.html go test ./internal/web/ -run TestDumpRenderedPage
func TestDumpRenderedPage(t *testing.T) {
	out := os.Getenv("DUMP_PAGE")
	if out == "" {
		t.Skip("set DUMP_PAGE=<path> to dump the rendered page")
	}
	cam := os.Getenv("DUMP_CAMERA")
	if cam == "" {
		cam = "external"
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	staticURL := "file://" + filepath.Join(wd, "static") + "/"

	html := renderFullPage(t, cam)
	html = strings.ReplaceAll(html, "/static/", staticURL)
	if err := os.WriteFile(out, []byte(html), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %d bytes to %s (static -> %s)", len(html), out, staticURL)
}
