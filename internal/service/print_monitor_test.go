package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
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

	verdict, err := svc.callOpenRouter(context.Background(), cfg, []byte("jpeg"), nil, nil)
	if err != nil {
		t.Fatalf("callOpenRouter: %v", err)
	}
	if verdict.HTTPStatus != http.StatusOK {
		t.Fatalf("status = %d, want %d", verdict.HTTPStatus, http.StatusOK)
	}
	if !verdict.Failing {
		t.Fatal("failing = false, want true")
	}
	if verdict.Confidence != 0.42 {
		t.Fatalf("confidence = %v, want 0.42", verdict.Confidence)
	}
	if verdict.Reason != "spaghetti" {
		t.Fatalf("reason = %q, want spaghetti", verdict.Reason)
	}
	if verdict.Raw != `{"failing":true,"confidence":0.42,"reason":"spaghetti"}` {
		t.Fatalf("raw = %q", verdict.Raw)
	}
}

func TestPrintMonitorCallOpenRouterParsesAnimal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"failing\":false,\"confidence\":0.9,\"reason\":\"looks fine\",\"animal_detected\":true,\"animal\":\"cat\"}"}}]}`))
	}))
	defer server.Close()

	svc := &PrintMonitorService{httpClient: server.Client()}
	cfg := model.PrintMonitorConfig{
		OpenRouterURL:         server.URL,
		OpenRouterKey:         "test-key",
		Model:                 "test-model",
		Prompt:                "Return JSON",
		EmergencyStopOnAnimal: true,
	}

	verdict, err := svc.callOpenRouter(context.Background(), cfg, []byte("jpeg"), nil, nil)
	if err != nil {
		t.Fatalf("callOpenRouter: %v", err)
	}
	if !verdict.AnimalDetected {
		t.Fatal("animal_detected = false, want true")
	}
	if verdict.Animal != "cat" {
		t.Fatalf("animal = %q, want cat", verdict.Animal)
	}
	if verdict.Failing {
		t.Fatal("failing = true, want false")
	}
}

type fakeAnimalNotifier struct {
	calls       int
	lastPayload map[string]any
	lastAttach  []string
}

func (f *fakeAnimalNotifier) NotifyAnimalEmergencyStop(_ context.Context, payload map[string]any, attachments []string) {
	f.calls++
	f.lastPayload = payload
	f.lastAttach = attachments
}

// With no smart socket configured (nil cfgMgr) the emergency stop cannot cut
// power, but it must still alert the user with the camera frame attached.
func TestPrintMonitorEmergencyStopForAnimalNotifies(t *testing.T) {
	notifier := &fakeAnimalNotifier{}
	svc := &PrintMonitorService{notifier: notifier}

	svc.emergencyStopForAnimal(context.Background(), PrintMonitorResult{
		AnimalDetected: true,
		Animal:         "cat",
		ContactSheet:   "data:image/jpeg;base64,AAAA",
		Filename:       "kitten.gcode",
	})

	if notifier.calls != 1 {
		t.Fatalf("notifier calls = %d, want 1", notifier.calls)
	}
	if len(notifier.lastAttach) != 1 || notifier.lastAttach[0] != "data:image/jpeg;base64,AAAA" {
		t.Fatalf("attachment = %v, want the contact sheet data URI", notifier.lastAttach)
	}
	reason, _ := notifier.lastPayload["reason"].(string)
	if !strings.Contains(reason, "cat") {
		t.Fatalf("reason = %q, want it to name the animal", reason)
	}
}
