# API Reference

ankerctl exposes a REST API and WebSocket streams on `127.0.0.1:4470` (default).

The complete, up-to-date endpoint reference is maintained in the wiki:

- [API Reference (wiki)](../wiki/API-Reference.md) -- full REST and WebSocket endpoint documentation

## Quick Summary

### Authentication Rules

- **POST/DELETE**: Always require API key (`X-Api-Key` header or `apikey` query param)
- **GET**: Public by default, except protected paths
- **Protected GET paths**: `/api/ankerctl/server/reload`, `/api/debug/*`,
  `/api/settings/mqtt`, `/api/notifications/settings`, `/api/printers`, `/api/history`
- **Exempt when no printer configured**: `/api/ankerctl/config/upload`, `/api/ankerctl/config/login`

### WebSocket Streams

| Path | Direction | Content | Auth |
|---|---|---|---|
| `/ws/mqtt` | Server -> Client | JSON MQTT events | No |
| `/ws/video` | Server -> Client | Binary H.264 frames | No |
| `/ws/pppp-state` | Server -> Client | JSON connection state (polled) | No |
| `/ws/upload` | Server -> Client | JSON upload progress | No |
| `/ws/ctrl` | Bidirectional | JSON commands | Inline (first message) |
