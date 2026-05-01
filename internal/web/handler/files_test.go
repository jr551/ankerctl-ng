package handler

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/django1982/ankerctl/internal/service"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestValidateStoredFilePath(t *testing.T) {
	tests := []struct {
		name       string
		filePath   string
		source     string
		wantPath   string
		wantSource string
		wantErr    string
	}{
		{
			name:       "onboard path",
			filePath:   "/usr/data/local/model/benchy.gcode",
			wantPath:   "/usr/data/local/model/benchy.gcode",
			wantSource: "onboard",
		},
		{
			name:       "usb path",
			filePath:   "/tmp/udisk/benchy.gcode",
			wantPath:   "/tmp/udisk/benchy.gcode",
			wantSource: "usb",
		},
		{
			name:     "missing path",
			filePath: " ",
			wantErr:  "Stored file path is required",
		},
		{
			name:     "unsupported path",
			filePath: "/tmp/benchy.gcode",
			wantErr:  "Unsupported stored file path",
		},
		{
			name:     "source mismatch",
			filePath: "/tmp/udisk/benchy.gcode",
			source:   "onboard",
			wantErr:  "Stored file path does not match source 'onboard'",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotPath, gotSource, err := validateStoredFilePath(tc.filePath, tc.source)
			if tc.wantErr != "" {
				if err == nil || err.Error() != tc.wantErr {
					t.Fatalf("error = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("validateStoredFilePath: %v", err)
			}
			if gotPath != tc.wantPath {
				t.Fatalf("path = %q, want %q", gotPath, tc.wantPath)
			}
			if gotSource != tc.wantSource {
				t.Fatalf("source = %q, want %q", gotSource, tc.wantSource)
			}
		})
	}
}

func TestFetchPrinterFilePreview(t *testing.T) {
	origClient := printerFilePreviewHTTPClient
	t.Cleanup(func() {
		printerFilePreviewHTTPClient = origClient
	})
	printerFilePreviewHTTPClient = &http.Client{
		Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"image/png; charset=utf-8"}},
				Body:       io.NopCloser(strings.NewReader("pngbytes")),
			}, nil
		}),
	}

	data, contentType, err := fetchPrinterFilePreview("http://printer.local/preview.png")
	if err != nil {
		t.Fatalf("fetchPrinterFilePreview: %v", err)
	}
	if string(data) != "pngbytes" {
		t.Fatalf("data = %q, want pngbytes", string(data))
	}
	if contentType != "image/png" {
		t.Fatalf("contentType = %q, want image/png", contentType)
	}
}

func TestPrinterFilesList_InvalidSource(t *testing.T) {
	h := newTestHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/api/files/printer?source=sdcard", nil)
	rr := httptest.NewRecorder()

	h.PrinterFilesList(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
	if !strings.Contains(rr.Body.String(), "Invalid storage source") {
		t.Fatalf("body = %q", rr.Body.String())
	}
}

func TestPrinterFilesList_InvalidValue(t *testing.T) {
	h := newTestHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/api/files/printer?value=abc", nil)
	rr := httptest.NewRecorder()

	h.PrinterFilesList(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
	if !strings.Contains(rr.Body.String(), "value must be an integer") {
		t.Fatalf("body = %q", rr.Body.String())
	}
}

func TestPrinterFilesList_ServiceUnavailable(t *testing.T) {
	h := newTestHandler(t)
	h.svc = service.NewServiceManager()

	req := httptest.NewRequest(http.MethodGet, "/api/files/printer", nil)
	rr := httptest.NewRecorder()

	h.PrinterFilesList(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusServiceUnavailable)
	}
}

func TestPrinterFileThumbnail_MissingFilename(t *testing.T) {
	h := newTestHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/api/files/printer/thumbnail", nil)
	rr := httptest.NewRecorder()

	h.PrinterFileThumbnail(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
	if !strings.Contains(rr.Body.String(), "Stored file path is required") {
		t.Fatalf("body = %q", rr.Body.String())
	}
}

func TestPrinterFileThumbnail_InvalidPath(t *testing.T) {
	h := newTestHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/api/files/printer/thumbnail?filename=/tmp/benchy.gcode", nil)
	rr := httptest.NewRecorder()

	h.PrinterFileThumbnail(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
	if !strings.Contains(rr.Body.String(), "Unsupported stored file path") {
		t.Fatalf("body = %q", rr.Body.String())
	}
}

func TestPrinterFileThumbnail_ServiceUnavailable(t *testing.T) {
	h := newTestHandler(t)
	h.svc = service.NewServiceManager()

	req := httptest.NewRequest(http.MethodGet, "/api/files/printer/thumbnail?filename=/usr/data/local/model/benchy.gcode", nil)
	rr := httptest.NewRecorder()

	h.PrinterFileThumbnail(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusServiceUnavailable)
	}
}

func TestPrinterFilePrint_InvalidSource(t *testing.T) {
	h := newTestHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/api/files/printer/print", strings.NewReader(`{"source":"sdcard"}`))
	rr := httptest.NewRecorder()

	h.PrinterFilePrint(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
	if !strings.Contains(rr.Body.String(), "Invalid storage source") {
		t.Fatalf("body = %q", rr.Body.String())
	}
}

func TestPrinterFilePrint_MissingPath(t *testing.T) {
	h := newTestHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/api/files/printer/print", strings.NewReader(`{}`))
	rr := httptest.NewRecorder()

	h.PrinterFilePrint(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
	if !strings.Contains(rr.Body.String(), "Stored file path is required") {
		t.Fatalf("body = %q", rr.Body.String())
	}
}

func TestPrinterFilePrint_InvalidPath(t *testing.T) {
	h := newTestHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/api/files/printer/print", strings.NewReader(`{"path":"/tmp/benchy.gcode"}`))
	rr := httptest.NewRecorder()

	h.PrinterFilePrint(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
	if !strings.Contains(rr.Body.String(), "Unsupported stored file path") {
		t.Fatalf("body = %q", rr.Body.String())
	}
}

func TestPrinterFilePrint_ServiceUnavailable(t *testing.T) {
	h := newTestHandler(t)
	h.svc = service.NewServiceManager()

	req := httptest.NewRequest(http.MethodPost, "/api/files/printer/print", strings.NewReader(`{"path":"/usr/data/local/model/benchy.gcode"}`))
	rr := httptest.NewRecorder()

	h.PrinterFilePrint(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusServiceUnavailable)
	}
}
