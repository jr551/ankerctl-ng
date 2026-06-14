package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/django1982/ankerctl/internal/model"
)

const defaultHomeAssistantHTTPTimeout = 30 * time.Second

type HomeAssistantState struct {
	EntityID    string         `json:"entity_id"`
	State       string         `json:"state"`
	LastChanged time.Time      `json:"last_changed"`
	LastUpdated time.Time      `json:"last_updated"`
	Attributes  map[string]any `json:"attributes"`
}

type HomeAssistantClient struct {
	BaseURL string
	Token   string
	Client  *http.Client
}

func NewHomeAssistantClient(baseURL, token string) *HomeAssistantClient {
	return &HomeAssistantClient{
		BaseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		Token:   strings.TrimSpace(token),
		Client:  &http.Client{Timeout: defaultHomeAssistantHTTPTimeout},
	}
}

func (c *HomeAssistantClient) Valid() bool {
	return c != nil && c.BaseURL != "" && c.Token != ""
}

func (c *HomeAssistantClient) request(ctx context.Context, method, rel string, body io.Reader) (*http.Response, error) {
	if !c.Valid() {
		return nil, fmt.Errorf("homeassistant: base URL and token are required")
	}
	u, err := url.JoinPath(c.BaseURL, rel)
	if err != nil {
		return nil, fmt.Errorf("homeassistant: build URL: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("User-Agent", "ankerctl")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.Client.Do(req)
}

func (c *HomeAssistantClient) State(ctx context.Context, entityID string) (HomeAssistantState, error) {
	var state HomeAssistantState
	entityID = strings.TrimSpace(entityID)
	if entityID == "" {
		return state, fmt.Errorf("homeassistant: entity_id is required")
	}
	resp, err := c.request(ctx, http.MethodGet, "/api/states/"+entityID, nil)
	if err != nil {
		return state, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return state, fmt.Errorf("homeassistant: state %s returned HTTP %d", entityID, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&state); err != nil {
		return state, fmt.Errorf("homeassistant: decode state: %w", err)
	}
	return state, nil
}

func (c *HomeAssistantClient) CallService(ctx context.Context, domain, serviceName, entityID string) error {
	domain = strings.TrimSpace(domain)
	serviceName = strings.TrimSpace(serviceName)
	entityID = strings.TrimSpace(entityID)
	if domain == "" || serviceName == "" || entityID == "" {
		return fmt.Errorf("homeassistant: domain, service and entity_id are required")
	}
	payload := strings.NewReader(fmt.Sprintf(`{"entity_id":%q}`, entityID))
	resp, err := c.request(ctx, http.MethodPost, "/api/services/"+domain+"/"+serviceName, payload)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("homeassistant: service %s.%s returned HTTP %d", domain, serviceName, resp.StatusCode)
	}
	return nil
}

func (c *HomeAssistantClient) CallServiceData(ctx context.Context, domain, serviceName string, data any) error {
	domain = strings.TrimSpace(domain)
	serviceName = strings.TrimSpace(serviceName)
	if domain == "" || serviceName == "" {
		return fmt.Errorf("homeassistant: domain and service are required")
	}
	var body io.Reader
	if data != nil {
		raw, err := json.Marshal(data)
		if err != nil {
			return fmt.Errorf("homeassistant: encode service payload: %w", err)
		}
		body = bytes.NewReader(raw)
	}
	resp, err := c.request(ctx, http.MethodPost, "/api/services/"+domain+"/"+serviceName, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024*64))
		if trimmed := strings.TrimSpace(string(msg)); trimmed != "" {
			return fmt.Errorf("homeassistant: service %s.%s returned HTTP %d: %s", domain, serviceName, resp.StatusCode, trimmed)
		}
		return fmt.Errorf("homeassistant: service %s.%s returned HTTP %d", domain, serviceName, resp.StatusCode)
	}
	return nil
}

func (c *HomeAssistantClient) DownloadCameraSnapshot(ctx context.Context, entityID, outputPath string) error {
	entityID = strings.TrimSpace(entityID)
	if entityID == "" {
		return fmt.Errorf("homeassistant: camera entity is required")
	}
	resp, err := c.request(ctx, http.MethodGet, "/api/camera_proxy/"+entityID, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("homeassistant: camera snapshot returned HTTP %d", resp.StatusCode)
	}
	if dir := filepath.Dir(outputPath); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("homeassistant: create snapshot dir: %w", err)
		}
	}
	tmp := outputPath + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("homeassistant: create snapshot: %w", err)
	}
	_, copyErr := io.Copy(out, resp.Body)
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("homeassistant: write snapshot: %w", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("homeassistant: close snapshot: %w", closeErr)
	}
	return os.Rename(tmp, outputPath)
}

func HomeAssistantCameraConfigured(cfg model.HomeAssistantCameraSettings) bool {
	return cfg.Enabled && strings.TrimSpace(cfg.BaseURL) != "" && strings.TrimSpace(cfg.Token) != "" && strings.TrimSpace(cfg.CameraEntityID) != ""
}

func HomeAssistantCameraStreamURL(cfg model.HomeAssistantCameraSettings) string {
	if strings.TrimSpace(cfg.BaseURL) == "" || strings.TrimSpace(cfg.CameraEntityID) == "" {
		return ""
	}
	u, err := url.JoinPath(strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/"), "/api/camera_proxy_stream/"+strings.TrimSpace(cfg.CameraEntityID))
	if err != nil {
		return ""
	}
	return u
}

func HomeAssistantCameraSnapshot(ctx context.Context, cfg model.HomeAssistantCameraSettings, outputPath string) error {
	client := NewHomeAssistantClient(cfg.BaseURL, cfg.Token)
	return client.DownloadCameraSnapshot(ctx, cfg.CameraEntityID, outputPath)
}
