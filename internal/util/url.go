package util

import (
	"fmt"
	"net/url"
)

// validURLSchemes lists URL schemes accepted for external resource URLs
// (camera feeds, notification endpoints). file://, gopher://, etc. are blocked
// to prevent SSRF against internal services and local file disclosure.
var validURLSchemes = map[string]bool{
	"http":  true,
	"https": true,
	"rtsp":  true,
}

// ValidateExternalURL checks that raw is a well-formed URL with an allowed
// scheme. An empty string is treated as "not configured" and returns nil so
// callers can skip validation for optional fields. Use this at API/config
// boundaries before persisting or dialing user-supplied URLs.
func ValidateExternalURL(raw string) error {
	if raw == "" {
		return nil
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if parsed.Scheme == "" {
		return fmt.Errorf("URL scheme is required (use http, https, or rtsp)")
	}
	if !validURLSchemes[parsed.Scheme] {
		return fmt.Errorf("URL scheme %q not allowed (use http, https, or rtsp)", parsed.Scheme)
	}
	return nil
}
