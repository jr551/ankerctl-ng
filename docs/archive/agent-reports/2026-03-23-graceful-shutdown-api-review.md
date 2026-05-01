# QA Report -- feat/graceful-shutdown-api
Date: 2026-03-23
Reviewer: go-qa-senior-reviewer
Files Reviewed:
- `internal/web/handler/shutdown.go` (new)
- `internal/web/handler/handler.go` (modified)
- `internal/web/handler/handler_test.go` (modified)
- `internal/web/server.go` (modified)
- `internal/web/routes.go` (modified)
- `cmd/ankerctl/main.go` (modified)
- `internal/web/static/ankersrv.js` (modified)
- `internal/web/static/tabs/debug.html` (modified)

Test Results: PASS -- `go test ./... -race -count=1` all 21 suites pass, `go vet ./...` clean.
Commits: 2 (`77b0dd7`, `02c3994`)

## Executive Summary

This branch adds a graceful server shutdown endpoint (`POST /api/ankerctl/server/shutdown`) that can be triggered via the web UI debug tab or a Ctrl+Shift+Q keyboard shortcut. The implementation is clean, well-structured, and race-safe. Overall quality is high (74/80). The one issue that should be addressed before merge is that the keyboard shortcut Ctrl+Shift+Q fires on all pages regardless of whether debug mode is enabled, which could surprise users or cause accidental shutdowns in production.

## Quality Score

| Category              | Score | Notes |
|---|---|---|
| Correctness           | 10/10 | Handler responds 200 before triggering shutdown; nil-trigger guard; sync.Once idempotency |
| Security              | 10/10 | POST route = API key required by auth middleware; no secrets logged; no new attack surface |
| Architecture          | 10/10 | ShutdownTrigger interface breaks circular dep cleanly; handler -> server via interface, not import |
| Idiomatic Go          | 9/10  | Minor: `shutdownOnce` not grouped with `shutdownCh` on same alignment block |
| Test Coverage         | 9/10  | 3 tests cover happy path, trigger invocation, and nil-trigger safety; no negative auth test |
| Performance           | 10/10 | Channel close + sync.Once -- zero overhead |
| Readability           | 9/10  | All exported symbols documented; GoDoc comments clear and accurate |
| Protocol Exactness    | 7/10  | N/A (no protocol code); keyboard shortcut comment claims debug-only but code is unconditional |
| **Total**             | **74/80** | **93%** |

## Findings

### 🟠 HOCH (should fix before merge)

**H-001** `internal/web/static/ankersrv.js:3752-3765` -- Keyboard shortcut Ctrl+Shift+Q is registered unconditionally

Problem: The `document.addEventListener("keydown", ...)` block at the bottom of `ankersrv.js` registers the Ctrl+Shift+Q shortcut on every page load, regardless of whether `DebugMode` is true. The JSDoc comment says "Only active when the debug tab is available (DebugMode=true)" but this is not enforced in code. Any authenticated user can trigger server shutdown via keyboard even when debug mode is off. The button itself is correctly gated (only rendered in `debug.html` which is conditionally included), but the keyboard shortcut bypasses this.

Risk: Accidental server shutdown in production by any authenticated user pressing Ctrl+Shift+Q. While the `confirm()` dialog provides a safety net, the shortcut should not be active when debug mode is disabled.

Fix: Guard the keydown listener behind a debug mode check. Two options:
1. Check for the existence of the debug tab element: `if (!document.getElementById("dbg-server-shutdown")) return;`
2. Use the template-injected `DebugMode` variable if available in the JS scope.

### 🟡 MITTEL (fix in follow-up)

**M-001** `internal/web/server.go:83-84` -- Struct field alignment inconsistency

Problem: `shutdownOnce sync.Once` is not aligned with the same indentation style as the preceding field `shutdownCh chan struct{}`. The `shutdownOnce` field lacks a leading tab to match.

Risk: No functional impact. Cosmetic only.

Fix: Align `shutdownOnce` with `shutdownCh`:
```go
shutdownCh   chan struct{}
shutdownOnce sync.Once
```

**M-002** `internal/web/handler/handler_test.go` -- No test for auth enforcement on shutdown route

Problem: Tests verify handler behavior (200 response, trigger called, nil-trigger safety) but do not test that the route is properly auth-protected. A regression in the auth middleware could expose the shutdown endpoint without API key validation.

Risk: Low -- auth is middleware-level and tested separately in `middleware/auth_test.go`. But an integration-level test for the shutdown route specifically would add confidence.

Fix: Add an integration test using chi router + auth middleware that verifies `POST /api/ankerctl/server/shutdown` returns 401 without an API key.

### 🔵 NIEDRIG (nice-to-have)

**N-001** `internal/web/handler/handler_test.go:187-193` -- Unused `mockShutdownTrigger`

Problem: `mockShutdownTrigger` with its `called` field is defined but `TestServerShutdown_Returns200WithMessage` uses it only to satisfy the interface. The `called` field is never asserted. The actual trigger-called verification uses `shutdownTriggerFunc` with a channel in `TestServerShutdown_TriggersCalled`. The mock could be removed and replaced with `shutdownTriggerFunc` in the first test too.

Risk: No functional impact. Minor dead code in tests.

**N-002** `cmd/ankerctl/main.go` -- Duplicated shutdown logic across select cases

Problem: The `ctx.Done()` and `srv.ShutdownCh()` cases have nearly identical bodies (call `sm.Shutdown()`, sleep 500ms, return nil). The only difference is `stop()` call and the log message.

Risk: If shutdown logic changes, two places must be updated. Acceptable for 6 lines.

## Positive Observations

1. **Excellent interface design**: The `ShutdownTrigger` interface in the handler package cleanly breaks the circular dependency between `handler` and `web` packages. This follows the established `StateReloader` / `VideoSupportChecker` pattern.

2. **Race safety**: `sync.Once` + channel close is the textbook-correct pattern for one-shot signaling across goroutines. The goroutine in `ServerShutdown` is trivially short-lived (just calls `close(chan)` under `sync.Once`), eliminating any goroutine leak risk.

3. **Defensive nil-check**: The handler gracefully handles the case where no `ShutdownTrigger` is set, avoiding a nil-pointer panic. This is tested explicitly.

4. **Frontend UX**: The `confirm()` dialog, dual error handling (response error vs. network error treated as expected), and the 10-second flash message are well-thought-out. The `btn-outline-danger` styling clearly communicates the destructive nature of the action.

5. **Auth compliance**: POST route is correctly protected by the existing auth middleware. No exemptions needed or added.

## Required Actions Before Merge

1. **Fix H-001**: Guard the Ctrl+Shift+Q keyboard shortcut so it only activates when debug mode is enabled.

## Recommended Follow-Up (post-merge)

1. Add integration-level auth test for the shutdown endpoint (M-002).
2. Clean up struct field alignment in `server.go` (M-001).
3. Consider removing the unused `mockShutdownTrigger` struct (N-001).
