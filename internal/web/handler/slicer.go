package handler

import (
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/django1982/ankerctl/internal/gcode"
	"github.com/django1982/ankerctl/internal/model"
)

func parseBoolHTTP(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// SlicerUpload handles OctoPrint-compatible multipart file uploads.
func (h *Handler) SlicerUpload(w http.ResponseWriter, r *http.Request) {
	if h.uploadMaxBytes > 0 {
		r.Body = http.MaxBytesReader(w, r.Body, h.uploadMaxBytes)
	}

	if err := r.ParseMultipartForm(64 << 20); err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid multipart form")
		return
	}
	fd, hdr, err := r.FormFile("file")
	if err != nil {
		h.writeError(w, http.StatusBadRequest, "missing file field")
		return
	}
	defer fd.Close()

	data, err := io.ReadAll(fd)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, "failed to read uploaded file")
		return
	}

	startPrint := parseBoolHTTP(r.FormValue("print"))
	cfg, _ := h.loadConfig()
	userID := ""
	rateLimit := 10
	if cfg != nil {
		if cfg.Account != nil {
			userID = cfg.Account.UserID
		}
		if cfg.UploadRateMbps > 0 {
			rateLimit = cfg.UploadRateMbps
		}
	}
	if v := strings.TrimSpace(r.FormValue("upload_rate_mbps")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			rateLimit = n
		}
	}

	var overrideStats gcode.TemperatureOverrideStats
	if startPrint && cfg != nil {
		if printer, _, _ := h.activePrinter(cfg); printer != nil {
			override := temperatureOverrideEntryForPrinter(cfg, printer.SN)
			if override.Enabled {
				data, overrideStats = gcode.ApplyTemperatureOverrides(data, gcode.TemperatureOverrides{
					NozzleMinTempC: override.NozzleMinTempC,
					BedMinTempC:    override.BedMinTempC,
				})
				if overrideStats.NozzleCommands > 0 || overrideStats.BedCommands > 0 {
					h.log.Info("slicer upload: applied temperature overrides",
						"file", hdr.Filename,
						"nozzle_min_temp_c", override.NozzleMinTempC,
						"bed_min_temp_c", override.BedMinTempC,
						"nozzle_commands", overrideStats.NozzleCommands,
						"bed_commands", overrideStats.BedCommands)
				}
			}
		}
	}

	// Borrow ppppservice so it starts and connects before the upload begins.
	// Python parity: filetransfer.py calls pppp_open() which waits for
	// StateConnected before sending any data.
	if _, err := h.svc.Borrow("ppppservice"); err != nil {
		h.log.Warn("slicer upload: pppp service unavailable", "file", hdr.Filename, "size", len(data), "err", err)
		h.writeError(w, http.StatusServiceUnavailable, "pppp service unavailable")
		return
	}
	defer h.svc.Return("ppppservice")

	// Borrow filetransfer so its WorkerRun loop is active to process the request.
	if _, err := h.svc.Borrow("filetransfer"); err != nil {
		h.log.Warn("slicer upload: file transfer service unavailable", "file", hdr.Filename, "size", len(data), "err", err)
		h.writeError(w, http.StatusServiceUnavailable, "file transfer service unavailable")
		return
	}
	defer h.svc.Return("filetransfer")

	ft, ok := h.fileTransfer()
	if !ok {
		h.log.Warn("slicer upload: file transfer service missing", "file", hdr.Filename, "size", len(data))
		h.writeError(w, http.StatusServiceUnavailable, "file transfer service unavailable")
		return
	}
	userName := strings.TrimSpace(r.UserAgent())
	if userName == "" {
		userName = "ankerctl"
	}
	// Archive GCode before starting the upload so the history row can store
	// the archive path immediately.  Failures are non-fatal — we log and
	// continue; the printer still gets its file.
	var archiveRelpath string
	var archiveSize int64
	if h.gcodeArchiver != nil {
		rel, sz, archErr := h.gcodeArchiver.Archive(hdr.Filename, data)
		if archErr != nil {
			h.log.Warn("slicer upload: gcode archive failed", "err", archErr)
		} else {
			archiveRelpath = rel
			archiveSize = sz
		}
	}

	if err := ft.SendFile(r.Context(), hdr.Filename, userName, userID, data, rateLimit, startPrint); err != nil {
		h.log.Warn("slicer upload: file transfer failed", "file", hdr.Filename, "size", len(data), "rate_mbps", rateLimit, "start_print", startPrint, "err", err)
		h.writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	h.log.Info("slicer upload: file transfer completed", "file", hdr.Filename, "size", len(data), "rate_mbps", rateLimit, "start_print", startPrint)

	// If archiving succeeded and we have a DB, back-fill the latest history
	// row for this file so that reprint works.
	if archiveRelpath != "" && h.db != nil {
		// FORGE-NOTE: We record archive info without a task_id here because
		// the MQTT task_id arrives asynchronously; SetArchiveInfo uses COALESCE
		// so it will not overwrite an existing value.
		if rowID, err := h.db.RecordStart(hdr.Filename, "", archiveRelpath, archiveSize); err != nil {
			h.log.Warn("slicer upload: failed to record history start with archive", "err", err)
		} else if rowID != 0 {
			h.log.Debug("slicer upload: archive stored in history", "row_id", rowID, "relpath", archiveRelpath)
		}
	}

	// Python parity: return effective rate and source after successful upload.
	cfgRate := 0
	if cfg != nil {
		cfgRate = cfg.UploadRateMbps
	}
	effectiveRate, rateSource := model.ResolveUploadRateMbpsWithSource(cfgRate, 0)
	h.writeJSON(w, http.StatusOK, map[string]any{
		"status":             "ok",
		"upload_rate_mbps":   effectiveRate,
		"upload_rate_source": rateSource,
		"temperature_overrides": map[string]any{
			"applied":         overrideStats.NozzleCommands > 0 || overrideStats.BedCommands > 0,
			"nozzle_commands": overrideStats.NozzleCommands,
			"bed_commands":    overrideStats.BedCommands,
		},
	})
}
