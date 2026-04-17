package handler

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/django1982/ankerctl/internal/service"
)

var unsafeGCodePrefixes = map[string]struct{}{
	"G0": {}, "G1": {}, "G28": {}, "G29": {}, "G91": {}, "G90": {},
}

// normalizeGCodeLines strips inline comments and blank lines, matching
// Python's cli.util.normalize_gcode_lines behaviour.
func normalizeGCodeLines(gcode string) []string {
	var out []string
	for _, raw := range strings.Split(gcode, "\n") {
		line := strings.TrimSpace(strings.SplitN(raw, ";", 2)[0])
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// PrinterGCode sends raw gcode commands.
func (h *Handler) PrinterGCode(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		GCode string `json:"gcode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload.GCode == "" {
		h.writeError(w, http.StatusBadRequest, "Missing gcode")
		return
	}

	lines := normalizeGCodeLines(payload.GCode)
	if len(lines) == 0 {
		h.writeError(w, http.StatusBadRequest, "No executable gcode lines found")
		return
	}

	// Borrow ensures the mqttqueue service is running (Python parity: borrow("mqttqueue")).
	svc, err := h.svc.Borrow("mqttqueue")
	if err != nil {
		h.writeError(w, http.StatusServiceUnavailable, "Service unavailable")
		return
	}
	defer h.svc.Return("mqttqueue")

	mqtt, ok := svc.(*service.MqttQueue)
	if !ok {
		h.writeError(w, http.StatusInternalServerError, "Service type mismatch")
		return
	}

	if mqtt.IsPrinting() {
		for _, line := range lines {
			parts := strings.Fields(line)
			if _, blocked := unsafeGCodePrefixes[strings.ToUpper(parts[0])]; blocked {
				h.writeError(w, http.StatusConflict, "Motion commands blocked while printing")
				return
			}
		}
	}

	if err := mqtt.SendGCode(r.Context(), strings.Join(lines, "\n")); err != nil {
		h.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// printControlAllowlist is the exhaustive set of valid ct=1008 (PrintControl)
// command values, mirroring the Python allowlist in web/__init__.py:
//
//	0 = start, 2 = stop, 3 = pause, 4 = resume
//
// Value 1 is intentionally excluded: it is an internal printer state indicator,
// not a valid control command. Any value outside this set is rejected with 400.
var printControlAllowlist = map[int]struct{}{
	0: {}, // start
	2: {}, // stop
	3: {}, // pause
	4: {}, // resume
}

// PrinterControl sends print-control commands.
// Body: {"value": <int>}  (matches Python; value=0 is valid — idle state)
func (h *Handler) PrinterControl(w http.ResponseWriter, r *http.Request) {
	// Decode into raw map so we can distinguish missing key from value=0.
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil || raw == nil {
		h.writeError(w, http.StatusBadRequest, "Missing value")
		return
	}
	rawVal, ok := raw["value"]
	if !ok {
		h.writeError(w, http.StatusBadRequest, "Missing value")
		return
	}
	var value int
	if err := json.Unmarshal(rawVal, &value); err != nil {
		h.writeError(w, http.StatusBadRequest, "Value must be an integer")
		return
	}
	if _, allowed := printControlAllowlist[value]; !allowed {
		h.writeError(w, http.StatusBadRequest, "Invalid control value")
		return
	}
	mqtt, ok := h.mqttQueue()
	if !ok {
		h.writeError(w, http.StatusServiceUnavailable, "Service unavailable")
		return
	}
	if err := mqtt.SendPrintControl(r.Context(), value); err != nil {
		h.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// PrinterAutolevel starts auto-leveling.
func (h *Handler) PrinterAutolevel(w http.ResponseWriter, r *http.Request) {
	mqtt, ok := h.mqttQueue()
	if !ok {
		h.writeError(w, http.StatusServiceUnavailable, "Service unavailable")
		return
	}
	if mqtt.IsPrinting() {
		h.writeError(w, http.StatusConflict, "Auto-leveling blocked while printing")
		return
	}
	if err := mqtt.SendAutoLeveling(r.Context()); err != nil {
		h.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// PrinterHome handles POST /api/printer/home.
// Body: {"axis": "all"} | {"axis": "xy"} | {"axis": "z"}
// Omitting "axis" defaults to "all".
// Motion is blocked while printing.
func (h *Handler) PrinterHome(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Axis string `json:"axis"`
	}
	// Silent decode: missing body or missing field uses zero value ("").
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&payload)
	}
	axis := strings.ToLower(strings.TrimSpace(payload.Axis))
	if axis == "" {
		axis = "all"
	}
	if axis != "all" && axis != "xy" && axis != "z" {
		h.writeError(w, http.StatusBadRequest, "Invalid home axis")
		return
	}

	mqtt, ok := h.mqttQueue()
	if !ok {
		h.writeError(w, http.StatusServiceUnavailable, "Service unavailable")
		return
	}
	if mqtt.IsPrinting() {
		h.writeError(w, http.StatusConflict, "Motion commands blocked while printing")
		return
	}
	if err := mqtt.SendHome(r.Context(), axis); err != nil {
		h.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "axis": axis})
}
