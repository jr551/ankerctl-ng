package util

import "testing"

func TestValidateExternalURL(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		// Accepted schemes.
		{"http", "http://example.com", false},
		{"https", "https://example.com/path?x=1", false},
		{"rtsp", "rtsp://camera.local:554/live", false},
		{"http with credentials", "http://user:pass@example.com", false},
		{"empty string is allowed (optional field)", "", false},

		// Rejected schemes (SSRF / local-file exposure vectors).
		{"file scheme blocked", "file:///etc/passwd", true},
		{"gopher scheme blocked", "gopher://internal:70/_", true},
		{"ftp scheme blocked", "ftp://example.com", true},
		{"data scheme blocked", "data:text/plain,hello", true},
		{"dict scheme blocked", "dict://localhost:11211/", true},
		{"ldap scheme blocked", "ldap://internal.corp/", true},
		{"javascript scheme blocked", "javascript:alert(1)", true},

		// Malformed / schemeless.
		{"missing scheme", "example.com/path", true},
		{"bare hostname", "localhost", true},
		// url.Parse is very permissive; control chars and raw spaces are rejected.
		{"control character in URL", "http://example.com/\x7f\x00", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateExternalURL(tt.input)
			if tt.wantErr && err == nil {
				t.Errorf("ValidateExternalURL(%q) = nil, want error", tt.input)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("ValidateExternalURL(%q) = %v, want nil", tt.input, err)
			}
		})
	}
}

func TestValidateExternalURL_SchemeCaseSensitive(t *testing.T) {
	// net/url lowercases schemes during parsing, so uppercase should work too.
	if err := ValidateExternalURL("HTTPS://example.com"); err != nil {
		t.Errorf("expected HTTPS (uppercased) to be accepted, got %v", err)
	}
}
