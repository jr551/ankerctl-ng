package gcode

import (
	"encoding/base64"
	"regexp"
	"strconv"
	"strings"
)

// thumbnailBeginRe matches the PrusaSlicer/BambuStudio/Cura slicer thumbnail
// block header comment: "; thumbnail begin WxH [size]" or "; png begin WxH [size]".
// Both 'x' and '*' are valid dimension separators.
//
// Python reference: cli/util.py _THUMBNAIL_BEGIN_PATTERN
var thumbnailBeginRe = regexp.MustCompile(
	`(?i)^;\s*(?:thumbnail|png)\s+begin\s+(\d+)[x*](\d+)(?:\s+\d+)?`,
)

// thumbnailEndRe matches the thumbnail block footer: "; thumbnail end" or "; png end".
//
// Python reference: cli/util.py _THUMBNAIL_END_PATTERN
var thumbnailEndRe = regexp.MustCompile(`(?i)^;\s*(?:thumbnail|png)\s+end\b`)

// ExtractThumbnail extracts the largest embedded thumbnail from slicer GCode.
//
// Thumbnails are encoded as Base64-chunked PNG data inside GCode comment blocks
// delimited by "; thumbnail begin WxH" / "; thumbnail end" (PrusaSlicer, BambuStudio)
// or "; png begin WxH" / "; png end" (Cura). When multiple thumbnails exist the
// largest by pixel area is returned. Returns nil, nil when no thumbnail is found.
//
// Python reference: cli/util.py extract_gcode_thumbnail
func ExtractThumbnail(data []byte) ([]byte, error) {
	text := string(data)

	var (
		bestImage []byte
		bestArea = -1

		currentArea  int
		currentLines []string
		inBlock      bool
	)

	for _, rawLine := range strings.Split(text, "\n") {
		line := strings.TrimSpace(rawLine)

		if m := thumbnailBeginRe.FindStringSubmatch(line); m != nil {
			w, err1 := strconv.Atoi(m[1])
			h, err2 := strconv.Atoi(m[2])
			if err1 != nil || err2 != nil {
				continue
			}
			currentArea = w * h
			currentLines = currentLines[:0]
			inBlock = true
			continue
		}

		if inBlock && thumbnailEndRe.MatchString(line) {
			encoded := strings.TrimSpace(strings.Join(currentLines, ""))
			area := currentArea
			inBlock = false
			currentLines = currentLines[:0]
			currentArea = 0

			if encoded == "" {
				continue
			}
			imgBytes, err := base64.StdEncoding.DecodeString(encoded)
			if err != nil {
				// Python uses validate=False (loose decode); try RawStdEncoding as fallback.
				imgBytes, err = base64.RawStdEncoding.DecodeString(encoded)
				if err != nil {
					continue
				}
			}
			if len(imgBytes) > 0 && area > bestArea {
				bestImage = imgBytes
				bestArea = area
			}
			continue
		}

		if !inBlock {
			continue
		}

		// Strip leading semicolon from data lines: "; <base64chunk>"
		if strings.HasPrefix(line, ";") {
			line = strings.TrimSpace(line[1:])
		}
		if line != "" {
			currentLines = append(currentLines, line)
		}
	}

	return bestImage, nil
}
