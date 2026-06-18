package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/django1982/ankerctl/internal/model"
)

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

func TestPrintMonitorCallOpenRouterStripsJSONCodeFence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Fatalf("path = %q, want /chat/completions", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"` + "```json\\n{\\\"failing\\\":true,\\\"confidence\\\":0.42,\\\"reason\\\":\\\"spaghetti\\\"}\\n```" + `"}}]}`))
	}))
	defer server.Close()

	svc := &PrintMonitorService{httpClient: server.Client()}
	cfg := model.PrintMonitorConfig{
		OpenRouterURL: server.URL,
		OpenRouterKey: "test-key",
		Model:         "test-model",
		Prompt:        "Return JSON",
	}

	failing, confidence, reason, raw, status, err := svc.callOpenRouter(context.Background(), cfg, []byte("jpeg"), nil, nil)
	if err != nil {
		t.Fatalf("callOpenRouter: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if !failing {
		t.Fatal("failing = false, want true")
	}
	if confidence != 0.42 {
		t.Fatalf("confidence = %v, want 0.42", confidence)
	}
	if reason != "spaghetti" {
		t.Fatalf("reason = %q, want spaghetti", reason)
	}
	if raw != `{"failing":true,"confidence":0.42,"reason":"spaghetti"}` {
		t.Fatalf("raw = %q", raw)
	}
}
