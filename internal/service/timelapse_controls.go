package service

import (
	"fmt"
	"time"
)

// TimelapseStatus describes the current capture state for the API.
type TimelapseStatus struct {
	// "idle" | "capturing" | "paused" | "assembling"
	State    string  `json:"state"`
	Filename string  `json:"filename,omitempty"`
	Frames   int     `json:"frames,omitempty"`
	// Seconds remaining in the resume window (only when State=="paused").
	ResumeWindowSec int `json:"resume_window_sec,omitempty"`
}

// Status returns a point-in-time snapshot of the timelapse capture state.
func (s *TimelapseService) Status() TimelapseStatus {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.active != nil {
		return TimelapseStatus{
			State:    "capturing",
			Filename: s.active.Filename,
			Frames:   s.active.FrameCtr,
		}
	}
	if s.resume != nil {
		remaining := int(time.Until(s.resume.Deadline).Seconds())
		if remaining < 0 {
			remaining = 0
		}
		return TimelapseStatus{
			State:           "paused",
			Filename:        s.resume.Filename,
			Frames:          s.resume.FrameCtr,
			ResumeWindowSec: remaining,
		}
	}
	return TimelapseStatus{State: "idle"}
}

// ManualStart triggers a timelapse capture for the given print filename.
// Returns the current filename or an error if a capture is already active,
// timelapse is disabled, or ffmpeg is not available.
//
// StartCapture itself is fire-and-forget (async command channel). ManualStart
// performs synchronous pre-flight checks for the conditions that would cause
// startCaptureLocked to silently no-op, so callers get an actionable error
// rather than a successful-looking response that produces no capture.
func (s *TimelapseService) ManualStart(filename string) (string, error) {
	s.mu.Lock()
	alreadyActive := s.active != nil
	enabled := s.enabled
	s.mu.Unlock()

	if alreadyActive {
		return "", fmt.Errorf("timelapse capture already in progress")
	}
	if !enabled {
		return "", fmt.Errorf("timelapse is disabled")
	}
	if err := ffmpegAvailable(); err != nil {
		return "", fmt.Errorf("timelapse: ffmpeg not available: %w", err)
	}
	if filename == "" {
		filename = "manual_" + time.Now().Format("20060102_150405")
	}
	s.StartCapture(filename)
	return filename, nil
}

// ManualPause suspends the active capture into a resumable state.
// Returns the current filename, or an error if no capture is active.
func (s *TimelapseService) ManualPause() (string, error) {
	s.mu.Lock()
	active := s.active
	s.mu.Unlock()

	if active == nil {
		return "", fmt.Errorf("no active timelapse capture to pause")
	}
	filename := active.Filename
	s.FinishCapture(false)
	return filename, nil
}

// ManualResume resumes a paused (resumable) capture.
// Returns the current filename, or an error if there is no resumable state.
func (s *TimelapseService) ManualResume() (string, error) {
	s.mu.Lock()
	resume := s.resume
	s.mu.Unlock()

	if resume == nil {
		return "", fmt.Errorf("no paused timelapse capture to resume")
	}
	filename := resume.Filename
	// Re-start with the same filename; startCaptureLocked will pick up resume state.
	s.StartCapture(filename)
	return filename, nil
}

// ManualStop finalises the active capture and triggers video assembly.
// Returns the current filename, or an error if no capture is active.
func (s *TimelapseService) ManualStop() (string, error) {
	s.mu.Lock()
	active := s.active
	s.mu.Unlock()

	if active == nil {
		return "", fmt.Errorf("no active timelapse capture to stop")
	}
	filename := active.Filename
	s.FinishCapture(true)
	return filename, nil
}

// ManualDismiss clears the resumable paused capture without assembling a video.
func (s *TimelapseService) ManualDismiss() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resume = nil
}
