# Protocol Documentation

## Overview

ankerctl communicates with AnkerMake M5 printers via three protocols:

1. **MQTT** -- Cloud-based command/status channel (encrypted)
2. **PPPP** -- LAN-based P2P protocol for video and file transfer (UDP)
3. **HTTP Auth** -- Anker cloud API for login and printer discovery

## MQTT Protocol

- **Broker port**: 8789
- **Encryption**: AES-256-CBC with fixed IV `3DPrintAnkerMake`
- **Header**: 63-byte fixed structure + encrypted body + XOR checksum
- **Subscribe topic**: `/phone/maker/{SN}/notice`
- **Publish topic**: `/device/maker/{SN}/command`
- **Username**: `eufy_{user_id}`
- **Password**: user email

See the [Protocol Details wiki page](../wiki/Protocol-Details.md) for message types, state machine, and packet format.

## PPPP Protocol

- **LAN discovery port**: 32108 (UDP)
- **Session port**: 32100 (UDP)
- **CLI discovery port**: 32109 (UDP) — `find_anker` binds here to avoid conflict with the server's 32108
- **Seed**: `EUPRAKM`
- **Transport**: Single UDP socket, 8 logical channels
- **Flow control**: DRW pipelining with 64-packet in-flight window
- **Retransmission timeout**: 0.5 seconds
- **Sequencing**: CyclicU16 (16-bit wraparound counter)
- **Local socket bind**: fixed (not ephemeral) — required for ufw/conntrack. See [`../operations/firewall.md`](../operations/firewall.md).

See the [Protocol Details wiki page](../wiki/Protocol-Details.md) for channel assignments, packet types, and crypto details. Detailed crypto validation lives in [`pppp-crypto-validation.md`](pppp-crypto-validation.md).

## HTTP Auth Flow

1. Login via Passport API v2 (email + password)
2. Receive auth_token + ab_code (region detection)
3. Query printer list via App API v1
4. Fetch DSK keys for P2P authentication
5. Decode PPPP init strings for connection parameters

See the [Protocol Details wiki page](../wiki/Protocol-Details.md) for API endpoints and data structures.

## Critical Constants

```
MQTT AES IV:           "3DPrintAnkerMake" (16 bytes)
MQTT Port:             8789
PPPP Port:             32100  (session, local + remote)
PPPP LAN Port:         32108  (discovery, server local bind)
PPPP Discovery Port:   32109  (discovery, find_anker CLI local bind)
PPPP Seed:             "EUPRAKM"
Default Host:          127.0.0.1
Default Port:          4470
```
