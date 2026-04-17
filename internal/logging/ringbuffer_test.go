package logging

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRingBuffer_BasicAppendAndLines(t *testing.T) {
	r := NewRingBuffer(4)

	if got := r.Lines(); len(got) != 0 {
		t.Fatalf("expected empty slice, got %v", got)
	}

	r.Append("a")
	r.Append("b")
	r.Append("c")

	got := r.Lines()
	want := []string{"a", "b", "c"}
	if !sliceEqual(got, want) {
		t.Fatalf("Lines() = %v, want %v", got, want)
	}
}

func TestRingBuffer_Wraps(t *testing.T) {
	r := NewRingBuffer(3)
	r.Append("1")
	r.Append("2")
	r.Append("3")
	r.Append("4") // evicts "1"

	got := r.Lines()
	want := []string{"2", "3", "4"}
	if !sliceEqual(got, want) {
		t.Fatalf("after wrap Lines() = %v, want %v", got, want)
	}
}

func TestRingBuffer_Tail(t *testing.T) {
	r := NewRingBuffer(10)
	for i := 0; i < 7; i++ {
		r.Append(fmt.Sprintf("line%d", i))
	}

	tail := r.Tail(3)
	want := []string{"line4", "line5", "line6"}
	if !sliceEqual(tail, want) {
		t.Fatalf("Tail(3) = %v, want %v", tail, want)
	}

	all := r.Tail(100)
	if len(all) != 7 {
		t.Fatalf("Tail(100) len = %d, want 7", len(all))
	}
}

func TestRingBuffer_ConcurrentSafe(t *testing.T) {
	r := NewRingBuffer(100)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			r.Append(fmt.Sprintf("goroutine-%d", n))
		}(i)
	}
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = r.Lines()
		}()
	}
	wg.Wait()
}

func TestRingBufferHandler_CapturesRecords(t *testing.T) {
	ring := NewRingBuffer(50)
	h := NewRingBufferHandler(nil, ring)
	logger := slog.New(h)

	logger.Info("hello world")
	logger.Warn("something happened", "key", "value")

	lines := ring.Lines()
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %v", len(lines), lines)
	}
	if !strings.Contains(lines[0], "hello world") {
		t.Errorf("line[0] should contain 'hello world': %q", lines[0])
	}
	if !strings.Contains(lines[1], "something happened") {
		t.Errorf("line[1] should contain 'something happened': %q", lines[1])
	}
	if !strings.Contains(lines[1], "key=value") {
		t.Errorf("line[1] should contain 'key=value': %q", lines[1])
	}
}

func TestRingBufferHandler_WithAttrs(t *testing.T) {
	ring := NewRingBuffer(10)
	base := NewRingBufferHandler(nil, ring)
	child := base.WithAttrs([]slog.Attr{slog.String("svc", "test")})
	logger := slog.New(child)
	logger.Info("msg")

	lines := ring.Lines()
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
}

func TestRingBufferHandler_Enabled(t *testing.T) {
	ring := NewRingBuffer(10)
	h := NewRingBufferHandler(nil, ring)
	// Without an inner handler all levels should be reported enabled.
	if !h.Enabled(nil, slog.LevelDebug) {
		t.Error("expected Enabled(Debug) = true for nil inner")
	}
}

func TestRingBuffer_ZeroCapacity(t *testing.T) {
	// Cap < 1 must not panic.
	r := NewRingBuffer(0)
	r.Append("x")
	lines := r.Lines()
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
}

func TestRingBuffer_TailEmpty(t *testing.T) {
	r := NewRingBuffer(5)
	if tail := r.Tail(3); len(tail) != 0 {
		t.Fatalf("empty buffer Tail should return nil/empty, got %v", tail)
	}
}

func TestRingBufferHandler_FormatsTimestamp(t *testing.T) {
	ring := NewRingBuffer(5)
	h := NewRingBufferHandler(nil, ring)
	rec := slog.NewRecord(time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC), slog.LevelInfo, "ts-test", 0)
	_ = h.Handle(nil, rec)

	lines := ring.Lines()
	if !strings.HasPrefix(lines[0], "2025-01-02T03:04:05") {
		t.Errorf("expected timestamp prefix, got %q", lines[0])
	}
}

// ---- Snapshot / Entries tests ----

func TestRingBuffer_Entries_MonotonicIDs(t *testing.T) {
	r := NewRingBuffer(5)
	r.Append("a")
	r.Append("b")
	r.Append("c")

	entries := r.Entries()
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
	for i, e := range entries {
		wantID := i + 1
		if e.ID != wantID {
			t.Errorf("entries[%d].ID = %d, want %d", i, e.ID, wantID)
		}
	}
}

func TestRingBuffer_Snapshot_InitialLoad(t *testing.T) {
	r := NewRingBuffer(10)
	for i := 0; i < 7; i++ {
		r.Append(fmt.Sprintf("line%d", i))
	}

	res := r.Snapshot(3, -1)
	if len(res.Entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(res.Entries))
	}
	if res.Entries[0].Text != "line4" {
		t.Errorf("first of last-3 should be line4, got %q", res.Entries[0].Text)
	}
	if res.Truncated {
		t.Error("Truncated should be false for initial load with enough capacity")
	}
	if res.MaxLines != 10 {
		t.Errorf("MaxLines = %d, want 10", res.MaxLines)
	}
}

func TestRingBuffer_Snapshot_Polling(t *testing.T) {
	r := NewRingBuffer(20)
	for i := 0; i < 5; i++ {
		r.Append(fmt.Sprintf("msg%d", i))
	}

	// Get first batch: entries 1–5.
	res := r.Snapshot(100, -1)
	if len(res.Entries) != 5 {
		t.Fatalf("initial snapshot: expected 5, got %d", len(res.Entries))
	}
	after := res.NextAfter // == 5

	// Append more; poll for new entries only.
	r.Append("msg5")
	r.Append("msg6")

	res2 := r.Snapshot(100, after)
	if len(res2.Entries) != 2 {
		t.Fatalf("poll snapshot: expected 2 new entries, got %d", len(res2.Entries))
	}
	if res2.Entries[0].Text != "msg5" {
		t.Errorf("first new entry = %q, want msg5", res2.Entries[0].Text)
	}
}

func TestRingBuffer_Snapshot_TruncationFlag(t *testing.T) {
	r := NewRingBuffer(10)
	for i := 0; i < 10; i++ {
		r.Append(fmt.Sprintf("x%d", i))
	}
	// Wrap around, losing older entries.
	for i := 0; i < 3; i++ {
		r.Append(fmt.Sprintf("y%d", i))
	}

	// Poll from an id that was evicted.
	res := r.Snapshot(100, 1)
	if !res.Truncated {
		t.Error("expected Truncated=true when after_id points to evicted entry")
	}
}

func TestRingBuffer_Snapshot_LimitClamped(t *testing.T) {
	r := NewRingBuffer(5)
	r.Append("a")
	r.Append("b")
	r.Append("c")

	res := r.Snapshot(0, -1) // limit=0 → clamped to 1
	if len(res.Entries) != 1 {
		t.Fatalf("limit=0 should return 1 entry, got %d", len(res.Entries))
	}
}

func TestRingBuffer_Snapshot_Empty(t *testing.T) {
	r := NewRingBuffer(100)
	res := r.Snapshot(200, -1)
	if len(res.Entries) != 0 {
		t.Fatalf("empty buffer should return 0 entries, got %d", len(res.Entries))
	}
	if res.FirstID != 0 || res.LastID != 0 {
		t.Errorf("empty: FirstID=%d LastID=%d, want 0,0", res.FirstID, res.LastID)
	}
}

// sliceEqual compares two string slices element by element.
func sliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
