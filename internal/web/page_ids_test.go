package web

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/django1982/ankerctl/internal/model"
	"github.com/django1982/ankerctl/internal/web/handler"
)

// idRe matches HTML element id attributes in the rendered templates.
var idRe = regexp.MustCompile(`\bid="([A-Za-z0-9_:.\-]+)"`)

// renderFullPage renders the real production page ("base.html", which pulls in
// every tab via index.html's "contents" block) with a fixed, fully-populated
// TemplateData so the template executes every branch without error.
func renderFullPage(t *testing.T, cameraSource string) string {
	t.Helper()

	tmpls, err := newTemplates()
	if err != nil {
		t.Fatalf("newTemplates: %v", err)
	}

	pr := &model.Printer{Name: "Test M5C", SN: "ABCDEQXY12", Model: "M5C", IPAddr: "10.0.0.9"}
	data := handler.TemplateData{
		Printers:              []model.Printer{*pr},
		PrinterList:           []handler.PrinterSummary{{Index: 0, Name: pr.Name, SN: pr.SN, Model: pr.Model, IPAddr: pr.IPAddr, Supported: true}},
		ActivePrinterIndex:    0,
		Printer:               pr,
		VideoSupported:        true,
		CameraEffectiveSource: cameraSource,
		CameraRefreshSec:      2,
		Configure:             true,
		DebugMode:             true,
		VideoProfiles:         []handler.VideoProfile{{ID: "sd", Label: "SD", Live: true}, {ID: "hd", Label: "HD", Live: true}},
		VideoProfileDefault:   "hd",
		RequestHost:           "localhost",
		RequestPort:           "4470",
		ConfigExistingEmail:   "user@example.com",
		CountryCodes:          "US,GB,DE",
		CurrentCountry:        "GB",
		LoginFilePath:         "/tmp/ankerctl",
		AnkerConfig:           "Account:\n  user_id: redacted",
		UploadRateChoices:     model.UploadRateMbpsChoices,
		UploadRateMbps:        10,
		UploadRateConfig:      10,
		UploadRateSource:      "config",
		AccentColor:           "#88f387",
	}

	var buf bytes.Buffer
	if err := tmpls.Render(&buf, "base.html", data); err != nil {
		t.Fatalf("render base.html (camera=%q): %v", cameraSource, err)
	}
	return buf.String()
}

// pageIDs returns the union of element ids present across both camera branches
// (external still-image vs. local H.264 video), so branch-exclusive hooks such
// as #external-camera-player and #player/#video-toggle are both captured.
func pageIDs(t *testing.T) []string {
	t.Helper()
	set := map[string]struct{}{}
	for _, src := range []string{"external", "local"} {
		html := renderFullPage(t, src)
		for _, m := range idRe.FindAllStringSubmatch(html, -1) {
			set[m[1]] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// TestPageElementIDsPreserved is the redesign safety net: it guards every
// element id the JS layer (ankersrv.js / ankersrv-slice.js) binds to. The
// golden list is the set of ids the page rendered before the UI redesign.
// A port that drops any id — silently breaking a live-data hook — fails here.
// Adding new ids is fine; removing a golden id is not.
//
// Regenerate intentionally with:  REGEN_PAGE_IDS=1 go test ./internal/web/ -run TestPageElementIDsPreserved
func TestPageElementIDsPreserved(t *testing.T) {
	ids := pageIDs(t)

	goldenPath := filepath.Join("testdata", "page_ids.golden")

	if os.Getenv("REGEN_PAGE_IDS") == "1" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(goldenPath, []byte(strings.Join(ids, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("regenerated %s with %d ids", goldenPath, len(ids))
		return
	}

	raw, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden (run with REGEN_PAGE_IDS=1 to create it): %v", err)
	}
	present := map[string]struct{}{}
	for _, id := range ids {
		present[id] = struct{}{}
	}
	var missing []string
	for _, want := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		want = strings.TrimSpace(want)
		if want == "" {
			continue
		}
		if _, ok := present[want]; !ok {
			missing = append(missing, want)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("the redesign dropped %d element id(s) the JS layer depends on:\n  %s\n\nIf a removal is truly intentional, regenerate with REGEN_PAGE_IDS=1.",
			len(missing), strings.Join(missing, "\n  "))
	}
	t.Logf("all %d golden element ids still present (%d ids in current render)", len(strings.Split(strings.TrimSpace(string(raw)), "\n")), len(ids))
}
