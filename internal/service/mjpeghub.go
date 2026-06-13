package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
)

// MJPEG framing constants — JPEG SOI/EOI markers and reader buffer sizing.
const (
	mjpegReadChunkSize = 8 * 1024
	mjpegMaxBufferSize = 4 * 1024 * 1024
	mjpegFrameChanCap  = 4
)

var (
	jpegSOI = []byte{0xff, 0xd8}
	jpegEOI = []byte{0xff, 0xd9}
)

// rtspLowLatencyArgs mirrors the Python RTSP_LOW_LATENCY_INPUT_ARGS used to
// minimise startup latency for RTSP sources. Combined with -rtsp_transport tcp
// at the call site for RTSP URLs.
var rtspLowLatencyArgs = []string{"-fflags", "nobuffer", "-probesize", "32768", "-analyzeduration", "0"}

// MJPEGScale represents the target scaling resolution for an MJPEG stream.
// A zero-width or zero-height value disables the scale filter entirely.
type MJPEGScale [2]int

// scaleFilter returns the ffmpeg "-vf" expression for the given scale, or "".
// Mirrors Python web/camera.py _scale_filter().
func scaleFilter(scale MJPEGScale) string {
	if scale[0] <= 0 || scale[1] <= 0 {
		return ""
	}
	w, h := scale[0], scale[1]
	return fmt.Sprintf(
		"scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:(ow-iw)/2:(oh-ih)/2",
		w, h, w, h,
	)
}

// PrinterMJPEGCmd builds an *exec.Cmd that transcodes the printer's H.264
// /video stream into MJPEG on stdout. The command is bound to ctx so that
// cancelling ctx kills ffmpeg.
//
// videoURL must point to the local /video endpoint (e.g.
// "http://127.0.0.1:4470/video?for_timelapse=1"). When apiKey is non-empty it
// is sent as an X-Api-Key request header — the URL itself is never modified
// here, callers wishing to embed the key as a query parameter must do so
// explicitly. Mirrors Python web/camera.py open_printer_mjpeg_stream().
func PrinterMJPEGCmd(ctx context.Context, videoURL, apiKey string, fps, quality int, scale MJPEGScale) *exec.Cmd {
	if fps <= 0 {
		fps = 5
	}
	if quality <= 0 {
		quality = 5
	}

	args := []string{
		"-loglevel", "error",
		"-nostdin",
		"-f", "h264",
	}
	if apiKey != "" {
		// HTTP header lines must end with CRLF, per ffmpeg docs.
		args = append(args, "-headers", "X-Api-Key: "+apiKey+"\r\n")
	}
	args = append(args,
		"-i", videoURL,
		"-an", "-sn", "-dn",
		"-r", strconv.Itoa(fps),
	)
	if vf := scaleFilter(scale); vf != "" {
		args = append(args, "-vf", vf)
	}
	args = append(args,
		"-f", "image2pipe",
		"-vcodec", "mjpeg",
		"-q:v", strconv.Itoa(quality),
		"pipe:1",
	)

	return exec.CommandContext(ctx, "ffmpeg", args...)
}

// ExternalMJPEGCmd builds an *exec.Cmd that transcodes an external camera
// stream (RTSP/HTTP MJPEG/etc.) into MJPEG on stdout. The command is bound to
// ctx — cancelling it kills ffmpeg. Mirrors Python web/camera.py
// open_external_mjpeg_stream(). For RTSP URLs the low-latency input args
// (rtsp_transport tcp, nobuffer, probesize, analyzeduration) are added.
func ExternalMJPEGCmd(ctx context.Context, inputURL string, scale MJPEGScale) *exec.Cmd {
	return ExternalMJPEGCmdWithHeaders(ctx, inputURL, nil, scale)
}

// ExternalMJPEGCmdWithHeaders builds an external camera transcoder with optional
// HTTP input headers. Header values are passed to ffmpeg via -headers and are
// not embedded in the input URL.
func ExternalMJPEGCmdWithHeaders(ctx context.Context, inputURL string, headers map[string]string, scale MJPEGScale) *exec.Cmd {
	args := []string{
		"-loglevel", "error",
		"-nostdin",
	}
	if len(headers) > 0 {
		var b strings.Builder
		for k, v := range headers {
			k = strings.TrimSpace(k)
			v = strings.TrimSpace(v)
			if k == "" || v == "" || strings.ContainsAny(k, "\r\n:") || strings.ContainsAny(v, "\r\n") {
				continue
			}
			b.WriteString(k)
			b.WriteString(": ")
			b.WriteString(v)
			b.WriteString("\r\n")
		}
		if b.Len() > 0 {
			args = append(args, "-headers", b.String())
		}
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(inputURL)), "rtsp://") {
		args = append(args, "-rtsp_transport", "tcp")
		args = append(args, rtspLowLatencyArgs...)
	}
	args = append(args,
		"-i", inputURL,
		"-an", "-sn", "-dn",
	)
	if vf := scaleFilter(scale); vf != "" {
		args = append(args, "-vf", vf)
	}
	args = append(args,
		"-f", "image2pipe",
		"-vcodec", "mjpeg",
		"-q:v", "5",
		"pipe:1",
	)

	return exec.CommandContext(ctx, "ffmpeg", args...)
}

// ReadMJPEGFrames starts cmd and returns a channel that receives complete JPEG
// frames extracted from cmd's stdout. The function spawns one reader goroutine
// which terminates ffmpeg and closes the returned channel when:
//   - ctx is cancelled,
//   - stdout reaches EOF,
//   - the underlying ffmpeg process exits, or
//   - any read returns an unrecoverable error.
//
// The returned channel is buffered (mjpegFrameChanCap). If a consumer is too
// slow, the reader blocks until the consumer drains a frame — this provides
// natural backpressure. Cancelling ctx is the only way for a stuck consumer to
// release resources.
//
// All goroutines started by this function exit on ctx cancellation; there are
// no goroutine leaks.
func ReadMJPEGFrames(ctx context.Context, cmd *exec.Cmd) (<-chan []byte, error) {
	if cmd == nil {
		return nil, errors.New("mjpeghub: nil command")
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("mjpeghub: stdout pipe: %w", err)
	}

	// Discard ffmpeg stderr — mirrors Python's subprocess.DEVNULL choice. We
	// rely on cmd.Wait() exit status (and stdout EOF) to detect failures; the
	// stderr text is not surfaced to clients to avoid leaking credentials that
	// ffmpeg may echo from the input URL. Using a bounded sink (io.Discard)
	// also prevents unbounded buffer growth on long-running streams.
	cmd.Stderr = io.Discard

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("mjpeghub: start ffmpeg: %w", err)
	}

	frames := make(chan []byte, mjpegFrameChanCap)

	go func() {
		defer close(frames)
		// Ensure ffmpeg is reaped even on early returns. CommandContext also
		// kills it on ctx cancellation, but we still need to Wait() to release
		// OS resources.
		defer func() {
			_ = stdout.Close()
			// Wait for the process to exit so we don't leak zombies. ctx
			// cancellation has already SIGKILL'd it via CommandContext if
			// needed.
			_ = cmd.Wait()
		}()

		buf := make([]byte, 0, mjpegReadChunkSize*4)
		chunk := make([]byte, mjpegReadChunkSize)

		for {
			// Honour ctx cancellation before each read. CommandContext will
			// also kill the process, which makes stdout.Read return an error.
			if ctx.Err() != nil {
				return
			}

			n, readErr := stdout.Read(chunk)
			if n > 0 {
				buf = append(buf, chunk[:n]...)
				// Extract every complete JPEG currently in the buffer.
				for {
					start := bytes.Index(buf, jpegSOI)
					if start < 0 {
						// No SOI yet; if buffer is bloated keep only the last
						// byte (a partial 0xff that could start the next SOI).
						if len(buf) > mjpegMaxBufferSize {
							if len(buf) > 0 {
								buf = append(buf[:0], buf[len(buf)-1])
							}
						}
						break
					}
					end := bytes.Index(buf[start+2:], jpegEOI)
					if end < 0 {
						// SOI without EOI: drop bytes before SOI to bound the
						// buffer. If we still exceed the cap, reset to keep
						// the SOI marker only.
						if start > 0 {
							buf = append(buf[:0], buf[start:]...)
						}
						if len(buf) > mjpegMaxBufferSize {
							buf = buf[:0]
						}
						break
					}
					end += start + 2 // absolute index of EOI start
					frame := make([]byte, end+2-start)
					copy(frame, buf[start:end+2])

					select {
					case frames <- frame:
					case <-ctx.Done():
						return
					}
					// Trim everything up to and including this frame.
					buf = append(buf[:0], buf[end+2:]...)
				}
			}
			if readErr != nil {
				// EOF or any other read error => terminate.
				return
			}
		}
	}()

	return frames, nil
}

// SnapshotExternal grabs a single JPEG from inputURL using ffmpeg, writing it
// to outputPath. It mirrors the Python single-frame capture path used for
// external cameras (i.e. _run_ffmpeg_snapshot without the format hint, with
// optional RTSP low-latency args). Returns an error with credential-scrubbed
// stderr on failure.
func SnapshotExternal(ctx context.Context, inputURL, outputPath string) error {
	rtsp := strings.HasPrefix(strings.ToLower(strings.TrimSpace(inputURL)), "rtsp://")
	args := []string{"-loglevel", "error", "-nostdin", "-y"}
	if rtsp {
		args = append(args, "-rtsp_transport", "tcp")
		args = append(args, rtspLowLatencyArgs...)
	}
	args = append(args, "-i", inputURL, "-frames:v", "1", outputPath)

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg: %w (%s)", err, scrubURLCredentials(strings.TrimSpace(string(out))))
	}
	return nil
}
