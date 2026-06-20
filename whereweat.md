# PPPP Upload Hardening & Self-Healing

**Branch:** `ha-camera-ai-monitor`  ·  **Last worked:** 2026-06-20  ·  **Status:** working & deployed (one commit pending, see [Deploy status](#deploy-status))

Goal of this work: make PPPP file uploads to the AnkerMake M5 survive the printer's
Wi-Fi power saving, which silently drops the P2P session while the client still
believes it is connected. Before this work, uploads hung for 2+ minutes and died
with `context canceled`. They now detect the dead session, recover it, and — as a
last resort — power-cycle the printer and resume.

> All claims in this doc were verified against the source on 2026-06-20 with
> `file:line` references. They were accurate at that commit; re-check the line
> numbers if the files have moved since.

---

## TL;DR — what now keeps uploads alive

1. **The client proves the session is live**, instead of trusting `State()==Connected`.
   It sends its own `PingReq` keepalives every 5s and exposes `Healthy()`, which
   goes false if no pong arrives within 15s.
2. **The service acts on that signal**: it refuses to start/continue an upload on a
   stale session and restarts the PPPP session instead of blocking on a dead one.
3. **Blocked writes can be unblocked**: closing the client closes every channel, so
   a `WriteContext` waiting on ACKs from a dead session returns `ErrChannelClosed`
   rather than hanging forever.
4. **A persistent upload loop** holds the payload in memory and escalates: retry →
   restart session → power-cycle the printer → wait for it to boot → retry, within a
   hard 5-minute budget.
5. **The progress bar only moves forward** and the upload card only shows while an
   upload is actually running.

---

## The self-healing escalation ladder

This is the part that took the longest to get right — the order and the budgets matter.
`Upload()` in `internal/service/pppp.go` drives it:

```
Upload(payload held in memory)               total budget: 5 min
│
├─ attempt upload
│    └─ uploadWithRetries: 3 tries, 2s apart, on connection drops
│
├─ session unhealthy / dropped?
│    └─ Restart() the PPPP session, re-establish, retry
│
└─ still failing after retries?
     └─ PowerCycle the printer (HA smart socket: off → 10s → on)
        wait 30s for boot, then waitForPrinterRecovery() polls UDP 32108
        until the PPPP port answers, then retry
        ── up to 3 power-cycles, all inside the 5-min ceiling ──
```

Two independent health gates feed this ladder so a dead session is caught whether
an upload is in flight or not:

- **`waitConnected`** (entry gate): only returns a client when it is both
  `StateConnected` **and** `Healthy()`. A connected-but-stale session returns
  `errStaleSession` and forces a restart — we never start an upload onto a corpse.
- **`WorkerRun`** (background gate): polls `Healthy()` every 50ms and returns
  `ErrServiceRestartSignal` the moment the session goes stale, so idle sessions
  self-heal too.

---

## Root causes → fixes

| # | Root cause | Fix |
|---|------------|-----|
| 1 | **No client keepalives.** The Go client answered the printer's ALIVE pings but never sent its own, so Wi-Fi power saving dropped idle sessions undetected. | `PingReq` every 5s + `Healthy()` staleness detection. |
| 2 | **P2pRdy flood thrashing.** Printer sends ~20 `P2pRdy`/sec after handshake; each one was resetting `remoteAddr`/`state` and disrupting the session. | Always reply with `P2pRdyAck` (the printer expects one per packet), but only transition `Connecting→Connected` on the **first**. |
| 3 | **UDP buffer overflow.** Default Linux rmem (~208KB) silently dropped DRW ACKs under the P2pRdy flood. | `listenUDPLocal` sets 1MB `SO_RCVBUF`+`SO_SNDBUF` on every UDP socket. |
| 4 | **Orphaned channel writes.** A mid-upload drop left `WriteContext` blocked forever on the dead client's channel. | `Channel.Close()` + `ErrChannelClosed`; `Client.Close()` closes all 8 channels to unblock pending writes. |
| 5 | **Stray progress values.** `extractProgress` scanned every MQTT message type for a "progress" field, so transient `1` values from non-print messages jumped the live bar. | Only call `extractProgress` for `ct=1001` (`MqttCmdPrintSchedule`), plus a client-side monotonic guard. |
| 6 | **Printer stuck state.** The PPPP daemon would wedge (accept handshake, then `Close` within ~200ms) and only a power-cycle cleared it. | Power-cycle-and-recover as the final escalation rung. |

---

## Change reference (by file)

### `internal/pppp/client/client.go`
- **Keepalives** — `PingReq` sent every `ppppKeepaliveInterval` (5s) while connected.
- **Health tracking** — `Healthy()` is false when no pong within `ppppStaleThreshold`
  (`3 × keepalive` = 15s). `lastPong` is refreshed by `PingResp`, `PingReq`,
  `Hello`, **and** `P2pRdy`. Note the grace period: `Healthy()` also tracks
  `connectedAt`, so a freshly connected session isn't reported stale before it has
  had a chance to pong.
- **P2pRdy guard** — always sends `P2pRdyAck`; transitions to `Connected` only on the first.
- **UDP buffers** — `setUDPSocketBuffers` applies `udpSocketBufferSize` (1MB) in `listenUDPLocal`.
- **Channel close** — `Client.Close()` calls `Channel.Close()` on all channels.
- **LanSearch rotation** — `OpenBroadcastLANToMany` rotates targets (known-IP →
  class-C broadcast → global broadcast) via `lanSearchRetryNumber % len(addrs)`.

### `internal/pppp/protocol/channel.go`
- `closed` flag on `Channel`, `ErrChannelClosed` sentinel.
- `Close()` sets the flag and signals `eventCh`; `WriteContext` checks `closed` each
  poll cycle and returns `ErrChannelClosed`.

### `internal/service/pppp.go`
- `Healthy()` added to the `ppppConn` interface.
- `waitConnected` requires `StateConnected` **and** `Healthy()`; returns `errStaleSession` otherwise.
- `WorkerRun` polls `Healthy()` every `pollInterval` (50ms); stale → `ErrServiceRestartSignal`.
- `uploadWithRetries`: 3 attempts (`uploadMaxRetries`), 2s apart.
- `Upload`: in-memory payload, escalation ladder above, `WithPowerController()` for injection.

### `internal/service/power_controller.go` *(new)*
- `PrinterPowerController` interface (`PowerCycle(ctx) error`).
- `smartSocketPowerController`: HA-backed, `switch.turn_off` → 10s → `switch.turn_on`.
- `waitForPrinterRecovery`: polls `udp4` dial to `printerIP:32108` every 3s until reachable.
- `PrinterPowerControllerFromConfig`: factory; returns a `noopPowerController` when the
  smart socket isn't configured/enabled, so the ladder degrades gracefully.

### `internal/service/mqttqueue.go`
- `handlePayload` only calls `extractProgress` when `ct == MqttCmdPrintSchedule` (1001).
  > ⚠️ The fix is at the **call site**, not inside `extractProgress` — that function
  > still searches keys broadly for "progress". Don't reuse it elsewhere expecting it
  > to be selective, and don't "simplify" the call-site guard away.

### `internal/web/static/ankersrv.js`
- **Monotonic progress guard** — `_maxSeenProgress` is the ceiling; while printing,
  a value more than 2% below the ceiling (and below it once past 10%) is discarded.
  Replaces the older `isSpuriousZero`/`isSpuriousLow` guards.
- **Upload card visibility** — `setUploadCardVisible()` toggles `.is-visible` on
  `#upload-card-wrapper`: on at upload start, off on idle/done/error.
- `_maxSeenProgress` resets on print idle (ct=1000), new-file load (ct=1044), and MQTT socket close.

### `internal/web/static/ankersrv.css`
- `.upload-glass-overlay` (blur) + `.upload-glass-spinner` (`glass-spin` keyframe).
- Dark-green card tint while printing: `body.print-active-glow .card`.
- `#upload-card-wrapper` show/hide.
- Debug/command feed fonts at `0.40rem` (`.state-debug-feed`, `.state-command-feed`).

### `internal/web/static/base.html`
- Header badge next to "ng": "now with extra AI goodness! adapted by jr" (Georgia/Comic Sans).

### `internal/web/static/tabs/home.html`
- Upload progress card wrapped in `id="upload-card-wrapper"`.

### `cmd/ankerctl/main.go`
- Wires `pppp.WithPowerController(service.PrinterPowerControllerFromConfig(cfgMgr))`.

### `internal/web/ws/pppp.go`
- When `ppppservice` is registered the status websocket stays **passive** — it reads
  the service's connection state rather than running an active probe, so it never
  binds UDP 32108 and races a real upload (`hasRegisteredPPPPService()` gates the probe).

---

## Tunables (quick reference)

| Constant | Value | Where |
|----------|-------|-------|
| `ppppKeepaliveInterval` | 5s | client.go |
| `ppppStaleThreshold` | 15s (3× keepalive) | client.go |
| `udpSocketBufferSize` | 1 MB | client.go |
| `pollInterval` | 50ms | pppp.go |
| `uploadMaxRetries` | 3 | pppp.go |
| upload retry delay | 2s | pppp.go |
| `uploadMaxPowerCycles` | 3 | pppp.go |
| `uploadTotalTimeout` | 5 min | pppp.go |
| `uploadRecoveryBootWait` | 30s | pppp.go |
| power-cycle off duration | 10s | power_controller.go |
| recovery poll interval | 3s | power_controller.go |
| recovery dial timeout | 2s | power_controller.go |
| PPPP port | 32108 | power_controller.go |

---

## Deploy status

- **Running on NAS:** `ankerctl-ng ai-goodness-20260620141836`, service active. This is the
  hardening work from commit `53a7fc1`.
- **Pending deploy:** commit `d22f187` (monotonic progress guard, upload-card hide-on-idle,
  font halving) is committed but **not yet on the NAS** — deploy when the current print
  finishes. (The most recent commit, `3c2c0f8`, is this doc only.)

> Untracked files in the working tree (`callback?code=…`, `firmware.factory.bin`) are
> stray local artifacts, unrelated to this work — don't commit them.

---

## Known remaining issues

- **Wi-Fi power-saving L2 loss.** The printer still intermittently becomes
  L2-unreachable (NAS can't ping it while RouterOS can). A static neighbor entry helps
  but doesn't fully solve it. The real fixes are a RouterOS proxy-ARP/neighbor setup or,
  better, a **wired connection to the printer** — either would eliminate this class of
  failure and make the power-cycle rung rarely needed.

## Next steps / verification owed

- [ ] Deploy `d22f187` to the NAS after the in-progress print completes.
- [ ] Live-test the full power-cycle recovery path end-to-end (couldn't test this
      session — printer in use). Confirm `waitForPrinterRecovery` returns and the upload
      resumes after a real socket cycle.
- [ ] Decide on wiring the printer / RouterOS proxy to retire the L2 issue.

---

# Session 2026-06-20 (later) — Config UX, fork prep, UI polish

Done alongside the hardening work above, all gated on `go build` + `go test ./...`
(all green) — **not deployed to NAS yet**.

## Notifications config — mode-based rewrite (headline)
The notifications setup was a single raw "server URL" field demanding Apprise URL
syntax (`mailto://`, `jsons://`), with SMTP buried in a write-only collapsed helper
(reload showed a raw URL, not your fields). Replaced with a **delivery-method
selector** — Email (SMTP) / Webhook / Apprise server — that shows only the relevant
fields and **round-trips properly**.

- `internal/web/static/tabs/setup.html` — mode selector (btn-check radios) + three
  panels; SMTP fields promoted to first-class; webhook URL + optional custom body;
  Apprise-server URL/key/tag; an "Advanced: effective URL" readout.
- `internal/web/static/ankersrv.js` — the canonical `server_url` is still the only
  thing sent to the backend (contract unchanged). New helpers `composeServerUrl`
  (per mode) and `applyServerUrlToFields` (parses the saved URL's scheme back into
  the right mode + fields). Webhook mode maps friendly `https://`→`jsons://` /
  `http://`→`json://`. Password is masked in the effective-URL preview.
- `internal/web/static/ankersrv.css` — themed mode selector/panels.
- Backend untouched: scheme still selects transport (`mailto[s]`=SMTP,
  `json[s]`=webhook, `http[s]`+key=notify API). See `internal/notifications/apprise.go`.

## Go cleanup
- `internal/service/pppp.go` `isRetryableUploadErr` — collapsed to idiomatic
  combined `errors.Is` + direct boolean return (behavior identical, test-covered).
- Audited apprise/pppp/client/mqttqueue for conciseness; the rest already passes
  `gofmt`/`go vet` and is reliability-critical + un-live-testable this session, so
  it was deliberately left alone rather than risk regressions.

## Fork-publish prep (for `jr551/ankerctl_go_remake`, fork of `Django1982/...`)
- `README.md` — fork banner + Credits & attribution + License (GPLv3) sections.
- `NOTICE`, `CONTRIBUTING.md`, `.github/ISSUE_TEMPLATE/` added.
- `.gitignore` hardened: firmware blobs, OAuth callback captures, `config.json`,
  `login*.json`, tokens/keys.
- ⚠️ Two stray sensitive files exist on disk (now gitignored, NOT committed):
  `firmware.factory.bin` and `callback?code=…` (contains a live OAuth auth code —
  **delete this one manually**). Publish plan: GPLv3 obligations preserved.

## In-flight feature branches (worktrees — review & merge before publishing)
- `worktree-agent-afc4f4f603317b714` (commit `2bee962`) — **Live preview rewind**:
  built from scratch (ring buffer sampling the live `<video>`), scrubber with
  keyboard support, LIVE/REWIND states, crossfade + vignette effects,
  prefers-reduced-motion. Needs live-camera testing.
- Camera-systems expansion (presets for Frigate/MJPEG/OctoPrint/etc.) — separate
  worktree, finishing as of this writing; merge + re-test when done.
