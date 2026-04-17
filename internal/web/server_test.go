package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	mw "github.com/django1982/ankerctl/internal/web/middleware"
	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
)

func TestRegisterRoutes_HealthEndpoint(t *testing.T) {
	s := NewServer(nil)
	s.router = chi.NewRouter()
	s.registerRoutes()

	r := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
	if got := w.Body.String(); got != "{\"status\":\"ok\"}\n" {
		t.Fatalf("body = %q, want %q", got, "{\"status\":\"ok\"}\\n")
	}
}

func TestRegisterRoutes_VersionEndpoint(t *testing.T) {
	s := NewServer(nil)
	s.router = chi.NewRouter()
	s.registerRoutes()

	r := httptest.NewRequest(http.MethodGet, "/api/version", nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

// TestRecoverer_NoStackTraceInResponse verifies that chimw.Recoverer does not
// write panic details or stack traces into the HTTP response body (CodeQL #25 /
// CWE-209). On panic the response must be exactly HTTP 500 with an empty body —
// all diagnostic output goes to stderr, never to the client.
func TestRecoverer_NoStackTraceInResponse(t *testing.T) {
	r := chi.NewRouter()
	r.Use(chimw.Recoverer)
	r.Get("/panic", func(w http.ResponseWriter, req *http.Request) {
		panic("intentional test panic: internal state exposed")
	})

	req := httptest.NewRequest(http.MethodGet, "/panic", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", w.Code, http.StatusInternalServerError)
	}

	body := w.Body.String()
	// These substrings would indicate a stack trace or panic message leaking
	// into the response body — any of them present is a security defect.
	leakIndicators := []string{
		"goroutine",
		"panic",
		"runtime/debug",
		".go:",
		"internal state exposed",
	}
	for _, indicator := range leakIndicators {
		if strings.Contains(body, indicator) {
			t.Errorf("response body leaks internal detail %q: body = %q", indicator, body)
		}
	}
}

func TestMiddlewareOrder_DeviceChecksBeforeAuth(t *testing.T) {
	s := NewServer(nil, WithAPIKey("test-api-key-1234"))
	s.login = false
	s.sessionManager = mw.NewSessionManager([]byte("secret"))
	s.router = chi.NewRouter()
	s.router.Use(chimw.Recoverer)
	s.router.Use(chimw.RequestID)
	s.router.Use(mw.SecurityHeaders)
	s.router.Use(mw.RequirePrinter(s))
	s.router.Use(mw.BlockUnsupportedDevice(s))
	s.router.Use(mw.Auth(s))
	s.registerRoutes()

	r := httptest.NewRequest(http.MethodPost, "/api/printer/control", nil)
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, r)

	// Must be 503 from RequirePrinter, not 401 from auth middleware.
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusServiceUnavailable)
	}
}
