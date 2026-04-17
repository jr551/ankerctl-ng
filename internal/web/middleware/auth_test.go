package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

type authTestState struct {
	apiKey string
	login  bool
	sm     *SessionManager
}

func (s *authTestState) APIKey() string                 { return s.apiKey }
func (s *authTestState) IsLoggedIn() bool               { return s.login }
func (s *authTestState) SessionManager() *SessionManager { return s.sm }

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func TestAuth_NoAPIKey_AllowsPost(t *testing.T) {
	state := &authTestState{apiKey: "", login: true, sm: NewSessionManager([]byte("secret"))}
	h := Auth(state)(okHandler())

	r := httptest.NewRequest(http.MethodPost, "/api/printer/control", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestAuth_PostWithoutAuth_ReturnsUnauthorized(t *testing.T) {
	state := &authTestState{apiKey: "test-api-key-1234", login: true, sm: NewSessionManager([]byte("secret"))}
	h := Auth(state)(okHandler())

	r := httptest.NewRequest(http.MethodPost, "/api/printer/control", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestAuth_PostWithHeader_Allows(t *testing.T) {
	state := &authTestState{apiKey: "test-api-key-1234", login: true, sm: NewSessionManager([]byte("secret"))}
	h := Auth(state)(okHandler())

	r := httptest.NewRequest(http.MethodPost, "/api/printer/control", nil)
	r.Header.Set("X-Api-Key", "test-api-key-1234")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestAuth_GetOpenPath_WithoutAuth_Allows(t *testing.T) {
	state := &authTestState{apiKey: "test-api-key-1234", login: true, sm: NewSessionManager([]byte("secret"))}
	h := Auth(state)(okHandler())

	r := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestAuth_GetProtectedPath_WithoutAuth_Unauthorized(t *testing.T) {
	state := &authTestState{apiKey: "test-api-key-1234", login: true, sm: NewSessionManager([]byte("secret"))}
	h := Auth(state)(okHandler())

	r := httptest.NewRequest(http.MethodGet, "/api/history", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestAuth_DebugPrefix_RequiresAuth(t *testing.T) {
	state := &authTestState{apiKey: "test-api-key-1234", login: true, sm: NewSessionManager([]byte("secret"))}
	h := Auth(state)(okHandler())

	r := httptest.NewRequest(http.MethodGet, "/api/debug/state", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestAuth_SetupPathWithoutLogin_Allows(t *testing.T) {
	state := &authTestState{apiKey: "test-api-key-1234", login: false, sm: NewSessionManager([]byte("secret"))}
	h := Auth(state)(okHandler())

	r := httptest.NewRequest(http.MethodPost, "/api/ankerctl/config/login", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestAuth_SetupPathWithLogin_RequiresAuth(t *testing.T) {
	state := &authTestState{apiKey: "test-api-key-1234", login: true, sm: NewSessionManager([]byte("secret"))}
	h := Auth(state)(okHandler())

	r := httptest.NewRequest(http.MethodPost, "/api/ankerctl/config/login", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

func TestAuth_ApiKeyQuery_SetsCookieAndRedirects(t *testing.T) {
	state := &authTestState{apiKey: "test-api-key-1234", login: true, sm: NewSessionManager([]byte("secret"))}
	h := Auth(state)(okHandler())

	r := httptest.NewRequest(http.MethodGet, "/api/history?apikey=test-api-key-1234&foo=1", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusFound)
	}
	if loc := w.Header().Get("Location"); loc != "/api/history?foo=1" {
		t.Fatalf("location = %q, want %q", loc, "/api/history?foo=1")
	}
	if len(w.Result().Cookies()) == 0 {
		t.Fatal("expected session cookie to be set")
	}
}

func TestAuth_SessionCookie_AllowsProtectedPost(t *testing.T) {
	sm := NewSessionManager([]byte("secret"))
	state := &authTestState{apiKey: "test-api-key-1234", login: true, sm: sm}
	h := Auth(state)(okHandler())

	cookieWriter := httptest.NewRecorder()
	baseReq := httptest.NewRequest(http.MethodGet, "/", nil)
	sm.SetAuthenticated(cookieWriter, baseReq, true)
	cookies := cookieWriter.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("expected session cookie")
	}

	r := httptest.NewRequest(http.MethodPost, "/api/printer/control", nil)
	r.AddCookie(cookies[0])
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

func TestSecureEquals_DifferentLengths(t *testing.T) {
	// Verify that secureEquals correctly rejects different-length strings
	// without a length-based early return (timing side-channel).
	if secureEquals("short", "muchlongerkey1234") {
		t.Fatal("expected false for different-length inputs")
	}
	if secureEquals("muchlongerkey1234", "short") {
		t.Fatal("expected false for different-length inputs (reversed)")
	}
	if secureEquals("", "notempty") {
		t.Fatal("expected false for empty vs non-empty")
	}
}

func TestSecureEquals_SameContent(t *testing.T) {
	if !secureEquals("test-api-key-1234", "test-api-key-1234") {
		t.Fatal("expected true for identical strings")
	}
	if secureEquals("test-api-key-1234", "test-api-key-1235") {
		t.Fatal("expected false for different content")
	}
}

func TestAuth_StaticPath_AlwaysAllowed(t *testing.T) {
	state := &authTestState{apiKey: "test-api-key-1234", login: true, sm: NewSessionManager([]byte("secret"))}
	h := Auth(state)(okHandler())

	r := httptest.NewRequest(http.MethodGet, "/static/app.js", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusOK)
	}
}

// TestAuth_ProtectedGETPaths_TableDriven covers every entry in
// protectedGETPaths/protectedGETPrefixes to ensure parity with the Python
// _PROTECTED_GET_PATHS set. Each protected path must 401 without a key and 200
// with a valid X-Api-Key header.
func TestAuth_ProtectedGETPaths_TableDriven(t *testing.T) {
	const apiKey = "test-api-key-1234"

	cases := []struct {
		name string
		path string
	}{
		{"server_reload", "/api/ankerctl/server/reload"},
		{"console_logs", "/api/console/logs"},
		{"debug_state", "/api/debug/state"},
		{"debug_logs", "/api/debug/logs"},
		{"debug_services", "/api/debug/services"},
		{"debug_prefix_dynamic", "/api/debug/some/subpath"},
		{"camera_frame", "/api/camera/frame"},
		{"camera_stream", "/api/camera/stream"},
		{"snapshot", "/api/snapshot"},
		{"settings_mqtt", "/api/settings/mqtt"},
		{"settings_filament_service", "/api/settings/filament-service"},
		{"settings_filament_service_advanced", "/api/settings/filament-service/advanced"},
		{"settings_timelapse", "/api/settings/timelapse"},
		{"settings_camera", "/api/settings/camera"},
		{"notifications_settings", "/api/notifications/settings"},
		{"printers", "/api/printers"},
		{"printer_bed_leveling", "/api/printer/bed-leveling"},
		{"printer_bed_leveling_last", "/api/printer/bed-leveling/last"},
		{"printer_settings_summary", "/api/printer/settings-summary"},
		{"printer_z_offset", "/api/printer/z-offset"},
		{"filaments", "/api/filaments"},
		{"filaments_service_swap", "/api/filaments/service/swap"},
		{"history", "/api/history"},
		{"timelapses", "/api/timelapses"},
		{"timelapse_snapshots", "/api/timelapse-snapshots"},
		{"timelapse_prefix_dynamic", "/api/timelapse/123"},
		{"timelapse_snapshot_prefix_dynamic", "/api/timelapse-snapshot/abc.jpg"},
	}

	for _, tc := range cases {
		t.Run(tc.name+"_denies_without_auth", func(t *testing.T) {
			state := &authTestState{apiKey: apiKey, login: true, sm: NewSessionManager([]byte("secret"))}
			h := Auth(state)(okHandler())

			r := httptest.NewRequest(http.MethodGet, tc.path, nil)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)

			if w.Code != http.StatusUnauthorized {
				t.Fatalf("GET %s: status = %d, want %d", tc.path, w.Code, http.StatusUnauthorized)
			}
		})

		t.Run(tc.name+"_allows_with_apikey_header", func(t *testing.T) {
			state := &authTestState{apiKey: apiKey, login: true, sm: NewSessionManager([]byte("secret"))}
			h := Auth(state)(okHandler())

			r := httptest.NewRequest(http.MethodGet, tc.path, nil)
			r.Header.Set("X-Api-Key", apiKey)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)

			if w.Code != http.StatusOK {
				t.Fatalf("GET %s with key: status = %d, want %d", tc.path, w.Code, http.StatusOK)
			}
		})
	}
}

// TestAuth_OpenGETPaths_TableDriven verifies that paths NOT in the protected
// set remain reachable without auth (regression guard against over-tightening).
func TestAuth_OpenGETPaths_TableDriven(t *testing.T) {
	const apiKey = "test-api-key-1234"

	cases := []struct {
		name string
		path string
	}{
		{"health", "/api/health"},
		{"config_upload_get", "/api/ankerctl/config/upload"},
		{"config_login_get", "/api/ankerctl/config/login"},
		{"root", "/"},
		{"static_asset", "/static/app.js"},
		// Adjacent but distinct path — must NOT be caught by a naive prefix match.
		{"printer_root_not_a_match", "/api/printer/status"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state := &authTestState{apiKey: apiKey, login: true, sm: NewSessionManager([]byte("secret"))}
			h := Auth(state)(okHandler())

			r := httptest.NewRequest(http.MethodGet, tc.path, nil)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)

			if w.Code != http.StatusOK {
				t.Fatalf("GET %s: status = %d, want %d", tc.path, w.Code, http.StatusOK)
			}
		})
	}
}
