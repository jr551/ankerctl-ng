package web

import (
	"regexp"
	"strings"
	"testing"
)

// TestDashboardJSContract guards the *structural* contract the dashboard JS in
// ankersrv.js depends on — beyond bare ids (covered by the page-id net), this
// asserts element TYPES and the class/data-attr hooks the live-data code reads.
// A reskin of tabs/home.html must keep all of these or the port breaks live
// updates even though the page still "renders".
func TestDashboardJSContract(t *testing.T) {
	ext := renderFullPage(t, "external")
	local := renderFullPage(t, "local")

	// matchTag reports whether an element with the given id is rendered using
	// the expected tag (e.g. id="player" must stay a <video>, not become a div).
	matchTag := func(html, tag, id string) bool {
		re := regexp.MustCompile(`(?s)<` + tag + `\b[^>]*\bid="` + regexp.QuoteMeta(id) + `"`)
		return re.MatchString(html)
	}
	count := func(html, substr string) int { return strings.Count(html, substr) }

	t.Run("element types preserved", func(t *testing.T) {
		checks := []struct {
			html, tag, id, why string
		}{
			{ext, "img", "external-camera-player", "JS swaps its src for live frames"},
			{ext, "img", "camera-rewind-image", "rewind buffer image"},
			{local, "video", "player", "jmuxer attaches H.264 to this <video>"},
			{ext, "canvas", "temp-chart", "Chart.js renders into this <canvas>"},
			{ext, "input", "set-nozzle-temp", "numeric setpoint input"},
			{ext, "input", "set-bed-temp", "numeric setpoint input"},
			{ext, "input", "step-dist-slider", "range slider"},
			{ext, "input", "camera-rewind-slider", "range slider"},
		}
		for _, c := range checks {
			if !matchTag(c.html, c.tag, c.id) {
				t.Errorf("#%s must be a <%s> (%s)", c.id, c.tag, c.why)
			}
		}
	})

	t.Run("linear progress bars stay inside .progress", func(t *testing.T) {
		// ankersrv.js sets .style.width on these; they must remain a bar in a track.
		for _, id := range []string{"progressbar", "upload-progressbar"} {
			if !matchTag(ext, "div", id) {
				t.Errorf("#%s must remain a <div> progress bar", id)
			}
		}
		if !strings.Contains(ext, `class="progress`) {
			t.Error("expected at least one .progress track container")
		}
	})

	t.Run("class + data-attr hooks present", func(t *testing.T) {
		// Debug/command feeds clear a .state-debug-line.muted placeholder.
		if count(ext, "state-debug-line muted") < 2 {
			t.Errorf("expected >=2 .state-debug-line.muted placeholders, got %d", count(ext, "state-debug-line muted"))
		}
		for _, cls := range []string{"rewind-status", "rewind-status-text", "rewind-dot"} {
			if !strings.Contains(ext, cls) {
				t.Errorf("missing rewind hook class %q", cls)
			}
		}
		// Preheat presets: 5 buttons, each carrying data-nozzle + data-bed.
		if n := count(ext, "preheat-preset"); n < 5 {
			t.Errorf("expected 5 .preheat-preset buttons, got %d", n)
		}
		if count(ext, "data-nozzle=") < 5 || count(ext, "data-bed=") < 5 {
			t.Error("preheat presets must keep data-nozzle and data-bed")
		}
		// Temp-graph window switcher: 4 buttons with data-window.
		if n := count(ext, "data-window="); n < 4 {
			t.Errorf("expected 4 data-window buttons, got %d", n)
		}
	})

	t.Run("video controls present in local mode", func(t *testing.T) {
		if !strings.Contains(local, "video-profile-btn") {
			t.Error("local camera mode must render .video-profile-btn quality buttons")
		}
		for _, id := range []string{"video-toggle", "light-on", "light-off", "snapshot-btn", "badge-video"} {
			if !strings.Contains(local, `id="`+id+`"`) {
				t.Errorf("local mode missing #%s", id)
			}
		}
	})

	t.Run("collapse sections present", func(t *testing.T) {
		for _, id := range []string{"temp-graph-section", "upload-progress-section"} {
			if !strings.Contains(ext, `id="`+id+`"`) {
				t.Errorf("missing collapse target #%s", id)
			}
		}
		if !strings.Contains(local, `id="video-controls-section"`) {
			t.Error("local mode missing collapse target #video-controls-section")
		}
	})
}
