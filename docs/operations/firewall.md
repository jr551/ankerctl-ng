# Firewall Configuration

**Audience:** Operators deploying ankerctl on a Linux host with ufw (or another stateful firewall) enabled.

## Why fixed local ports?

ankerctl binds PPPP sockets to **fixed local UDP ports** rather than letting the OS assign ephemeral ports. This is required for stateful firewalls:

- LAN discovery (`LanSearch`) is sent as a UDP broadcast.
- conntrack does **not** track broadcast UDP, so the firewall has no state for the printer's unicast reply.
- If the local socket is bound to a random ephemeral port, the reply hits an unknown port and ufw drops it.
- Binding to fixed, documented ports lets the operator add a single static rule per port.

The cloud-relay path (`OpenWAN`) keeps an ephemeral bind because conntrack handles unicast UDP correctly there; no extra rule is needed.

## Required ufw rules

Run on the host before starting ankerctl:

```bash
sudo ufw allow in proto udp to any port 32100
sudo ufw allow in proto udp to any port 32108
sudo ufw allow in proto udp to any port 32109
sudo ufw reload
```

## Port reference

| Port | Proto | Direction | Code path | Purpose |
|------|-------|-----------|-----------|---------|
| 32100 | UDP | inbound | `OpenLAN` | PPPP session: file upload, camera stream, remote control |
| 32108 | UDP | inbound | `OpenBroadcastLAN` | LAN discovery — server's broadcast / PunchPkt bind |
| 32109 | UDP | inbound | `OpenBroadcast` | LAN discovery — `find_anker` CLI bind. Distinct from 32108 so the CLI can run while the server is active. |
| 8789 | TCP | outbound | `internal/mqtt` | Anker MQTT broker (TLS). Default `ufw allow out` covers it. |
| 4470 | TCP | inbound | `internal/web` | ankerctl web UI / API (only if exposing beyond `127.0.0.1`). |

> **Tip:** If you run ankerctl only on `127.0.0.1`, you do **not** need a ufw rule for port 4470. You still need 32100/32108/32109 for the printer LAN traffic regardless of the web bind address.

## Source-IP restriction (optional)

To limit the LAN ports to your printer's subnet, replace `any` with the subnet:

```bash
sudo ufw allow in proto udp from 192.168.1.0/24 to any port 32100
sudo ufw allow in proto udp from 192.168.1.0/24 to any port 32108
sudo ufw allow in proto udp from 192.168.1.0/24 to any port 32109
sudo ufw reload
```

## Common errors

### `is another ankerctl instance running?`

Logged by `listenUDPLocal()` when a fixed port is already in use. Causes:

- A second ankerctl process is running.
- The `find_anker` CLI is running while the server is also running and both are trying to bind 32108. Since the v1.0.x patch, the CLI uses 32109 for exactly this reason — make sure you are on the latest build.
- Another application on the host is using the port (rare; 32100/32108 are PPPP-specific).

Resolution: stop the other process, or check `ss -ulnp | grep -E ':(32100|32108|32109)'`.

### Printer never appears connected, no log errors

Almost always a firewall issue. Verify with:

```bash
sudo ufw status verbose | grep -E '32100|32108|32109'
```

All three rules must show `ALLOW IN`. Without 32108, discovery silently times out — the broadcast goes out but the printer's unicast reply is dropped.

### Docker

When deploying via Docker, you must use `network_mode: host`. Bridge networking does not pass UDP broadcasts. ufw rules apply on the host as normal.

## Related documentation

- Wiki: [Protocol Details — PPPP](../wiki/Protocol-Details.md#pppp-protocol)
- Wiki: [Installation — Firewall (ufw)](../wiki/Installation-and-Configuration.md#firewall-ufw)
- Wiki: [Troubleshooting — Printer not found on LAN](../wiki/Troubleshooting.md#printer-not-found-on-lan)
- Code: `internal/pppp/client/client.go` (`listenUDPLocal`, `OpenLAN`, `OpenBroadcastLAN`, `OpenBroadcast`)
- Issues: `Django1982/ankerctl_go_remake#66` (Linux/ufw), `Django1982/ankermake-m5-protocol#77` (Windows, same root cause)
