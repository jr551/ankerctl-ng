package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/django1982/ankerctl/internal/service"
	"github.com/django1982/ankerctl/internal/util"
)

var printerFilePreviewHTTPClient = &http.Client{Timeout: 10 * time.Second}

func validateStoredFilePath(filePath, source string) (string, string, error) {
	normalizedPath := strings.TrimSpace(filePath)
	if normalizedPath == "" {
		return "", "", fmt.Errorf("Stored file path is required")
	}

	inferredSource := service.InferStoredFileSourceFromPath(normalizedPath)
	if inferredSource != "onboard" && inferredSource != "usb" {
		return "", "", fmt.Errorf("Unsupported stored file path")
	}

	if source != "" && inferredSource != source {
		return "", "", fmt.Errorf("Stored file path does not match source '%s'", source)
	}

	return normalizedPath, inferredSource, nil
}

func (h *Handler) storedFileControlIdentity() (string, string) {
	cfg, err := h.loadConfig()
	if err != nil || cfg == nil || cfg.Account == nil {
		return "", ""
	}
	return cfg.Account.Email, cfg.Account.UserID
}

func fetchPrinterFilePreview(previewURL string) ([]byte, string, error) {
	previewURL = strings.TrimSpace(previewURL)
	req, err := http.NewRequest(http.MethodGet, previewURL, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", "ankerctl")

	resp, err := printerFilePreviewHTTPClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("Preview image request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("Preview image request failed with HTTP %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("Preview image request failed: %w", err)
	}

	contentType := "image/jpeg"
	if rawType := strings.TrimSpace(resp.Header.Get("Content-Type")); rawType != "" {
		if parsedType, _, err := mime.ParseMediaType(rawType); err == nil && parsedType != "" {
			contentType = parsedType
		} else {
			contentType = rawType
		}
	}

	return data, contentType, nil
}

// PrinterFilesList handles GET /api/files/printer.
func (h *Handler) PrinterFilesList(w http.ResponseWriter, r *http.Request) {
	source := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("source")))
	if source == "" {
		source = "onboard"
	}
	if source != "onboard" && source != "usb" {
		h.writeError(w, http.StatusBadRequest, "Invalid storage source")
		return
	}

	var sourceValue *int
	if rawValue := strings.TrimSpace(r.URL.Query().Get("value")); rawValue != "" {
		parsedValue, err := strconv.Atoi(rawValue)
		if err != nil {
			h.writeError(w, http.StatusBadRequest, "value must be an integer")
			return
		}
		sourceValue = &parsedValue
	}

	mqtt, err := h.borrowMqttQueue()
	if err != nil {
		h.writeError(w, http.StatusServiceUnavailable, "MQTT service unavailable")
		return
	}
	defer h.svc.Return("mqttqueue")

	result, err := mqtt.ProbeStoredFiles(r.Context(), source, sourceValue, 5*time.Second, time.Second)
	if err != nil {
		h.writeError(w, http.StatusServiceUnavailable, fmt.Sprintf("MQTT storage probe failed: %v", err))
		return
	}
	if result.ReplyCount == 0 {
		h.writeError(w, http.StatusGatewayTimeout, fmt.Sprintf("No response from printer for storage source '%s'", source))
		return
	}

	resolvedSource := source
	if result.SourceValue == 1 {
		resolvedSource = "onboard"
	}

	files := make([]map[string]any, 0, len(result.Files))
	for _, entry := range result.Files {
		item := map[string]any{
			"name":      entry.Name,
			"path":      entry.Path,
			"timestamp": entry.Timestamp,
			"source":    entry.Source,
		}
		thumbURL := url.URL{
			Path: "/api/files/printer/thumbnail",
		}
		query := thumbURL.Query()
		query.Set("filename", entry.Path)
		query.Set("source", resolvedSource)
		thumbURL.RawQuery = query.Encode()
		item["thumbnail_url"] = thumbURL.String()
		files = append(files, item)
	}

	h.writeJSON(w, http.StatusOK, map[string]any{
		"status":       "ok",
		"source":       resolvedSource,
		"source_value": result.SourceValue,
		"reply_count":  result.ReplyCount,
		"files":        files,
	})
}

// PrinterFileThumbnail handles GET /api/files/printer/thumbnail.
func (h *Handler) PrinterFileThumbnail(w http.ResponseWriter, r *http.Request) {
	source := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("source")))
	filePath := r.URL.Query().Get("filename")
	if strings.TrimSpace(filePath) == "" {
		filePath = r.URL.Query().Get("path")
	}

	validatedPath, _, err := validateStoredFilePath(filePath, source)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	mqtt, err := h.borrowMqttQueue()
	if err != nil {
		h.writeError(w, http.StatusServiceUnavailable, "MQTT service unavailable")
		return
	}
	defer h.svc.Return("mqttqueue")

	_, userID := h.storedFileControlIdentity()
	previewURL := mqtt.GetCachedStoredFilePreviewURL(validatedPath)
	if previewURL == "" {
		allowProbe := !mqtt.IsPrinting() && !mqtt.HasPendingStoredFileStart()
		previewURL, err = mqtt.GetStoredFilePreviewURL(r.Context(), validatedPath, userID, allowProbe)
		if err != nil {
			h.writeError(w, http.StatusBadGateway, err.Error())
			return
		}
	}
	if strings.TrimSpace(previewURL) == "" {
		h.writeError(w, http.StatusNotFound, "Thumbnail not available for this stored file")
		return
	}
	if err := util.ValidateExternalURL(previewURL); err != nil {
		h.writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	data, contentType, err := fetchPrinterFilePreview(previewURL)
	if err != nil {
		h.writeError(w, http.StatusBadGateway, err.Error())
		return
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("Cache-Control", "private, max-age=600")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// PrinterFilePrint handles POST /api/files/printer/print.
func (h *Handler) PrinterFilePrint(w http.ResponseWriter, r *http.Request) {
	payload := map[string]any{}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&payload)
	}

	source := strings.ToLower(strings.TrimSpace(stringFromAny(payload["source"])))
	if source != "" && source != "onboard" && source != "usb" {
		h.writeError(w, http.StatusBadRequest, "Invalid storage source")
		return
	}

	filePath := stringFromAny(payload["path"])
	if strings.TrimSpace(filePath) == "" {
		filePath = stringFromAny(payload["filename"])
	}

	validatedPath, inferredSource, err := validateStoredFilePath(filePath, source)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	mqtt, err := h.borrowMqttQueue()
	if err != nil {
		h.writeError(w, http.StatusServiceUnavailable, "MQTT service unavailable")
		return
	}
	defer h.svc.Return("mqttqueue")

	if mqtt.IsPrinting() || mqtt.HasPendingStoredFileStart() {
		h.writeError(w, http.StatusConflict, "Printer is already busy with another print job")
		return
	}

	userName, userID := h.storedFileControlIdentity()
	started, err := mqtt.StartStoredFile(r.Context(), validatedPath, userName, userID)
	if err != nil {
		h.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	if !started {
		h.writeJSON(w, http.StatusGatewayTimeout, map[string]string{
			"error": "Selected file preview loaded, but the printer did not confirm the job start. Stored-file launching is still incomplete for this firmware.",
		})
		return
	}

	h.writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"source": inferredSource,
		"path":   validatedPath,
		"name":   filepath.Base(validatedPath),
	})
}

func stringFromAny(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}
