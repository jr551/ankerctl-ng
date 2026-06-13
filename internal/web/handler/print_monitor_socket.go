package handler

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/django1982/ankerctl/internal/model"
	"github.com/django1982/ankerctl/internal/service"
)

func (h *Handler) SettingsPrintMonitorGet(w http.ResponseWriter, _ *http.Request) {
	cfg, _ := h.loadConfig()
	pm := model.DefaultPrintMonitorConfig()
	if cfg != nil {
		pm = cfg.PrintMonitor
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"print_monitor": pm})
}

func (h *Handler) SettingsPrintMonitorUpdate(w http.ResponseWriter, r *http.Request) {
	var payload map[string]any
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		h.writeError(w, http.StatusBadRequest, "Invalid JSON payload")
		return
	}
	pmPayload := payload
	if raw, ok := payload["print_monitor"]; ok {
		m, ok := raw.(map[string]any)
		if !ok {
			h.writeError(w, http.StatusBadRequest, "Invalid print_monitor payload")
			return
		}
		pmPayload = m
	}

	var updated model.PrintMonitorConfig
	err := h.cfg.Modify(func(cfg *model.Config) (*model.Config, error) {
		if cfg == nil {
			return cfg, nil
		}
		updated = cfg.PrintMonitor
		mergeIntoStruct(&updated, pmPayload)
		cfg.PrintMonitor = updated
		return cfg, nil
	})
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "failed to update print monitor settings")
		return
	}
	if pm, ok := h.printMonitor(); ok {
		pm.Configure(updated)
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "print_monitor": updated})
}

func (h *Handler) PrintMonitorStatus(w http.ResponseWriter, _ *http.Request) {
	pm, ok := h.printMonitor()
	if !ok {
		h.writeJSON(w, http.StatusOK, map[string]any{"available": false})
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"available": true, "status": pm.Status()})
}

func (h *Handler) PrintMonitorCheck(w http.ResponseWriter, _ *http.Request) {
	pm, ok := h.printMonitor()
	if !ok {
		h.writeError(w, http.StatusServiceUnavailable, "print monitor service unavailable")
		return
	}
	pm.RunOnce()
	h.writeJSON(w, http.StatusAccepted, map[string]string{"status": "queued"})
}

func (h *Handler) SettingsSmartSocketGet(w http.ResponseWriter, _ *http.Request) {
	cfg, _ := h.loadConfig()
	ss := model.DefaultSmartSocketConfig()
	if cfg != nil {
		ss = cfg.SmartSocket
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"smart_socket": ss})
}

func (h *Handler) SettingsSmartSocketUpdate(w http.ResponseWriter, r *http.Request) {
	var payload map[string]any
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		h.writeError(w, http.StatusBadRequest, "Invalid JSON payload")
		return
	}
	ssPayload := payload
	if raw, ok := payload["smart_socket"]; ok {
		m, ok := raw.(map[string]any)
		if !ok {
			h.writeError(w, http.StatusBadRequest, "Invalid smart_socket payload")
			return
		}
		ssPayload = m
	}

	var updated model.SmartSocketConfig
	err := h.cfg.Modify(func(cfg *model.Config) (*model.Config, error) {
		if cfg == nil {
			return cfg, nil
		}
		updated = cfg.SmartSocket
		mergeIntoStruct(&updated, ssPayload)
		cfg.SmartSocket = updated
		return cfg, nil
	})
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "failed to update smart socket settings")
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "smart_socket": updated})
}

func (h *Handler) SmartSocketState(w http.ResponseWriter, r *http.Request) {
	cfg, err := h.loadConfig()
	if err != nil || cfg == nil || !cfg.SmartSocket.Enabled {
		h.writeJSON(w, http.StatusOK, map[string]any{"available": false})
		return
	}
	ss := cfg.SmartSocket
	if strings.TrimSpace(ss.SwitchEntity) == "" {
		h.writeJSON(w, http.StatusOK, map[string]any{"available": false, "error": "switch entity is not configured"})
		return
	}
	client := service.NewHomeAssistantClient(ss.BaseURL, ss.Token)
	sw, err := client.State(r.Context(), ss.SwitchEntity)
	if err != nil {
		h.writeJSON(w, http.StatusOK, map[string]any{"available": false, "error": err.Error()})
		return
	}
	out := map[string]any{
		"available": true,
		"state":     sw.State,
	}
	if strings.TrimSpace(ss.PowerEntity) != "" {
		if power, err := client.State(r.Context(), ss.PowerEntity); err == nil {
			out["power"] = power.State
			if unit, ok := power.Attributes["unit_of_measurement"].(string); ok && unit != "" {
				out["power_unit"] = unit
			} else {
				out["power_unit"] = ss.PowerUnit
			}
		}
	}
	h.writeJSON(w, http.StatusOK, out)
}

func (h *Handler) SmartSocketControl(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Action string `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		h.writeError(w, http.StatusBadRequest, "Invalid JSON payload")
		return
	}
	action := strings.ToLower(strings.TrimSpace(payload.Action))
	if action != "on" && action != "off" {
		h.writeError(w, http.StatusBadRequest, "action must be on or off")
		return
	}
	cfg, err := h.loadConfig()
	if err != nil || cfg == nil || !cfg.SmartSocket.Enabled || strings.TrimSpace(cfg.SmartSocket.SwitchEntity) == "" {
		h.writeError(w, http.StatusBadRequest, "smart socket is not configured")
		return
	}
	client := service.NewHomeAssistantClient(cfg.SmartSocket.BaseURL, cfg.SmartSocket.Token)
	serviceName := "turn_on"
	if action == "off" {
		serviceName = "turn_off"
	}
	if err := client.CallService(r.Context(), "switch", serviceName, cfg.SmartSocket.SwitchEntity); err != nil {
		h.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "action": action})
}
