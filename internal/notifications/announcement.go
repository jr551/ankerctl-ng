package notifications

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/django1982/ankerctl/internal/model"
	"github.com/django1982/ankerctl/internal/service"
)

func SendHomeAnnouncement(ctx context.Context, cfg model.HomeAnnouncementConfig, event string, payload map[string]any) DeliveryResult {
	result := DeliveryResult{
		At:        time.Now(),
		Event:     event,
		Transport: "ha_tts",
	}
	if !cfg.Enabled {
		result.Message = "Home announcement is disabled"
		return result
	}
	if !announcementEventEnabled(cfg, event) {
		result.Message = fmt.Sprintf("Announcement event disabled: %s", event)
		return result
	}
	baseURL := strings.TrimSpace(cfg.BaseURL)
	token := strings.TrimSpace(cfg.Token)
	ttsEntityID := strings.TrimSpace(cfg.TTSEntityID)
	mediaPlayerEntityID := strings.TrimSpace(cfg.MediaPlayerEntityID)
	if baseURL == "" || token == "" || ttsEntityID == "" || mediaPlayerEntityID == "" {
		result.Message = "Home announcement configuration is incomplete"
		return result
	}
	renderPayload := clonePayloadMap(payload)
	renderPayload["event"] = event
	renderPayload["title"] = EventTitle(event)
	renderPayload["type"] = EventType(event)
	if _, ok := renderPayload["body"]; !ok {
		renderPayload["body"] = RenderTemplate(DefaultTemplateForEvent(event), payload)
	}
	message := strings.TrimSpace(RenderTemplate(cfg.Template, renderPayload))
	if message == "" {
		result.Message = "Announcement message is empty"
		return result
	}

	client := service.NewHomeAssistantClient(baseURL, token)
	data := map[string]any{
		"entity_id":              ttsEntityID,
		"media_player_entity_id": mediaPlayerEntityID,
		"message":                message,
		"cache":                  cfg.Cache,
	}
	if lang := strings.TrimSpace(cfg.Language); lang != "" {
		data["language"] = lang
	}
	if err := client.CallServiceData(ctx, "tts", "speak", data); err != nil {
		result.Message = err.Error()
		result.Target = baseURL
		return result
	}
	result.OK = true
	result.Message = "Home announcement sent"
	result.Target = baseURL
	result.Title = EventTitle(event)
	return result
}

func announcementEventEnabled(cfg model.HomeAnnouncementConfig, event string) bool {
	switch event {
	case EventPrintStarted:
		return cfg.Events.PrintStarted
	case EventPrintFinished:
		return cfg.Events.PrintFinished
	case EventPrintFailed:
		return cfg.Events.PrintFailed
	case EventPrintPaused:
		return cfg.Events.PrintPaused
	case EventPrintResumed:
		return cfg.Events.PrintResumed
	case EventGCodeUploaded:
		return cfg.Events.GcodeUploaded
	case EventPrintProgress:
		return cfg.Events.PrintProgress
	default:
		return false
	}
}
