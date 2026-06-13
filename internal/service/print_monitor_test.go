package service

import "testing"

func TestPrintMonitorChatCompletionsURL(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "keeps full chat completions URL",
			in:   "https://openrouter.ai/api/v1/chat/completions",
			want: "https://openrouter.ai/api/v1/chat/completions",
		},
		{
			name: "appends to gateway base URL",
			in:   "https://api.kilo.ai/api/gateway",
			want: "https://api.kilo.ai/api/gateway/chat/completions",
		},
		{
			name: "trims trailing slash",
			in:   "https://api.kilo.ai/api/gateway/",
			want: "https://api.kilo.ai/api/gateway/chat/completions",
		},
		{
			name: "leaves invalid URL unchanged",
			in:   "not a url",
			want: "not a url",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := printMonitorChatCompletionsURL(tt.in); got != tt.want {
				t.Fatalf("printMonitorChatCompletionsURL(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
