# ankerctl-ng

Experimental AnkerMake web UI and CLI build.

[![Release](https://img.shields.io/badge/release-v1.0.0-success)](https://github.com/jr551/ankerctl_go_remake/releases/tag/v1.0.0)
[![Go Version](https://img.shields.io/badge/Go-1.24+-00ADD8?logo=go)](https://go.dev/)
[![License: GPL v3](https://img.shields.io/badge/License-GPLv3-blue.svg)](LICENSE)
[![CI](https://github.com/jr551/ankerctl_go_remake/actions/workflows/ci.yml/badge.svg)](https://github.com/jr551/ankerctl_go_remake/actions/workflows/ci.yml)
[![Docker](https://img.shields.io/badge/ghcr.io-ankerctl--ng-blue?logo=docker)](https://ghcr.io/jr551/ankerctl-ng)

![ankerctl-ng dashboard](docs/img/screenshot-dashboard-ng.png)

## What this fork adds

- Home Assistant camera support and smart socket controls
- AI print checks with saved replies and short-lived evidence images
- Notification history, raw webhook posting, SMTP helper, and HA speech hooks
- Power saving controls, better camera loading, and UI cleanup for day-to-day printer use
- OrcaSlicer-first setup flow

## Status

This is an **experimental build**. It is aimed at people who want the extra features above and are comfortable with a moving target.

## Quick start

### Source install

```sh
git clone https://github.com/jr551/ankerctl_go_remake.git
cd ankerctl_go_remake
./install.sh install
```

The script:

- installs Go build tools if missing
- builds `ankerctl-ng`
- installs it
- asks whether to create a `systemd` service

To update later:

```sh
git pull
./install.sh update
```

### Docker

```sh
docker run -d \
  --name ankerctl-ng \
  --network host \
  -v ~/.ankerctl-ng:/home/ankerctl/.ankerctl-ng \
  -v ankerctl-ng-captures:/captures \
  ghcr.io/jr551/ankerctl-ng:latest
```

Or:

**Docker Compose:**

```yaml
services:
  ankerctl-ng:
    image: ghcr.io/jr551/ankerctl-ng:latest
    container_name: ankerctl-ng
    network_mode: host
    restart: unless-stopped
    volumes:
      - ~/.ankerctl-ng:/home/ankerctl/.ankerctl-ng
      - ankerctl-ng-captures:/captures
    env_file: .env

volumes:
  ankerctl-ng-captures:
```

Copy `.env.example` to `.env` and adjust the values, then run:

```sh
docker compose up -d
```

`network_mode: host` is still required for printer LAN traffic.

> **Firewall:** If ufw or another stateful firewall is enabled on the host, allow inbound UDP on **32100, 32108, and 32109**. ankerctl binds these as fixed local ports so conntrack can pass the printer's reply to a broadcast LanSearch. See [`docs/operations/firewall.md`](docs/operations/firewall.md) for the full rationale and `ufw allow` commands.

To build locally instead:

```sh
docker build -t ankerctl-ng .
```

## OrcaSlicer

Use OrcaSlicer with:

- Host type: `OctoPrint`
- Host / URL: `http://YOUR-HOST:4470`
- API key: only if you enabled write protection

Use `Send and Print` when you want the job to start immediately.

## Notes

- Default UI port: `4470`
- Config dir for new installs: `~/.ankerctl-ng`
- Older `~/.ankerctl` installs are still detected automatically

## Repo docs

Most of the older migration and protocol docs are still in [`docs/`](docs/), but the simplest path is:

1. use `install.sh`
2. open the web UI
3. import/login
4. connect OrcaSlicer
