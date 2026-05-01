# Final Gaps Implementation Plan (2026-03-25)

**Status**: 76/80 (95%) -- 7 open items + 3 test gaps to close
**Goal**: Reach 80/80 (100%) feature parity with Python reference

---

## Executive Summary

The remaining work falls into three tiers ordered by impact and dependency:

| Tier | Items | Effort | Risk |
|------|-------|--------|------|
| A: Protocol/Concurrency | H-002 (pppp-state -- ALREADY DONE), M-003 (Wire channels) | 2-3d | Medium |
| B: Code Quality/Security | H-004 (VideoQueue interfaces), M-001 (DUID redact), N-001 (email PII), N-004 (video tap) | 1d | Low |
| C: Feature Gap | H-005 (bed leveling live) | 1-2d | Medium |
| D: Test Gaps | PPPP upload integration, discoverAndPersist, pppp-state probe | 1-2d | Low |

Total estimated effort: **5-8 developer-days**

---

## H-002 Assessment: ALREADY IMPLEMENTED

After reading `internal/web/ws/pppp.go`, H-002 is **already fully implemented** and matches the Python reference exactly:

| Python Feature | Go Implementation | Status |
|---|---|---|
| Shared probe thread (`_maybe_start_pppp_probe`) | `startProbe()` goroutine with `ppppProbe.running` guard | DONE |
| `MAX_RETRIES=2`, `RETRY_INTERVAL=15`, `PROBE_INTERVAL=60` | `ppppMaxRetries=2`, `ppppRetryInterval=15s`, `ppppProbeInterval=60s` | DONE |
| `MQTT_STALE_AFTER=30` | `ppppMQTTStaleAfter=30s` | DONE |
| `probe_fail_count` with backoff | `ppppProbe.failCount` with `nextInterval` switch at `> ppppMaxRetries` | DONE |
| `client_count` ref-counting | `ppppProbe.clientCount` with Lock | DONE |
| Immediate probe on first client | `isFirst` check triggers `startProbe()` | DONE |
| MQTT staleness detection + recovery reset | `mqttWasStale`/`mqttRecovered` logic, resets `result`/`failCount` | DONE |
| `pppp_went_dormant` re-probe | `ppppWentDormant := wasConnected && probeResult == nil` | DONE |
| Keepalive every 10s while connected | `ppppKeepaliveEvery=10s`, `lastKeepalive` | DONE |

**Conclusion**: H-002 was resolved in a prior session (QA sprint 2026-03-10 notes confirm "H-002 (PPPP state probe/retry) was already implemented"). No further work needed.

**Remaining items: 6 code fixes + 3 test gaps = 9 items total.**

---

## Phase 1: Concurrency Fix (M-003)

**Priority**: Medium -- affects video throughput under load
**Branch**: `fix/wire-channel-reads`
**Agent**: `go-network-systems-engineer`
**Files**: `internal/pppp/protocol/channel.go`

### Problem

`Wire.Peek` and `Wire.Read` use `sync.Cond` with manual timer-based broadcast:

```go
// Current (channel.go:65-70):
timer := time.AfterFunc(remaining, func() {
    w.mu.Lock()
    w.cv.Broadcast()
    w.mu.Unlock()
})
w.cv.Wait()
```

This pattern:
1. Cannot be cancelled via `context.Context` -- goroutine leaks on shutdown
2. Has contention under high video frame rates (all waiters wake on every broadcast)
3. The `AfterFunc` goroutine races with `timer.Stop()` on fast paths

### Solution

Replace `sync.Cond` with channel-based signaling:

```go
type Wire struct {
    mu     sync.Mutex
    buf    []byte
    notify chan struct{} // buffered(1), signalled on Write
}
```

- `Peek(size, timeout)` -> select on `notify` channel + `time.After(remaining)` in a loop
- `Read(size, timeout)` -> same, but consumes from buf
- Add `PeekContext(ctx, size)` and `ReadContext(ctx, size)` variants that accept `context.Context`
- `Write` does non-blocking send to `notify` channel

### Test Requirements

- Extend `internal/pppp/protocol/channel_test.go`:
  - `TestWirePeekContextCancellation` -- context cancel returns nil before timeout
  - `TestWireReadConcurrentWriters` -- 10 goroutines writing, 1 reading, no data loss
  - `TestWirePeekTimeout` -- returns nil after exact timeout (within 50ms tolerance)
  - `TestWireReadContextDeadline` -- deadline exceeded returns nil

### Acceptance Criteria

1. All existing `channel_test.go` tests still pass
2. No `sync.Cond` usage remains in the file
3. Context cancellation is testable and deterministic
4. `go vet ./internal/pppp/protocol/...` clean

---

## Phase 2: Code Quality Fixes (H-004, N-004, N-001, M-001)

These are independent, low-risk fixes that can be done in parallel on a single branch.

**Branch**: `fix/code-quality-batch`
**Agent**: `go-migration-architect`

### 2a: H-004 -- VideoQueue typed interfaces

**File**: `internal/service/videoqueue.go:322-350`

**Problem**: Two inline interface definitions (`ppppLifecycle`, `videoHandlerRegistrar`) inside `WorkerStart()` are fragile -- if the underlying types change, the type assertions silently fail. The `time.Sleep(100ms)` poll loop is not cancellable.

**Fix**:
1. Extract interfaces to package-level named types in `videoqueue.go`:

```go
// PPPPLifecycle controls the PPPP service start/connect lifecycle.
type PPPPLifecycle interface {
    Start(context.Context)
    IsConnected() bool
    State() RunState
}

// VideoHandlerRegistrar allows registering a video frame callback.
type VideoHandlerRegistrar interface {
    RegisterVideoHandler(func(protocol.VideoFrame))
}
```

2. Add these as explicit fields on `VideoQueue` (set via constructor or setter):

```go
type VideoQueue struct {
    // ...
    ppppLifecycle PPPPLifecycle
    videoRegistrar VideoHandlerRegistrar
    // ...
}
```

3. Replace `time.Sleep(100ms)` with a `time.Ticker` + context select:

```go
ticker := time.NewTicker(100 * time.Millisecond)
defer ticker.Stop()
deadline := time.After(6 * time.Second)
for !q.ppppLifecycle.IsConnected() {
    select {
    case <-ctx.Done():
        return ctx.Err()
    case <-deadline:
        return errors.New("videoqueue: ppppservice connection timeout")
    case <-ticker.C:
        if q.ppppLifecycle.State() == StateStopped {
            return errors.New("videoqueue: ppppservice stopped during startup")
        }
    }
}
```

**Test Requirements**:
- `TestVideoQueueWorkerStartCancellation` -- cancel context during PPPP wait, verify clean exit
- `TestVideoQueueWorkerStartPPPPStopped` -- mock returns `StateStopped`, verify error

### 2b: N-004 -- Dual type-switch in video tap callback

**File**: `internal/web/handler/general.go:217-232`

**Problem**: The `/video` handler's `Tap` callback handles both `service.VideoFrameEvent` and `[]byte`, but the VideoQueue only emits `VideoFrameEvent` (see `videoqueue.go:410`). The `[]byte` branch is dead code that creates confusion.

**Fix**: Remove the `case []byte:` branch. Only handle `service.VideoFrameEvent`, consistent with `internal/web/ws/video.go:53`.

**Test Requirements**: None -- this is dead code removal. Existing `TestVideoHandler` covers the live path.

### 2c: N-001 -- ConfigLogin logs email as cleartext

**File**: `internal/web/handler/config.go:238`

**Current**: `slog.Info("cloud login successful", "email", logging.RedactEmail(email), ...)` -- this is ALREADY using `RedactEmail`.

**Also check** `internal/web/handler/general.go:111`: `a.Email` is printed unredacted in the `configShow` output (the `/api/ankerctl/config/show` endpoint).

**Fix**: Replace `a.Email` with `logging.RedactEmail(a.Email)` in the `configShow` function at line 111. This is the actual PII leak -- the show endpoint returns the full email in the response body and in slog output.

**Test Requirements**: Unit test `TestConfigShowRedactsEmail` -- verify output contains masked email, not full email.

### 2d: M-001 -- DUID not consistently redacted

**File**: `internal/service/pppp.go`

**Current state**: Lines 112, 127 already use `logging.RedactID(printer.P2PDUID, 4)`. Good.

**Audit remaining log sites**: Grep for any `P2PDUID` or `duid` in slog calls that are NOT redacted.

**Fix**: Any remaining unredacted DUID log entries get wrapped in `logging.RedactID(..., 4)`.

**Test Requirements**: None beyond the existing `TestRedact` and `TestRedactID` tests in `internal/logging/redact_test.go`.

### Acceptance Criteria (Phase 2)

1. `go test ./internal/service/... ./internal/web/handler/...` all pass
2. No inline type assertions for PPPP lifecycle in VideoQueue
3. No unredacted PII (email, DUID) in log output
4. `go vet ./...` clean

---

## Phase 3: Bed Leveling Live (H-005)

**Priority**: Medium -- functional gap
**Branch**: `feat/bed-leveling-live-parsing`
**Agent**: `go-network-systems-engineer`
**Files**: `internal/service/mqttqueue.go`, `internal/web/handler/bedlevel.go`

### Current State

The handler and service method already exist:
- `handler.BedLevelingLive` calls `q.QueryBedLeveling(ctx)` (bedlevel.go:14)
- `MqttQueue.QueryBedLeveling` sends "M420 V" GCode and collects ct=1043 responses for 4s (mqttqueue.go:779+)
- `blGridRe` regex parses `BL-Grid-N` lines (mqttqueue.go:777)
- `LastBedLevelingGrid()` returns persisted grid (mqttqueue.go:770)

### What Needs Verification

The QA report flagged this as a "stub" but the implementation appears to be present. We need to:

1. **Verify M420 V response parsing** against actual printer output
2. **Verify grid statistics** (min/max/mean) calculation matches Python
3. **Verify persistence** -- grid is saved to `bedLevelingGrid` field and survives service restarts
4. **Cross-reference Python** `_read_bed_leveling_grid()` for edge cases

### Action

Read the full `QueryBedLeveling` implementation, compare line-by-line with Python, and:
- If complete: mark H-005 as DONE, add test coverage
- If incomplete: implement missing parsing/statistics

### Test Requirements

- `TestQueryBedLevelingParsing` -- feed mock ct=1043 responses with known BL-Grid lines, verify parsed grid
- `TestBedLevelingGridStatistics` -- verify min/max/mean calculations
- `TestBedLevelingEmptyResponse` -- no BL-Grid lines, verify graceful empty response

### Acceptance Criteria

1. `QueryBedLeveling` produces identical JSON structure to Python `_read_bed_leveling_grid()`
2. Grid statistics (min, max, mean, std) match Python within float64 precision
3. Empty/malformed responses return meaningful errors

---

## Phase 4: Test Gaps

**Branch**: `test/integration-gaps`
**Agent**: `go-network-systems-engineer` (PPPP upload), `go-migration-architect` (discovery, pppp-state)

### 4a: PPPP File Upload Integration Test

**File**: `internal/pppp/client/upload_test.go` (new)

**Scope**: Test the full upload sequence:
1. JSON handshake (file metadata)
2. 32KB block chunking
3. Progress reporting
4. Retry on DRW failure (Channel.ResetTx)
5. Completion acknowledgment

**Approach**: Mock UDP transport (in-memory `net.PacketConn`), feed scripted responses, verify:
- Block sequence numbers are sequential
- Progress events fire at correct percentages
- Retry after simulated packet loss recovers

### 4b: `discoverAndPersistPrinterIPs` Test

**File**: `internal/web/handler/config_test.go` (extend)

**Scope**: Test the background goroutine:
1. Multiple printers, some with DUID, some without
2. Mock LAN broadcast returns IP for one printer
3. Verify config file is updated with discovered IP
4. Verify DB cache is updated
5. Timeout after no response for a printer

**Approach**: Inject mock discovery function, verify side effects on config and DB.

### 4c: `/ws/pppp-state` Probe Logic Test

**File**: `internal/web/ws/pppp_test.go` (new)

**Scope**: Test the full probe state machine:
1. First client triggers immediate probe
2. Successful probe emits "connected"
3. Failed probe emits "disconnected", increments failCount
4. After MAX_RETRIES, switches to long interval
5. MQTT staleness triggers re-probe
6. MQTT recovery resets probe state
7. ppppWentDormant triggers re-probe
8. Client disconnect decrements clientCount

**Approach**: Mock `ppppProbeState` and `mqttMessageTimeProvider`, inject into Handler, drive via goroutine-based fake WebSocket.

### Acceptance Criteria (Phase 4)

1. All new tests pass deterministically (no flaky timing)
2. Test coverage for PPPP upload path >= 80%
3. Probe state machine test covers all 8 scenarios listed above
4. `go test -race ./...` clean

---

## Implementation Schedule

```
Day 1-2: Phase 2 (code quality batch) -- go-migration-architect
         All 4 fixes are independent, small, and low-risk.
         Branch: fix/code-quality-batch
         Merge to main after go test pass.

Day 2-3: Phase 1 (Wire channel refactor) -- go-network-systems-engineer
         Depends on nothing. Higher risk due to protocol-level change.
         Branch: fix/wire-channel-reads
         Requires careful testing before merge.

Day 3-4: Phase 3 (bed leveling verification/completion) -- go-network-systems-engineer
         Depends on nothing. May turn out to be already done.
         Branch: feat/bed-leveling-live-parsing OR just tests if impl is complete.
         Merge to main after test pass.

Day 4-5: Phase 4 (test gaps) -- split between agents
         Depends on Phases 1-3 being merged (tests may use refactored interfaces).
         Branch: test/integration-gaps
         Merge to main after all tests pass + go test -race clean.
```

## Merge Order

```
1. fix/code-quality-batch        (no deps, low risk, builds confidence)
2. fix/wire-channel-reads        (no deps on #1, but merge after for clean main)
3. feat/bed-leveling-live-parsing (no deps, independent feature)
4. test/integration-gaps          (depends on #1 for VideoQueue interfaces, #2 for Wire)
```

All merges require:
- `go test ./...` passes
- `go vet ./...` clean
- No unredacted secrets in diff
- Commit messages follow `type(scope): subject` format

---

## Agent Assignment Summary

| Item | Agent | Branch | Est. Effort |
|------|-------|--------|-------------|
| H-004 VideoQueue interfaces | `go-migration-architect` | `fix/code-quality-batch` | 2h |
| N-004 Dead code removal | `go-migration-architect` | `fix/code-quality-batch` | 15min |
| N-001 Email PII redaction | `go-migration-architect` | `fix/code-quality-batch` | 30min |
| M-001 DUID redaction audit | `go-migration-architect` | `fix/code-quality-batch` | 30min |
| M-003 Wire channel refactor | `go-network-systems-engineer` | `fix/wire-channel-reads` | 4-6h |
| H-005 Bed leveling verify | `go-network-systems-engineer` | `feat/bed-leveling-live-parsing` | 2-4h |
| Test: PPPP upload | `go-network-systems-engineer` | `test/integration-gaps` | 3-4h |
| Test: discoverAndPersist | `go-migration-architect` | `test/integration-gaps` | 2h |
| Test: pppp-state probe | `go-migration-architect` | `test/integration-gaps` | 3h |

**Total: 5-8 developer-days across 2 agents working in parallel.**

---

## Risk Register

| Risk | Impact | Mitigation |
|------|--------|------------|
| Wire refactor breaks PPPP file transfer | High | Run existing channel_test.go + manual upload test before merge |
| VideoQueue interface change breaks service wiring | Medium | Constructor signature change is compile-time caught |
| Bed leveling already complete (wasted effort) | Low | Start with verification, only implement if gaps found |
| Flaky video test (TestVideoHandler) | Low | Pre-existing, tracked separately, not in scope |

---

## Post-Completion

After all 4 phases merge to main:
- Run full `go test -race ./...`
- Update MIGRATION_PLAN.md to 80/80 (100%)
- Tag `v0.9.0` release candidate
- Docker build verification
- Update agent memory with final status
