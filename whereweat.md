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

## Parallel feature agents — outcome (both hit a stale-base problem)
Two agents ran in isolated worktrees branched from an OLD commit (`4a5841e`) that
predated this branch's Camera & AI pane, HA camera proxy, and existing rewind
feature. Both built things that **collided** with newer work on this branch.

- **Camera presets** (`worktree-agent-afbed6b1...`, `49b8971`) — MERGED PARTIALLY
  (commit `2003644`). Kept the backward-compatible model + backend: `Kind`/`Fields`
  on `ExternalCameraSettings`, `CameraKind*` presets (MJPEG/OctoPrint/Frigate/
  go2rtc/Reolink/RTSP), `DeriveExternalCameraURLs`, settings-handler merge, tests,
  API docs. **Dropped** its new "Camera" setup pane + JS — it duplicated the
  existing `#camera-form` (Camera & AI pane), causing duplicate element IDs. The
  presets are dormant until the picker is grafted into the real form (TODO below).
- **Live preview rewind** (`worktree-agent-afc4f4f...`, `2bee962`) — NOT MERGED
  (merge aborted). This branch already has a working rewind (`cameraFrameBuffer` +
  `rewindControls/Slider/Label`); the agent built a parallel from-scratch version
  that would break it. Branch retained for cherry-picking effects ideas (crossfade,
  vignette, LIVE pulse, reduced-motion) into the EXISTING rewind as a follow-up.

## TODO from this session
- [ ] Graft the camera preset picker into the existing Camera & AI `#camera-form`
      (backend already supports `kind`/`fields`).
- [ ] Optionally enhance the existing rewind with effects (don't re-merge the agent
      branch — port the CSS ideas onto the working markup).
- [ ] Publish-as-fork steps (see fork-prep plan); delete the stray `callback?code=…`
      file (live OAuth code) before any publish.

---

# Session 2026-06-20 (continued) — Camera presets, rewind effects, gcode viewer

Built and **deployed to the NAS** (binary `feature-camera-rewind-gcode-20260620153225`)
while the "Bean Ice Cube Tray" print ran — the restart was print-safe (printer
prints autonomously; verified the print continued through the redeploy, no log errors).

## Delivered (all on branch `ha-camera-ai-monitor`, pushed)
- **Camera preset picker** — the External camera form now has a "Camera system"
  dropdown (MJPEG/OctoPrint/Frigate/go2rtc/Reolink/RTSP + Advanced/Custom) with
  per-preset fields and a live resolved-URL preview, grafted into the existing
  HA-proxy `#camera-form`. JS `deriveCameraUrls` mirrors `model.DeriveExternalCameraURLs`;
  server re-derives non-custom kinds on save, so saved URLs are Go-guaranteed.
- **Rewind effects** — crossfade of the buffered frame, LIVE/REWIND pill, vignette
  while scrubbing, green thumb + focus ring, `prefers-reduced-motion`. Buffer logic
  and 0=Live convention unchanged; confirmed display-only (never issues a print).
- **GCode toolpath viewer** — history rows with an archive get a "View" button
  opening a pure-canvas 2D top-down toolpath (layer slider, travel toggle,
  fit-to-bbox). New `GET /api/history/{id}/gcode` serves the plain-text archive.
- **FE polish** — feed font 0.40rem→0.7rem, captcha alt, collapse aria-labels,
  fixed an invalid nested `<a>` in the header, removed a dead ternary.

## Verified
- `go build` / `go vet` / `go test ./...` all green; 6 new `HistoryGCode` handler tests.
- gcode parser smoke-tested offline AND against the **real 8.2 MB file being printed**
  via the actual `parseGcode` from source: 56 layers, 231K extruding segments,
  sensible bbox, 105 ms, not truncated.
- On the live NAS (read-only): all new markup renders in the configured dashboard;
  `GET /api/history/42/gcode` → HTTP 200, 8.2 MB.

## Deferred — run when the printer is IDLE (needs a free printer / browser / live feed)
- [ ] **Camera preset live feed** — save a real preset (e.g. Frigate/go2rtc) and
      confirm the live view + `Test frame` (`/api/camera/frame`) actually shows video.
      (Form logic + URL derivation are verified; only the live fetch is pending.)
- [ ] **Camera save round-trip on real config** — not done in-session to avoid
      overwriting the live HA "3d printer camera" config. Verify kind/fields persist.
- [ ] **Rewind crossfade on a moving feed** — buffer only fills from a live stream;
      confirm crossfade/vignette/LIVE pill and snap-back-to-live look right, and
      re-confirm it stays display-only.
- [ ] **GCode viewer in a browser** — open a history row's View button, scrub layers.
      (Endpoint + parser verified; only the canvas render is unviewed.)
- [ ] **PPPP upload hardening / power-cycle recovery** (from the earlier session) —
      upload a test gcode, force a stale session, confirm retry→restart→power-cycle.
      Recipe in `nas.md` (Test Upload + Power-cycle). DO NOT run during a print.
- [ ] **Reprint-from-history** — issues a real print; idle only.

## Notes
- The aborted camera/rewind worktree branches were mined for reusable code, not merged.
- Still pending from before: delete the stray `callback?code=…` file (live OAuth code)
  before any public fork publish.

## Idle-printer test results (2026-06-20, after the Bean Ice Cube Tray print)
Run on the deployed NAS binary once the print finished (it stops at 100% and never
returns to idle, so ankerctl's state stays `printing` — uploads still work fine).

- ✅ **PPPP upload (normal path)** — `POST /api/files/local` print=false, 3.5 KB test
  gcode: HTTP 200 in **0.46 s**. Logs show a clean LanSearch→PunchPkt→P2pRdy handshake
  to 192.168.69.33 (with broadcast fallbacks) and `file transfer completed`. No errors,
  no stale-session. (Left a tiny `ankerctl-pppp-test.gcode` on printer storage.)
- ✅ **Camera frame** — `/api/camera/frame` → HTTP 200, image/jpeg, 20 KB (HA proxy serving).
- ✅ **Camera config GET on new binary** — `source=external`, HA proxy on,
  `kind=(none)` → confirms the new preset model loads legacy configs backward-compatibly.

### Still needs a human / explicit go-ahead
- **Power-cycle recovery** — not run: it physically reboots the printer via the HA
  smart socket and needs forced PPPP failures to trigger the escalation ladder. Run
  with supervision (recipe in `nas.md`).
- **Camera preset save round-trip** — not run to avoid overwriting the live HA camera
  (`camera.front_door_camera`). Pick a preset in the UI when you want it; logic is
  unit-tested and the form renders.
- **Rewind crossfade / gcode viewer canvas** — browser-visual; open the Home rewind
  scrubber and a history row's "View" button to eyeball. Endpoint + parser already
  verified against the real 8.2 MB print file.

## Print-completion fix — confirmed live (2026-06-20)
The M5C stops at 100% at the end of a model and never reports idle, so ankerctl
stayed "printing" forever and `RecordFinish` was never called (entries left
"started" → "interrupted" by the next print). Fixed in `mqttqueue.go`:
`handlePrintSchedule` now treats progress==100 (while printing) as completion —
records the finish, forces idle, emits a `print_state` idle event; a
`finishedAtFull` latch ignores the printer's continued "printing" reports until a
new print resets progress. Commit `8eec09f` (+2 unit tests).

**Verified on real hardware:** deployed to NAS, printed a tiny 10mm test cube
start→finish. The M5C held at 100%; the fix logged `print finished (progress
reached 100%)`, recorded history entry `finished` (dur=149s, prog=100), and
returned state to idle. Deploying also unstuck the previously-jammed Bean print.

Minor known quirk (pre-existing, not from this fix): the deferred-history-start
logic can create two rows for one print (one `interrupted` prog=0 + one
`finished`) when the printer re-sends the filename across the pre_print→printing
transition. Cosmetic; the `finished` row is correct. Worth a follow-up.

## Polyslice (STL→slice→print) tab — feasibility verdict
Asked: add https://github.com/jgphilpott/polyslice as a tab for in-browser STL
slicing + print. **Feasible and architecturally clean.** Polyslice is a mature
MIT-licensed JS slicer that runs in the browser (ESM/IIFE, no native/WASM deps),
parses STL/OBJ/3MF itself, and has infill/walls/supports/adhesion + printer
profiles. It's built on three.js.

Why it fits: the print path already exists — slice client-side → POST the gcode
to the existing `/api/files/local` (print=true/false). NO backend change needed;
the new gcode toolpath viewer can preview the slice. Work is front-end: vendor
three.js + polyslice (CSP requires self-hosting, no CDN), a "Slice" tab (STL
upload + three.js preview + settings + slice + send-to-printer).

Risks before trusting it: (a) bundle size — three.js is large, lazy-load the tab;
(b) in-browser slicing of big models is slow/memory-heavy — use a Web Worker;
(c) **slice-output correctness/safety on the M5C** — must confirm a Marlin-style
M5C profile (220×220 bed, temps, start/end gcode) and validate real output on the
printer (like the cube test) before trusting an auto-sliced print. Estimate: a
few focused hours + hardware validation.
