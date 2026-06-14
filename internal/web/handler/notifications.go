package handler

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/django1982/ankerctl/internal/model"
	"github.com/django1982/ankerctl/internal/notifications"
)

// NotificationsGet returns notification settings.
func (h *Handler) NotificationsGet(w http.ResponseWriter, _ *http.Request) {
	cfg, _ := h.loadConfig()
	var apprise model.AppriseConfig
	var announcement model.HomeAnnouncementConfig
	if cfg != nil {
		apprise = cfg.Notifications.Apprise
		announcement = cfg.Notifications.Announcement
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"apprise": apprise, "announcement": announcement})
}

// NotificationsUpdate updates notification settings.
func (h *Handler) NotificationsUpdate(w http.ResponseWriter, r *http.Request) {
	var payload map[string]any
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		h.writeError(w, http.StatusBadRequest, "Invalid JSON payload")
		return
	}
	apprisePayload := payload
	var announcementPayload map[string]any
	if raw, ok := payload["apprise"]; ok {
		m, ok := raw.(map[string]any)
		if !ok {
			h.writeError(w, http.StatusBadRequest, "Invalid apprise payload")
			return
		}
		apprisePayload = m
	}
	if raw, ok := payload["announcement"]; ok {
		m, ok := raw.(map[string]any)
		if !ok {
			h.writeError(w, http.StatusBadRequest, "Invalid announcement payload")
			return
		}
		announcementPayload = m
	}

	if h.cfg == nil {
		h.writeError(w, http.StatusServiceUnavailable, "config manager unavailable")
		return
	}
	var updated model.AppriseConfig
	var updatedAnnouncement model.HomeAnnouncementConfig
	err := h.cfg.Modify(func(cfg *model.Config) (*model.Config, error) {
		if cfg == nil {
			return cfg, nil
		}
		if v, ok := apprisePayload["key"].(string); ok && strings.TrimSpace(v) == "" {
			delete(apprisePayload, "key")
		}
		updated = cfg.Notifications.Apprise
		mergeIntoStruct(&updated, apprisePayload)
		cfg.Notifications.Apprise = updated
		updatedAnnouncement = cfg.Notifications.Announcement
		if announcementPayload != nil {
			if v, ok := announcementPayload["token"].(string); ok && strings.TrimSpace(v) == "" {
				delete(announcementPayload, "token")
			}
			mergeIntoStruct(&updatedAnnouncement, announcementPayload)
			cfg.Notifications.Announcement = updatedAnnouncement
		}
		return cfg, nil
	})
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "failed to update settings")
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "apprise": updated, "announcement": updatedAnnouncement})
}

// NotificationsTest sends real test messages, optionally using payload overrides.
func (h *Handler) NotificationsTest(w http.ResponseWriter, r *http.Request) {
	var payload map[string]any
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil && err != io.EOF {
		h.writeError(w, http.StatusBadRequest, "Invalid JSON payload")
		return
	}

	var apprisePayload map[string]any
	var announcementPayload map[string]any
	if payload != nil {
		if raw, ok := payload["apprise"]; ok {
			m, ok := raw.(map[string]any)
			if !ok {
				h.writeError(w, http.StatusBadRequest, "Invalid apprise payload")
				return
			}
			apprisePayload = m
		} else {
			apprisePayload = payload
		}
		if raw, ok := payload["announcement"]; ok {
			m, ok := raw.(map[string]any)
			if !ok {
				h.writeError(w, http.StatusBadRequest, "Invalid announcement payload")
				return
			}
			announcementPayload = m
		}
	}

	cfg, err := h.loadConfig()
	if err != nil || cfg == nil {
		h.writeError(w, http.StatusBadRequest, "No printers configured")
		return
	}

	appriseCfg := cfg.Notifications.Apprise
	announcementCfg := cfg.Notifications.Announcement
	if apprisePayload != nil {
		// Only allow overriding connection parameters for the test request.
		// This is defense-in-depth against SSRF: even if URL validation in
		// notifyURL() were bypassed, an attacker cannot influence templates,
		// event flags, or other behavior through the test endpoint.
		if v, ok := apprisePayload["server_url"].(string); ok {
			appriseCfg.ServerURL = v
		}
		if v, ok := apprisePayload["key"].(string); ok {
			appriseCfg.Key = v
		}
		if v, ok := apprisePayload["raw_body_template"].(string); ok {
			appriseCfg.RawBodyTemplate = v
		}
		if v, ok := apprisePayload["raw_content_type"].(string); ok {
			appriseCfg.RawContentType = v
		}
	}
	if announcementPayload != nil {
		if v, ok := announcementPayload["base_url"].(string); ok {
			announcementCfg.BaseURL = v
		}
		if v, ok := announcementPayload["token"].(string); ok {
			announcementCfg.Token = v
		}
		if v, ok := announcementPayload["tts_entity_id"].(string); ok {
			announcementCfg.TTSEntityID = v
		}
		if v, ok := announcementPayload["media_player_entity_id"].(string); ok {
			announcementCfg.MediaPlayerEntityID = v
		}
		if v, ok := announcementPayload["template"].(string); ok {
			announcementCfg.Template = v
		}
	}

	var snap notifications.SnapshotCapturer
	if vq, ok := h.videoQueue(); ok {
		snap = vq
	}

	okNotify, msgNotify := notifications.SendTestNotification(r.Context(), appriseCfg, snap)
	okAnnounce, msgAnnounce := true, ""
	if announcementCfg.Enabled {
		okAnnounce, msgAnnounce = notifications.SendTestAnnouncement(r.Context(), announcementCfg)
	}
	if !okNotify || !okAnnounce {
		msg := msgNotify
		if !okNotify && !okAnnounce {
			msg = msgNotify + "; " + msgAnnounce
		} else if !okAnnounce {
			msg = msgAnnounce
		}
		h.writeError(w, http.StatusBadRequest, msg)
		return
	}
	msg := msgNotify
	if announcementCfg.Enabled && msgAnnounce != "" {
		msg = msgNotify + "; " + msgAnnounce
	}
	h.writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "message": msg})
}
