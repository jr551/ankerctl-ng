package middleware

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"net/url"
	"strings"
)

var protectedGETPaths = map[string]bool{
	"/api/ankerctl/server/reload":             true,
	"/api/console/logs":                       true,
	"/api/debug/state":                        true,
	"/api/debug/logs":                         true,
	"/api/debug/services":                     true,
	"/api/camera/frame":                       true,
	"/api/camera/stream":                      true,
	"/api/snapshot":                           true,
	"/api/settings/mqtt":                      true,
	"/api/settings/filament-service":          true,
	"/api/settings/filament-service/advanced": true,
	"/api/settings/timelapse":                 true,
	"/api/settings/camera":                    true,
	"/api/notifications/settings":             true,
	"/api/printers":                           true,
	"/api/printer/bed-leveling":               true,
	"/api/printer/bed-leveling/last":          true,
	"/api/printer/settings-summary":           true,
	"/api/printer/z-offset":                   true,
	"/api/filaments":                          true,
	"/api/filaments/service/swap":             true,
	"/api/history":                            true,
	"/api/timelapses":                         true,
	"/api/timelapse-snapshots":                true,
}

// protectedGETPrefixes covers dynamic path segments that require auth on all sub-paths.
var protectedGETPrefixes = []string{
	"/api/debug/",
	"/api/timelapse/",
	"/api/timelapse-snapshot/",
}

var setupPaths = map[string]bool{
	"/api/ankerctl/config/upload": true,
	"/api/ankerctl/config/login":  true,
}

// AuthState provides server runtime state required by auth middleware.
type AuthState interface {
	APIKey() string
	IsLoggedIn() bool
	SessionManager() *SessionManager
}

// Auth enforces API key and session-based auth with Python-compatible rules.
func Auth(state AuthState) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			apiKey := state.APIKey()
			if apiKey == "" {
				next.ServeHTTP(w, r)
				return
			}

			if strings.HasPrefix(r.URL.Path, "/static/") {
				next.ServeHTTP(w, r)
				return
			}

			if keyFromQuery := r.URL.Query().Get("apikey"); keyFromQuery != "" {
				if secureEquals(keyFromQuery, apiKey) {
					sm := state.SessionManager()
					if sm != nil {
						sm.SetAuthenticated(w, r, true)
					}
					http.Redirect(w, r, stripAPIKeyParam(r.URL), http.StatusFound)
					return
				}
			}

			if secureEquals(r.Header.Get("X-Api-Key"), apiKey) {
				next.ServeHTTP(w, r)
				return
			}

			if isPublicMethod(r.Method) && !isProtectedGETPath(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}

			if setupPaths[r.URL.Path] && !state.IsLoggedIn() {
				next.ServeHTTP(w, r)
				return
			}

			sm := state.SessionManager()
			if sm != nil && sm.IsAuthenticated(r) {
				next.ServeHTTP(w, r)
				return
			}

			writeJSONError(w, http.StatusUnauthorized, "Unauthorized. Provide API key via X-Api-Key header or ?apikey=...")
		})
	}
}

func isPublicMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
}

func isProtectedGETPath(path string) bool {
	if protectedGETPaths[path] {
		return true
	}
	for _, prefix := range protectedGETPrefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

func stripAPIKeyParam(u *url.URL) string {
	copyURL := *u
	q := copyURL.Query()
	q.Del("apikey")
	copyURL.RawQuery = q.Encode()
	if copyURL.RawQuery == "" {
		return copyURL.Path
	}
	return copyURL.Path + "?" + copyURL.RawQuery
}

func secureEquals(a, b string) bool {
	// Hash both strings with SHA-256 to ensure they have the same length (32 bytes).
	// This prevents subtle.ConstantTimeCompare from leaking the length of the string via timing,
	// because it returns immediately if lengths differ.
	hashA := sha256.Sum256([]byte(a))
	hashB := sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(hashA[:], hashB[:]) == 1
}
