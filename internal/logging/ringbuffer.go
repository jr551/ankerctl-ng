package logging

import (
	"context"
	"log/slog"
	"strings"
	"sync"
)

// RingBuffer is a thread-safe, fixed-capacity circular buffer of log lines.
// It is designed to be embedded as an slog.Handler so that all structured
// log records are captured in memory and can be served via the debug log viewer.
type RingBuffer struct {
	mu     sync.RWMutex
	buf    []string
	ids    []int // parallel slice: monotonic ID per slot
	cap    int
	head   int // index of the oldest entry (write head wraps here)
	size   int // number of valid entries currently stored
	nextID int // next ID to assign (starts at 1)
}

// LogEntry is a single console log line with a stable monotonic ID.
type LogEntry struct {
	ID   int    `json:"id"`
	Text string `json:"text"`
}

// Snapshot returns a Python-compatible console log response.
// limit caps the number of returned entries (1–maxLines).
// afterID, if >= 0, returns only entries with ID > afterID.
// Pass afterID = -1 to get the most recent `limit` entries (same as after_id=None).
type SnapshotResult struct {
	Entries   []LogEntry `json:"entries"`
	FirstID   int        `json:"first_id"`
	LastID    int        `json:"last_id"`
	NextAfter int        `json:"next_after"`
	Truncated bool       `json:"truncated"`
	MaxLines  int        `json:"max_lines"`
}

// NewRingBuffer creates a RingBuffer with the given capacity.
// Capacity must be > 0; if 0 or negative, it is set to 1.
func NewRingBuffer(capacity int) *RingBuffer {
	if capacity <= 0 {
		capacity = 1
	}
	return &RingBuffer{
		buf:    make([]string, capacity),
		ids:    make([]int, capacity),
		cap:    capacity,
		nextID: 1,
	}
}

// Append adds a single line to the buffer, evicting the oldest entry when full.
func (r *RingBuffer) Append(line string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.buf[r.head] = line
	r.ids[r.head] = r.nextID
	r.nextID++
	r.head = (r.head + 1) % r.cap
	if r.size < r.cap {
		r.size++
	}
}

// Lines returns all stored lines in chronological order (oldest first).
// The returned slice is a copy; it is safe to modify.
func (r *RingBuffer) Lines() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.size == 0 {
		return nil
	}
	out := make([]string, r.size)
	if r.size < r.cap {
		// Buffer not yet full: entries start at index 0.
		copy(out, r.buf[:r.size])
	} else {
		// Buffer is full: oldest entry is at head.
		n := copy(out, r.buf[r.head:])
		copy(out[n:], r.buf[:r.head])
	}
	return out
}

// Tail returns the last n lines in chronological order.
// If n >= the number of stored lines, all lines are returned.
func (r *RingBuffer) Tail(n int) []string {
	all := r.Lines()
	if n <= 0 || len(all) <= n {
		return all
	}
	return all[len(all)-n:]
}

// String returns all stored lines joined by newlines.
func (r *RingBuffer) String() string {
	return strings.Join(r.Lines(), "\n")
}

// Entries returns all stored entries in chronological order (oldest first).
// Each entry carries its monotonic ID and the line text.
func (r *RingBuffer) Entries() []LogEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.size == 0 {
		return nil
	}
	out := make([]LogEntry, r.size)
	if r.size < r.cap {
		for i := 0; i < r.size; i++ {
			out[i] = LogEntry{ID: r.ids[i], Text: r.buf[i]}
		}
	} else {
		n := 0
		for i := r.head; i < r.cap; i++ {
			out[n] = LogEntry{ID: r.ids[i], Text: r.buf[i]}
			n++
		}
		for i := 0; i < r.head; i++ {
			out[n] = LogEntry{ID: r.ids[i], Text: r.buf[i]}
			n++
		}
	}
	return out
}

// Snapshot returns a Python-compatible console-log response.
// limit caps the returned entries (clamped to 1–capacity).
// afterID ≥ 0 returns only entries with ID > afterID (polling mode).
// afterID < 0 returns the most recent `limit` entries (initial-load mode).
func (r *RingBuffer) Snapshot(limit int, afterID int) SnapshotResult {
	all := r.Entries()

	if limit < 1 {
		limit = 1
	}
	if limit > r.cap {
		limit = r.cap
	}

	firstID, lastID := 0, 0
	if len(all) > 0 {
		firstID = all[0].ID
		lastID = all[len(all)-1].ID
	}

	var selected []LogEntry
	truncated := false

	if afterID < 0 {
		// Initial load: return the most recent `limit` entries.
		if len(all) > limit {
			selected = all[len(all)-limit:]
		} else {
			selected = all
		}
	} else {
		// Polling: return entries with ID > afterID.
		if len(all) > 0 && afterID < firstID-1 {
			truncated = true
		}
		for _, e := range all {
			if e.ID > afterID {
				selected = append(selected, e)
			}
		}
		if len(selected) > limit {
			truncated = true
			selected = selected[len(selected)-limit:]
		}
	}

	if selected == nil {
		selected = []LogEntry{}
	}

	return SnapshotResult{
		Entries:   selected,
		FirstID:   firstID,
		LastID:    lastID,
		NextAfter: lastID,
		Truncated: truncated,
		MaxLines:  r.cap,
	}
}

// --- slog.Handler implementation ---

// RingBufferHandler wraps an inner slog.Handler and also writes formatted
// records to a RingBuffer.  It satisfies the slog.Handler interface.
type RingBufferHandler struct {
	inner slog.Handler
	ring  *RingBuffer
}

// NewRingBufferHandler wraps inner and captures all records to ring.
// If inner is nil, records are only captured (no further output).
func NewRingBufferHandler(inner slog.Handler, ring *RingBuffer) *RingBufferHandler {
	return &RingBufferHandler{inner: inner, ring: ring}
}

// Enabled reports whether both this handler and the inner handler are enabled
// for the given level.
func (h *RingBufferHandler) Enabled(ctx context.Context, level slog.Level) bool {
	if h.inner != nil {
		return h.inner.Enabled(ctx, level)
	}
	return true
}

// Handle captures the formatted record to the ring buffer then delegates to inner.
func (h *RingBufferHandler) Handle(ctx context.Context, r slog.Record) error {
	// Format a compact text line: "TIME LEVEL MSG key=val ..."
	var sb strings.Builder
	sb.WriteString(r.Time.Format("2006-01-02T15:04:05.000"))
	sb.WriteByte(' ')
	sb.WriteString(r.Level.String())
	sb.WriteByte(' ')
	sb.WriteString(r.Message)
	r.Attrs(func(a slog.Attr) bool {
		sb.WriteByte(' ')
		sb.WriteString(a.Key)
		sb.WriteByte('=')
		sb.WriteString(a.Value.String())
		return true
	})
	h.ring.Append(sb.String())

	if h.inner != nil {
		return h.inner.Handle(ctx, r)
	}
	return nil
}

// WithAttrs returns a new handler whose records include the given attrs.
func (h *RingBufferHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	var inner slog.Handler
	if h.inner != nil {
		inner = h.inner.WithAttrs(attrs)
	}
	return &RingBufferHandler{inner: inner, ring: h.ring}
}

// WithGroup returns a new handler that scopes future attrs under name.
func (h *RingBufferHandler) WithGroup(name string) slog.Handler {
	var inner slog.Handler
	if h.inner != nil {
		inner = h.inner.WithGroup(name)
	}
	return &RingBufferHandler{inner: inner, ring: h.ring}
}
