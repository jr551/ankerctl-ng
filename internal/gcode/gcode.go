package gcode

import (
	"bytes"
	"regexp"
	"strconv"
	"strings"
)

var (
	estimatedTimePattern = regexp.MustCompile(`(?i);\s*estimated printing time[^=]*=\s*(.*)`) // slicer header
	timeTokenPattern     = regexp.MustCompile(`(?i)(\d+)\s*([dhms])`)
	gcodeCommandPattern  = regexp.MustCompile(`(?i)^\s*([A-Z]+\d+)`)
	tempParamPattern     = regexp.MustCompile(`(?i)(^|[ \t])S[ \t]*([-+]?[0-9]+(?:\.[0-9]+)?)`)
	layerCountPatterns   = []*regexp.Regexp{
		regexp.MustCompile(`(?i)^;LAYER_COUNT:(\d+)`),
		regexp.MustCompile(`(?i)^;\s*total layer(?:s)?\s*(?:number|count)?\s*[=:]\s*(\d+)`),
	}
)

var timeUnits = map[string]int{
	"d": 86400,
	"h": 3600,
	"m": 60,
	"s": 1,
}

// TemperatureOverrides raises non-zero GCode temperature targets below the
// configured floor. Zero targets are left alone so cooldown commands still work.
type TemperatureOverrides struct {
	NozzleMinTempC int
	BedMinTempC    int
}

// TemperatureOverrideStats reports how many commands were changed.
type TemperatureOverrideStats struct {
	NozzleCommands int
	BedCommands    int
}

// PatchGCodeTime inserts a ;TIME:<seconds> marker before the first G28 if missing.
func PatchGCodeTime(data []byte) []byte {
	text := string(data)
	lines := strings.SplitAfter(text, "\n")
	if len(lines) == 0 {
		return data
	}

	g28Index := -1
	seconds := 0
	hasSeconds := false

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(strings.ToUpper(trimmed), ";TIME:") {
			return data
		}
		if g28Index == -1 && strings.HasPrefix(strings.ToUpper(trimmed), "G28") {
			g28Index = i
		}
		if !hasSeconds {
			if m := estimatedTimePattern.FindStringSubmatch(line); len(m) == 2 {
				if parsed, ok := parseEstimatedSeconds(m[1]); ok {
					seconds = parsed
					hasSeconds = true
				}
			}
		}
		if g28Index != -1 && hasSeconds {
			break
		}
	}

	if g28Index == -1 || !hasSeconds {
		return data
	}

	insert := ";TIME:" + strconv.Itoa(seconds) + "\n"
	patched := make([]string, 0, len(lines)+1)
	patched = append(patched, lines[:g28Index]...)
	patched = append(patched, insert)
	patched = append(patched, lines[g28Index:]...)
	return []byte(strings.Join(patched, ""))
}

// ApplyTemperatureOverrides raises M104/M109 and M140/M190 S parameters that
// are below the configured minimums. Comments and cooldown commands are kept.
func ApplyTemperatureOverrides(data []byte, overrides TemperatureOverrides) ([]byte, TemperatureOverrideStats) {
	if overrides.NozzleMinTempC <= 0 && overrides.BedMinTempC <= 0 {
		return data, TemperatureOverrideStats{}
	}
	lines := strings.SplitAfter(string(data), "\n")
	var out strings.Builder
	out.Grow(len(data))
	var stats TemperatureOverrideStats

	for _, line := range lines {
		codePart := line
		commentPart := ""
		if idx := strings.Index(codePart, ";"); idx >= 0 {
			commentPart = codePart[idx:]
			codePart = codePart[:idx]
		}

		patched, changed := applyTemperatureOverrideLine(codePart, overrides)
		if changed {
			cmd := firstGCodeCommand(codePart)
			if isNozzleTempCommand(cmd) {
				stats.NozzleCommands++
			} else if isBedTempCommand(cmd) {
				stats.BedCommands++
			}
		}
		out.WriteString(patched)
		out.WriteString(commentPart)
	}

	if stats.NozzleCommands == 0 && stats.BedCommands == 0 {
		return data, stats
	}
	return []byte(out.String()), stats
}

func applyTemperatureOverrideLine(codePart string, overrides TemperatureOverrides) (string, bool) {
	cmd, restStart := parseGCodeCommand(codePart)
	minTemp := 0
	switch {
	case isNozzleTempCommand(cmd):
		minTemp = overrides.NozzleMinTempC
	case isBedTempCommand(cmd):
		minTemp = overrides.BedMinTempC
	default:
		return codePart, false
	}
	if minTemp <= 0 {
		return codePart, false
	}

	rest := codePart[restStart:]
	match := tempParamPattern.FindStringSubmatchIndex(rest)
	if len(match) != 6 {
		return codePart, false
	}
	valueStart := restStart + match[4]
	valueEnd := restStart + match[5]
	rawValue := codePart[valueStart:valueEnd]
	target, err := strconv.ParseFloat(rawValue, 64)
	if err != nil || target <= 0 || target >= float64(minTemp) {
		return codePart, false
	}
	return codePart[:valueStart] + strconv.Itoa(minTemp) + codePart[valueEnd:], true
}

func firstGCodeCommand(codePart string) string {
	cmd, _ := parseGCodeCommand(codePart)
	return cmd
}

func parseGCodeCommand(codePart string) (string, int) {
	match := gcodeCommandPattern.FindStringSubmatchIndex(codePart)
	if len(match) != 4 {
		return "", 0
	}
	return strings.ToUpper(codePart[match[2]:match[3]]), match[1]
}

func isNozzleTempCommand(cmd string) bool {
	return cmd == "M104" || cmd == "M109"
}

func isBedTempCommand(cmd string) bool {
	return cmd == "M140" || cmd == "M190"
}

// ExtractLayerCount extracts the layer count from slicer comments.
func ExtractLayerCount(data []byte) (int, bool) {
	text := string(data)
	lines := strings.Split(text, "\n")

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, ";") {
			break
		}
		for _, pattern := range layerCountPatterns {
			m := pattern.FindStringSubmatch(trimmed)
			if len(m) != 2 {
				continue
			}
			value, err := strconv.Atoi(m[1])
			if err == nil {
				return value, true
			}
		}
	}

	count := bytes.Count(data, []byte(";LAYER_CHANGE"))
	if count > 0 {
		return count, true
	}
	return 0, false
}

func parseEstimatedSeconds(raw string) (int, bool) {
	matches := timeTokenPattern.FindAllStringSubmatch(raw, -1)
	if len(matches) == 0 {
		return 0, false
	}
	total := 0
	for _, m := range matches {
		value, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		total += value * timeUnits[strings.ToLower(m[2])]
	}
	if total <= 0 {
		return 0, false
	}
	return total, true
}
