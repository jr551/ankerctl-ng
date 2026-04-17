package gcode

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
)

// minimalPNG is a 1×1 transparent PNG (smallest valid PNG, widely known test vector).
// Used as a stand-in for real slicer thumbnails in tests.
// Python reference: test_cli_util.py iVBORw0KGgo… strings (also 1×1 PNGs).
var minimalPNGa = mustDecodeBase64("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+a5ZQAAAAASUVORK5CYII=")
var minimalPNGb = mustDecodeBase64("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAusB9WfKJeYAAAAASUVORK5CYII=")

func mustDecodeBase64(s string) []byte {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		panic("bad test vector: " + err.Error())
	}
	return b
}


func TestExtractThumbnail_prefers_largest(t *testing.T) {
	// Matches Python: test_extract_gcode_thumbnail_reads_embedded_png_and_prefers_largest
	smallB64 := base64.StdEncoding.EncodeToString(minimalPNGa)
	largeB64 := base64.StdEncoding.EncodeToString(minimalPNGb)

	gcode := "; thumbnail begin 32x32 10\n" +
		"; " + smallB64 + "\n" +
		"; thumbnail end\n" +
		"; thumbnail begin 256x256 10\n" +
		"; " + largeB64 + "\n" +
		"; thumbnail end\n"

	thumb, err := ExtractThumbnail([]byte(gcode))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if thumb == nil {
		t.Fatal("expected a thumbnail, got nil")
	}
	// Must be the large one (256x256 area > 32x32)
	if !bytes.Equal(thumb, minimalPNGb) {
		t.Errorf("expected large thumbnail, got %d bytes", len(thumb))
	}
	// Basic PNG magic-number check
	if !bytes.HasPrefix(thumb, []byte("\x89PNG\r\n\x1a\n")) {
		t.Errorf("thumbnail does not start with PNG magic bytes")
	}
}

func TestExtractThumbnail_no_thumbnail_returns_nil(t *testing.T) {
	// Matches Python: test_extract_gcode_thumbnail_returns_none_without_embedded_preview
	thumb, err := ExtractThumbnail([]byte("G28\nM104 S200\n"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if thumb != nil {
		t.Errorf("expected nil, got %d bytes", len(thumb))
	}
}

func TestExtractThumbnail_empty_input(t *testing.T) {
	thumb, err := ExtractThumbnail(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if thumb != nil {
		t.Errorf("expected nil for empty input")
	}
}

func TestExtractThumbnail_single_block(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString(minimalPNGa)
	gcode := "; thumbnail begin 64x64 68\n" +
		"; " + encoded + "\n" +
		"; thumbnail end\n" +
		"G28\nM104 S200\n"

	thumb, err := ExtractThumbnail([]byte(gcode))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if thumb == nil {
		t.Fatal("expected thumbnail, got nil")
	}
	if !bytes.Equal(thumb, minimalPNGa) {
		t.Errorf("thumbnail bytes mismatch: got len=%d, want len=%d", len(thumb), len(minimalPNGa))
	}
}

func TestExtractThumbnail_png_keyword_variant(t *testing.T) {
	// Cura uses "; png begin WxH" instead of "; thumbnail begin WxH"
	encoded := base64.StdEncoding.EncodeToString(minimalPNGa)
	gcode := "; png begin 128x128 68\n" +
		"; " + encoded + "\n" +
		"; png end\n"

	thumb, err := ExtractThumbnail([]byte(gcode))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if thumb == nil {
		t.Fatal("expected thumbnail for 'png begin' variant, got nil")
	}
	if !bytes.Equal(thumb, minimalPNGa) {
		t.Errorf("thumbnail bytes mismatch")
	}
}

func TestExtractThumbnail_star_separator(t *testing.T) {
	// Some slicers use '*' instead of 'x' as dimension separator
	encoded := base64.StdEncoding.EncodeToString(minimalPNGa)
	gcode := "; thumbnail begin 64*64 68\n" +
		"; " + encoded + "\n" +
		"; thumbnail end\n"

	thumb, err := ExtractThumbnail([]byte(gcode))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if thumb == nil {
		t.Fatal("expected thumbnail for '*' separator variant, got nil")
	}
}

func TestExtractThumbnail_multi_line_base64(t *testing.T) {
	// Real slicer output splits base64 across many comment lines.
	// Verify that multi-line blocks are correctly concatenated before decode.
	raw := minimalPNGa
	encoded := base64.StdEncoding.EncodeToString(raw)

	// Split encoded string into 10-char chunks
	var sb strings.Builder
	sb.WriteString("; thumbnail begin 32x32 10\n")
	for i := 0; i < len(encoded); i += 10 {
		end := i + 10
		if end > len(encoded) {
			end = len(encoded)
		}
		sb.WriteString("; ")
		sb.WriteString(encoded[i:end])
		sb.WriteString("\n")
	}
	sb.WriteString("; thumbnail end\n")

	thumb, err := ExtractThumbnail([]byte(sb.String()))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if thumb == nil {
		t.Fatal("expected thumbnail for multi-line block")
	}
	if !bytes.Equal(thumb, raw) {
		t.Errorf("multi-line: thumbnail bytes mismatch (got %d bytes)", len(thumb))
	}
}

func TestExtractThumbnail_invalid_base64_skipped(t *testing.T) {
	// A block with non-base64 garbage should be skipped, not returned.
	gcode := "; thumbnail begin 32x32 10\n" +
		"; not-valid-base64!!!@@@\n" +
		"; thumbnail end\n"

	thumb, err := ExtractThumbnail([]byte(gcode))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if thumb != nil {
		t.Errorf("expected nil for invalid base64 block, got %d bytes", len(thumb))
	}
}

func TestExtractThumbnail_empty_block_skipped(t *testing.T) {
	// An empty block (no data lines between begin/end) should return nil.
	gcode := "; thumbnail begin 32x32 10\n" +
		"; thumbnail end\n"

	thumb, err := ExtractThumbnail([]byte(gcode))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if thumb != nil {
		t.Errorf("expected nil for empty block")
	}
}
