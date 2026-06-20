# whereweat.md — PPPP Upload Hardening & Self-Healing

## Session 2026-06-20

### Problem
PPPP file uploads to AnkerMake M5 printer were hanging for 2+ minutes, failing
with "context canceled" errors. The printer's Wi-Fi power saving would silently
drop PPPP sessions, leaving `State()==Connected` while no traffic flowed. Uploads
would block forever waiting for ACKs from the dead session.

### Root Causes Identified

1. **No PPPP keepalives**: The Go client responded to printer ALIVE pings but never sent its own. Idle sessions were dropped by Wi-Fi power saving without detection.

2. **P2pRdy flood thrashing**: Printer sends ~20 P2pRdy/sec after handshake. Every one reset `remoteAddr` and `state`, disrupting the session. Original port-32100 switch patch made things worse.

3. **UDP buffer overflow**: Default Linux rmem (~208KB) silently dropped DRW ACKs under P2pRdy flood load. `OpenBroadcastLAN` paths had no buffer tuning.

4. **Orphaned channel writes**: When the connection dropped mid-upload, `WriteContext` blocked forever on the dead client's channel — no mechanism to unblock.

5. **Stray progress values**: `extractProgress()` scanned ALL MQTT message types for "progress" fields, normalizing transient `1` values from non-ct=1001 messages into the live progress bar.

6. **Printer stuck state**: Printer's PPPP daemon would enter a persistent broken state (accepting handshake then sending Close within 200ms), requiring a power-cycle to clear.

### Changes Made

#### `internal/pppp/client/client.go`
- **Keepalives**: `PingReq` sent every 5s while connected (`ppppKeepaliveInterval=5s`)
- **Health tracking**: `Healthy()` returns false when no pong within 15s (`ppppStaleThreshold=3× keepalive`). `lastPong` refreshed on PingResp, PingReq, and Hello packets
- **P2pRdy guard**: Always sends `P2pRdyAck` (printer expects ACK for each flood packet), but only transitions Connecting→Connected on the first one
- **UDP buffers**: `listenUDPLocal` now sets 1MB SO_RCVBUF+SO_SNDBUF for all UDP paths
- **Channel close**: `Client.Close()` calls `Channel.Close()` on all 8 channels, unblocking `WriteContext` with `ErrChannelClosed` instead of hanging forever
- **LanSearch rotation**: Multi-target broadcast (`OpenBroadcastLANToMany`) rotates through known-IP → class-C broadcast → global broadcast targets

#### `internal/pppp/protocol/channel.go`
- Added `closed` flag to `Channel` struct
- `ErrChannelClosed` sentinel error
- `Close()` method signals eventCh and sets closed flag
- `WriteContext` checks `closed` each polling cycle

#### `internal/service/pppp.go`
- **`Healthy()` interface method** on `ppppConn`
- **`waitConnected`**: requires both `StateConnected` AND `Healthy()`. Returns `errStaleSession` on stale sessions, triggers `s.Restart()`
- **`WorkerRun`**: proactive `Healthy()` check every 50ms, restarts immediately on stale sessions
- **`uploadWithRetries`**: 3 attempts with 2s delays on connection drops
- **`Upload` persistent loop**: holds payload in memory. Retries 3×, then power-cycles printer, waits 30s+recovery, retries. Up to 3 power-cycles, 5-minute total timeout
- **`WithPowerController()`** setter for `PrinterPowerController` injection

#### `internal/service/power_controller.go` (NEW)
- `PrinterPowerController` interface with `PowerCycle(ctx) error`
- `smartSocketPowerController`: HA-backed implementation (off→10s→on)
- `waitForPrinterRecovery()`: UDP dial poll until printer PPPP port reachable
- `PrinterPowerControllerFromConfig()` factory

#### `internal/service/mqttqueue.go`
- `extractProgress` now only runs for ct=1001 (`MqttCmdPrintSchedule`), preventing stray "progress" fields from other MQTT commands from overwriting the live value

#### `internal/web/static/ankersrv.js`
- **isSpuriousLow guard**: ignores transient 1-2% progress values when last displayed is >7% ahead
- Upload card hidden by default (CSS `#upload-card-wrapper.is-visible`)
- Glass overlay effects during upload phase (CSS `.upload-glass-overlay`)

#### `internal/web/static/ankersrv.css`
- Dark green card styling when printing (`body.print-active-glow .card`)
- Upload glass overlay with spinner animation
- Upload card visibility toggle

#### `cmd/ankerctl/main.go`
- Wires `pppp.WithPowerController(service.PrinterPowerControllerFromConfig(cfgMgr))`

#### `internal/web/ws/pppp.go`
- PPPP websocket stays passive when `ppppservice` is registered, preventing UDP 32108 port conflicts

## Deployed Binary
`ankerctl-ng progress-fix-ui-20260620140656` running on NAS, service active.

## Known Remaining Issues
- Printer Wi-Fi power saving still causes intermittent L2 reachability loss (NAS can't ping printer while RouterOS can). Static neighbor entry helps but doesn't fully solve. RouterOS proxy or printer wired connection would eliminate this.
