package notifications

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/smtp"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/django1982/ankerctl/internal/model"
)

const defaultTimeout = 10 * time.Second

// eventEnvOverrides maps event names to their env-var overrides.
var eventEnvOverrides = map[string]string{
	EventPrintStarted:  "APPRISE_EVENT_PRINT_STARTED",
	EventPrintFinished: "APPRISE_EVENT_PRINT_FINISHED",
	EventPrintFailed:   "APPRISE_EVENT_PRINT_FAILED",
	EventGCodeUploaded: "APPRISE_EVENT_GCODE_UPLOADED",
	EventPrintProgress: "APPRISE_EVENT_PRINT_PROGRESS",
}

// Client sends notifications to an Apprise API server.
type Client struct {
	settings   model.AppriseConfig
	http       *http.Client
	lookupHost func(host string) ([]string, error) // injectable for tests
}

type DeliveryResult struct {
	At          time.Time `json:"at"`
	Event       string    `json:"event,omitempty"`
	OK          bool      `json:"ok"`
	Message     string    `json:"message,omitempty"`
	Transport   string    `json:"transport,omitempty"`
	Target      string    `json:"target,omitempty"`
	StatusCode  int       `json:"status_code,omitempty"`
	Title       string    `json:"title,omitempty"`
	ResponseRaw string    `json:"response_raw,omitempty"`
}

// NewClient builds an Apprise client with a 10-second HTTP timeout.
func NewClient(settings model.AppriseConfig) *Client {
	return &Client{
		settings:   settings,
		lookupHost: net.LookupHost,
		http: &http.Client{
			Timeout: defaultTimeout,
		},
	}
}

// ResolveAppriseEnv applies APPRISE_* environment variable overrides to a config,
// matching Python's AppriseClient._resolve_settings().
func ResolveAppriseEnv(cfg model.AppriseConfig) model.AppriseConfig {
	if v, ok := readBoolEnv("APPRISE_ENABLED"); ok {
		cfg.Enabled = v
	}
	if v := os.Getenv("APPRISE_SERVER_URL"); v != "" {
		cfg.ServerURL = v
	}
	if v := os.Getenv("APPRISE_KEY"); v != "" {
		cfg.Key = v
	}
	if v := os.Getenv("APPRISE_TAG"); v != "" {
		cfg.Tag = v
	}

	// Event overrides
	for event, envVar := range eventEnvOverrides {
		if v, ok := readBoolEnv(envVar); ok {
			switch event {
			case EventPrintStarted:
				cfg.Events.PrintStarted = v
			case EventPrintFinished:
				cfg.Events.PrintFinished = v
			case EventPrintFailed:
				cfg.Events.PrintFailed = v
			case EventGCodeUploaded:
				cfg.Events.GcodeUploaded = v
			case EventPrintProgress:
				cfg.Events.PrintProgress = v
			}
		}
	}

	// Progress overrides
	if v, ok := readIntEnv("APPRISE_PROGRESS_INTERVAL"); ok {
		cfg.Progress.IntervalPercent = v
	}
	if v, ok := readBoolEnv("APPRISE_PROGRESS_INCLUDE_IMAGE"); ok {
		cfg.Progress.IncludeImage = v
	}
	if v := os.Getenv("APPRISE_SNAPSHOT_QUALITY"); v != "" {
		cfg.Progress.SnapshotQuality = v
	}
	if v, ok := readBoolEnv("APPRISE_SNAPSHOT_FALLBACK"); ok {
		cfg.Progress.SnapshotFallback = v
	}
	if v, ok := readBoolEnv("APPRISE_SNAPSHOT_LIGHT"); ok {
		cfg.Progress.SnapshotLight = v
	}
	if v, ok := readIntEnv("APPRISE_PROGRESS_MAX"); ok {
		cfg.Progress.MaxValue = v
	}

	return cfg
}

// readBoolEnv reads an env var as bool, returning (value, found).
func readBoolEnv(key string) (bool, bool) {
	raw := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	if raw == "" {
		return false, false
	}
	switch raw {
	case "1", "true", "yes", "on", "y", "t":
		return true, true
	case "0", "false", "no", "off", "n", "f":
		return false, true
	default:
		slog.Warn("Ignoring unsupported env var value", "var", key, "value", raw)
		return false, false
	}
}

// readIntEnv reads an env var as int, returning (value, found).
func readIntEnv(key string) (int, bool) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return 0, false
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		slog.Warn("Ignoring unsupported env var value", "var", key, "value", raw)
		return 0, false
	}
	return v, true
}

// IsConfigured reports whether URL + key are present.
func (c *Client) IsConfigured() bool {
	return c.directJSONURL() != nil || c.directSMTPURL() != nil || (c.serverURL() != "" && c.key() != "")
}

// IsEnabled reports whether Apprise is enabled and configured.
func (c *Client) IsEnabled() bool {
	return c.settings.Enabled && c.IsConfigured()
}

// IsEventEnabled checks whether the event is enabled in config.
func (c *Client) IsEventEnabled(event string) bool {
	switch event {
	case EventPrintStarted:
		return c.settings.Events.PrintStarted
	case EventPrintFinished:
		return c.settings.Events.PrintFinished
	case EventPrintFailed:
		return c.settings.Events.PrintFailed
	case EventPrintPaused:
		return c.settings.Events.PrintPaused
	case EventPrintResumed:
		return c.settings.Events.PrintResumed
	case EventGCodeUploaded:
		return c.settings.Events.GcodeUploaded
	case EventPrintProgress:
		return c.settings.Events.PrintProgress
	default:
		return false
	}
}

// SendEvent renders template/title/type and posts a notification.
func (c *Client) SendEvent(ctx context.Context, event string, payload map[string]any, attachments []string) (bool, string) {
	res := c.SendEventDetailed(ctx, event, payload, attachments)
	return res.OK, res.Message
}

func (c *Client) SendEventDetailed(ctx context.Context, event string, payload map[string]any, attachments []string) DeliveryResult {
	if !c.IsEnabled() {
		return DeliveryResult{At: time.Now(), Event: event, OK: false, Message: "Apprise is disabled or missing required settings"}
	}
	if !c.IsEventEnabled(event) {
		return DeliveryResult{At: time.Now(), Event: event, OK: false, Message: fmt.Sprintf("Event disabled: %s", event)}
	}

	tmpl := c.templateForEvent(event)
	body := RenderTemplate(tmpl, payload)
	if u := c.directJSONURL(); u != nil && strings.TrimSpace(c.settings.RawBodyTemplate) != "" {
		renderPayload := clonePayloadMap(payload)
		renderPayload["event"] = event
		renderPayload["title"] = EventTitle(event)
		renderPayload["body"] = body
		renderPayload["type"] = EventType(event)
		renderPayload["tag"] = strings.TrimSpace(c.settings.Tag)
		rendered := RenderTemplate(c.settings.RawBodyTemplate, renderPayload)
		result := c.postRawDetailed(ctx, u, EventTitle(event), rendered, strings.TrimSpace(c.settings.RawContentType))
		result.Event = event
		return result
	}
	result := c.postDetailed(ctx, EventTitle(event), body, EventType(event), attachments)
	result.Event = event
	return result
}

// Post sends a raw notification payload.
// Attachments that are local file paths are uploaded as multipart/form-data.
// Other attachments (data URIs, URLs) are sent inline as JSON.
func (c *Client) Post(ctx context.Context, title, body, typ string, attachments []string) (bool, string) {
	res := c.postDetailed(ctx, title, body, typ, attachments)
	return res.OK, res.Message
}

func (c *Client) postDetailed(ctx context.Context, title, body, typ string, attachments []string) DeliveryResult {
	result := DeliveryResult{
		At:        time.Now(),
		Title:     title,
		Transport: "http",
	}
	if !c.IsConfigured() {
		result.Message = "Apprise server URL or key missing"
		return result
	}
	if u := c.directSMTPURL(); u != nil {
		return c.postSMTP(ctx, u, title, body, typ, attachments)
	}
	u := c.directJSONURL()
	if u == nil {
		u = c.notifyURL()
	}
	if u == nil {
		result.Message = "Apprise server URL or key missing"
		return result
	}
	result.Target = u.String()
	if c.directJSONURL() != nil {
		result.Transport = "webhook"
		if strings.TrimSpace(c.settings.RawBodyTemplate) != "" {
			renderPayload := map[string]any{
				"title": title,
				"body":  body,
				"type":  typ,
				"tag":   strings.TrimSpace(c.settings.Tag),
			}
			rendered := RenderTemplate(c.settings.RawBodyTemplate, renderPayload)
			return c.postRawDetailed(ctx, u, title, rendered, strings.TrimSpace(c.settings.RawContentType))
		}
	}

	// Separate local file paths from inline attachments (data URIs, URLs).
	var localFiles, inlineAttach []string
	for _, a := range attachments {
		if a == "" {
			continue
		}
		if strings.HasPrefix(a, "data:") || strings.HasPrefix(a, "http://") || strings.HasPrefix(a, "https://") {
			inlineAttach = append(inlineAttach, a)
		} else if _, err := os.Stat(a); err == nil {
			localFiles = append(localFiles, a)
		} else {
			inlineAttach = append(inlineAttach, a)
		}
	}

	// If we have local files, try multipart upload first (Python _post_with_attachments).
	if len(localFiles) > 0 {
		delivery, tried := c.postMultipart(ctx, u, title, body, typ, localFiles)
		if tried {
			if delivery.Target == "" {
				delivery.Target = result.Target
			}
			return delivery
		}
		// Fall through to JSON if multipart failed to build.
	}

	// JSON payload path.
	payload := map[string]any{
		"title": title,
		"body":  body,
		"type":  typ,
	}
	if tag := strings.TrimSpace(c.settings.Tag); tag != "" {
		payload["tag"] = tag
	}
	if len(inlineAttach) > 0 {
		payload["attach"] = inlineAttach
	}

	bodyJSON, err := json.Marshal(payload)
	if err != nil {
		result.Message = fmt.Sprintf("marshal apprise payload: %v", err)
		return result
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(bodyJSON))
	if err != nil {
		result.Message = fmt.Sprintf("build apprise request: %v", err)
		return result
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req) // lgtm[go/request-forgery] - URL validated by notifyURL: scheme allowlist + DNS-based private-IP check
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			result.Message = ctx.Err().Error()
			return result
		}
		result.Message = err.Error()
		return result
	}
	defer resp.Body.Close()

	return parseResponse(resp, result)
}

func (c *Client) postRawDetailed(ctx context.Context, u *url.URL, title, rawBody, contentType string) DeliveryResult {
	result := DeliveryResult{
		At:        time.Now(),
		Title:     title,
		Transport: "webhook",
		Target:    u.String(),
	}
	if strings.TrimSpace(contentType) == "" {
		contentType = "application/json"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), strings.NewReader(rawBody))
	if err != nil {
		result.Message = fmt.Sprintf("build raw webhook request: %v", err)
		return result
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := c.http.Do(req)
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			result.Message = ctx.Err().Error()
			return result
		}
		result.Message = err.Error()
		return result
	}
	defer resp.Body.Close()
	return parseResponse(resp, result)
}

func (c *Client) postSMTP(ctx context.Context, u *url.URL, title, body, typ string, attachments []string) DeliveryResult {
	result := DeliveryResult{At: time.Now(), Title: title, Transport: "smtp", Target: u.String()}
	tlsMode := smtpTLSMode(u)
	host := u.Hostname()
	if host == "" {
		result.Message = "SMTP host missing"
		return result
	}
	port := u.Port()
	if port == "" {
		if strings.EqualFold(u.Scheme, "mailtos") {
			port = "465"
		} else {
			port = "587"
		}
	}
	from := strings.TrimSpace(u.Query().Get("from"))
	username := ""
	password := ""
	if u.User != nil {
		username = u.User.Username()
		password, _ = u.User.Password()
	}
	if from == "" {
		from = username
	}
	recipients := smtpRecipients(u)
	if from == "" {
		result.Message = "SMTP from address missing"
		return result
	}
	if len(recipients) == 0 {
		result.Message = "SMTP recipient missing"
		return result
	}

	addr := net.JoinHostPort(host, port)
	dialer := &net.Dialer{Timeout: defaultTimeout}
	var client *smtp.Client
	var err error
	if tlsMode == "ssl" {
		conn, dialErr := tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
		if dialErr != nil {
			result.Message = dialErr.Error()
			return result
		}
		client, err = smtp.NewClient(conn, host)
	} else {
		conn, dialErr := dialer.DialContext(ctx, "tcp", addr)
		if dialErr != nil {
			result.Message = dialErr.Error()
			return result
		}
		client, err = smtp.NewClient(conn, host)
	}
	if err != nil {
		result.Message = err.Error()
		return result
	}
	defer client.Close()

	if tlsMode == "starttls" {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			result.Message = "SMTP server does not support STARTTLS"
			return result
		}
		if err := client.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
			result.Message = err.Error()
			return result
		}
	}
	if username != "" {
		if ok, _ := client.Extension("AUTH"); ok {
			if err := client.Auth(smtp.PlainAuth("", username, password, host)); err != nil {
				result.Message = err.Error()
				return result
			}
		}
	}
	if err := client.Mail(from); err != nil {
		result.Message = err.Error()
		return result
	}
	for _, rcpt := range recipients {
		if err := client.Rcpt(rcpt); err != nil {
			result.Message = err.Error()
			return result
		}
	}
	w, err := client.Data()
	if err != nil {
		result.Message = err.Error()
		return result
	}
	msg := smtpMessage(from, recipients, title, typ, body, attachments)
	if _, err := io.WriteString(w, msg); err != nil {
		_ = w.Close()
		result.Message = err.Error()
		return result
	}
	if err := w.Close(); err != nil {
		result.Message = err.Error()
		return result
	}
	if err := client.Quit(); err != nil {
		result.Message = err.Error()
		return result
	}
	result.OK = true
	result.Message = "SMTP notification sent"
	return result
}

func smtpRecipients(u *url.URL) []string {
	var recipients []string
	add := func(raw string) {
		for _, part := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ';' || r == ' ' }) {
			if v := strings.TrimSpace(part); v != "" {
				recipients = append(recipients, v)
			}
		}
	}
	for _, raw := range u.Query()["to"] {
		add(raw)
	}
	if path := strings.Trim(u.EscapedPath(), "/"); path != "" {
		if decoded, err := url.PathUnescape(path); err == nil {
			add(decoded)
		}
	}
	return recipients
}

func smtpTLSMode(u *url.URL) string {
	if v := strings.ToLower(strings.TrimSpace(u.Query().Get("tls"))); v != "" {
		switch v {
		case "none", "starttls", "ssl":
			return v
		}
	}
	if strings.EqualFold(u.Scheme, "mailtos") {
		return "ssl"
	}
	return "starttls"
}

func smtpMessage(from string, to []string, title, typ, body string, attachments []string) string {
	subject := mime.QEncoding.Encode("utf-8", cleanSMTPHeader(title))
	images := decodeImageAttachments(attachments)

	if len(images) == 0 {
		headers := []string{
			"From: " + cleanSMTPHeader(from),
			"To: " + cleanSMTPHeader(strings.Join(to, ", ")),
			"Subject: " + subject,
			"MIME-Version: 1.0",
			"Content-Type: text/plain; charset=UTF-8",
			"X-Ankerctl-Notification-Type: " + cleanSMTPHeader(typ),
		}
		return strings.Join(headers, "\r\n") + "\r\n\r\n" + normalizeSMTPBody(body) + "\r\n"
	}

	// Multipart message so the snapshot of the print is embedded in the email.
	const boundary = "==ankerctl-ng-boundary-3f9a1c=="
	var b strings.Builder
	b.WriteString("From: " + cleanSMTPHeader(from) + "\r\n")
	b.WriteString("To: " + cleanSMTPHeader(strings.Join(to, ", ")) + "\r\n")
	b.WriteString("Subject: " + subject + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("X-Ankerctl-Notification-Type: " + cleanSMTPHeader(typ) + "\r\n")
	b.WriteString("Content-Type: multipart/mixed; boundary=\"" + boundary + "\"\r\n\r\n")

	b.WriteString("--" + boundary + "\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n\r\n")
	b.WriteString(normalizeSMTPBody(body) + "\r\n\r\n")

	for i, img := range images {
		b.WriteString("--" + boundary + "\r\n")
		b.WriteString("Content-Type: " + img.mimeType + "\r\n")
		b.WriteString("Content-Transfer-Encoding: base64\r\n")
		b.WriteString(fmt.Sprintf("Content-Disposition: inline; filename=\"snapshot-%d.%s\"\r\n\r\n", i+1, img.ext))
		b.WriteString(wrapBase64(img.b64) + "\r\n")
	}
	b.WriteString("--" + boundary + "--\r\n")
	return b.String()
}

type smtpImage struct {
	mimeType string
	ext      string
	b64      string // already-base64-encoded payload
}

// decodeImageAttachments extracts data:image/...;base64,<data> attachments,
// which is how the snapshot is passed through the notification pipeline.
// Non-data-URI attachments (e.g. plain URLs) cannot be embedded in email and
// are skipped.
func decodeImageAttachments(attachments []string) []smtpImage {
	var out []smtpImage
	for _, a := range attachments {
		if !strings.HasPrefix(a, "data:") {
			continue
		}
		comma := strings.IndexByte(a, ',')
		if comma < 0 {
			continue
		}
		meta := a[len("data:"):comma]
		if !strings.Contains(meta, "base64") {
			continue
		}
		mimeType := strings.TrimSuffix(meta, ";base64")
		if mimeType == "" {
			mimeType = "application/octet-stream"
		}
		ext := "bin"
		if i := strings.IndexByte(mimeType, '/'); i >= 0 {
			ext = mimeType[i+1:]
		}
		out = append(out, smtpImage{mimeType: mimeType, ext: ext, b64: a[comma+1:]})
	}
	return out
}

// wrapBase64 re-wraps a base64 string to 76-character lines per RFC 2045.
func wrapBase64(s string) string {
	s = strings.NewReplacer("\r", "", "\n", "").Replace(s)
	const width = 76
	var b strings.Builder
	for len(s) > width {
		b.WriteString(s[:width])
		b.WriteString("\r\n")
		s = s[width:]
	}
	b.WriteString(s)
	return b.String()
}

func cleanSMTPHeader(v string) string {
	v = strings.ReplaceAll(v, "\r", " ")
	v = strings.ReplaceAll(v, "\n", " ")
	return strings.TrimSpace(v)
}

func normalizeSMTPBody(v string) string {
	v = strings.ReplaceAll(v, "\r\n", "\n")
	v = strings.ReplaceAll(v, "\r", "\n")
	return strings.ReplaceAll(v, "\n", "\r\n")
}

// postMultipart uploads local files as multipart/form-data.
// Returns (ok, msg, tried) where tried=false means the caller should fall through.
func (c *Client) postMultipart(ctx context.Context, u *url.URL, title, body, typ string, files []string) (DeliveryResult, bool) {
	result := DeliveryResult{At: time.Now(), Title: title, Transport: "webhook", Target: u.String()}
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	_ = writer.WriteField("title", title)
	_ = writer.WriteField("body", body)
	_ = writer.WriteField("type", typ)
	if tag := strings.TrimSpace(c.settings.Tag); tag != "" {
		_ = writer.WriteField("tag", tag)
	}

	attachCount := 0
	for _, path := range files {
		f, err := os.Open(path)
		if err != nil {
			slog.Warn("Apprise attachment not found", "path", path, "error", err)
			continue
		}
		attachCount++
		fieldName := fmt.Sprintf("attach%d", attachCount)
		part, err := writer.CreateFormFile(fieldName, filepath.Base(path))
		if err != nil {
			f.Close()
			continue
		}
		_, _ = io.Copy(part, f)
		f.Close()
	}

	if attachCount == 0 {
		return DeliveryResult{}, false // No files added, fall through to JSON.
	}

	_ = writer.Close()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), &buf)
	if err != nil {
		result.Message = fmt.Sprintf("build multipart request: %v", err)
		return result, true
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := c.http.Do(req) // lgtm[go/request-forgery] - URL validated by notifyURL: scheme allowlist + DNS-based private-IP check
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			result.Message = ctx.Err().Error()
			return result, true
		}
		result.Message = err.Error()
		return result, true
	}
	defer resp.Body.Close()

	return parseResponse(resp, result), true
}

func parseResponse(resp *http.Response, result DeliveryResult) DeliveryResult {
	dataBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
	result.StatusCode = resp.StatusCode
	result.ResponseRaw = strings.TrimSpace(string(dataBytes))

	var data map[string]any
	_ = json.Unmarshal(dataBytes, &data)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if msg := mapMessage(data); msg != "" {
			result.Message = msg
			return result
		}
		result.Message = fmt.Sprintf("%d %s", resp.StatusCode, resp.Status)
		return result
	}

	if success, ok := data["success"].(bool); ok && !success {
		if msg := mapMessage(data); msg != "" {
			result.Message = msg
			return result
		}
		result.Message = "Apprise error"
		return result
	}

	if msg, ok := data["message"].(string); ok && msg != "" {
		result.OK = true
		result.Message = msg
		return result
	}
	result.OK = true
	result.Message = "Notification sent"
	return result
}

func mapMessage(data map[string]any) string {
	if data == nil {
		return ""
	}
	if msg, ok := data["error"].(string); ok && msg != "" {
		return msg
	}
	if msg, ok := data["message"].(string); ok && msg != "" {
		return msg
	}
	return ""
}

func clonePayloadMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func (c *Client) templateForEvent(event string) string {
	templates := c.settings.Templates
	switch event {
	case EventPrintStarted:
		if templates.PrintStarted != "" {
			return templates.PrintStarted
		}
	case EventPrintFinished:
		if templates.PrintFinished != "" {
			return templates.PrintFinished
		}
	case EventPrintFailed:
		if templates.PrintFailed != "" {
			return templates.PrintFailed
		}
	case EventPrintPaused:
		if templates.PrintPaused != "" {
			return templates.PrintPaused
		}
	case EventPrintResumed:
		if templates.PrintResumed != "" {
			return templates.PrintResumed
		}
	case EventGCodeUploaded:
		if templates.GcodeUploaded != "" {
			return templates.GcodeUploaded
		}
	case EventPrintProgress:
		if templates.PrintProgress != "" {
			return templates.PrintProgress
		}
	}
	return DefaultTemplateForEvent(event)
}

func (c *Client) serverURL() string {
	return strings.TrimRight(strings.TrimSpace(c.settings.ServerURL), "/")
}

func (c *Client) key() string {
	return strings.Trim(strings.TrimSpace(c.settings.Key), "/")
}

func (c *Client) notifyURL() *url.URL {
	serverURL := c.serverURL()
	key := c.key()
	if serverURL == "" || key == "" {
		return nil
	}

	// Direct Apprise JSON webhook URL support. Apprise service URLs use
	// json:// and jsons://; ankerctl normally targets an Apprise API server,
	// but this lets a configured jsons:// URL post JSON directly.
	if direct, err := url.Parse(serverURL); err == nil && (direct.Scheme == "json" || direct.Scheme == "jsons") {
		if direct.Scheme == "jsons" {
			direct.Scheme = "https"
		} else {
			direct.Scheme = "http"
		}
		if c.isPrivateHost(direct) {
			slog.Warn("Apprise direct JSON URL points to private/loopback address, ignoring", "url", direct.String())
			return nil
		}
		return direct
	}

	base := serverURL
	if !strings.HasSuffix(base, "/notify") {
		base += "/notify"
	}
	full := base + "/" + key
	u, err := url.Parse(full)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		slog.Warn("Apprise server URL has unsupported scheme, ignoring", "url", full)
		return nil
	}
	if c.isPrivateHost(u) {
		slog.Warn("Apprise server URL points to private/loopback address, ignoring", "url", full)
		return nil
	}
	return u
}

func (c *Client) directJSONURL() *url.URL {
	raw := c.serverURL()
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil
	}
	switch strings.ToLower(u.Scheme) {
	case "json":
		u.Scheme = "http"
	case "jsons":
		u.Scheme = "https"
	default:
		return nil
	}
	if u.Host == "" {
		return nil
	}
	if c.isPrivateHost(u) {
		slog.Warn("Apprise JSON webhook points to private/loopback address, ignoring", "url", raw)
		return nil
	}
	return u
}

func (c *Client) directSMTPURL() *url.URL {
	raw := c.serverURL()
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil
	}
	switch strings.ToLower(u.Scheme) {
	case "mailto", "mailtos":
	default:
		return nil
	}
	if u.Host == "" {
		return nil
	}
	if c.isPrivateHost(u) {
		slog.Warn("SMTP notification host points to private/loopback address, ignoring", "url", raw)
		return nil
	}
	return u
}

// isPrivateHost reports whether u's host resolves to a private, loopback, or
// link-local address. This prevents SSRF against internal services and cloud IMDS.
// For hostname (non-IP) targets a DNS lookup is performed so that a domain
// pointing at an internal address (DNS-rebinding / split-horizon) is also blocked.
func (c *Client) isPrivateHost(u *url.URL) bool {
	host := u.Hostname()
	if ip := net.ParseIP(host); ip != nil {
		return isRestrictedIP(ip)
	}
	lower := strings.ToLower(host)
	if lower == "localhost" || strings.HasSuffix(lower, ".local") {
		return true
	}
	// Resolve the hostname and check every returned address. Fail-closed: if
	// DNS lookup fails we treat the host as private to avoid sending requests
	// to an unresolvable destination.
	addrs, err := c.lookupHost(host)
	if err != nil {
		return true
	}
	for _, addr := range addrs {
		if ip := net.ParseIP(addr); ip != nil && isRestrictedIP(ip) {
			return true
		}
	}
	return false
}

func isRestrictedIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast()
}
