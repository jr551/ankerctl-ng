# ankerctl Documentation

This directory contains the project documentation for the Go reimplementation of ankerctl.

> **Project status: v1.0.0 — Feature parity achieved (2026-05-01).**

## Structure

```
docs/
  MIGRATION_PLAN.md   Historical 17-phase migration roadmap (completed)
  wiki/               User-facing wiki pages (Home, Installation, API, Troubleshooting, ...)
  architecture/       Architecture decisions, package design, dependency graph
  api/                REST API reference, WebSocket protocol, OctoPrint compat
  protocols/          MQTT, PPPP, and HTTP auth protocol documentation
  development/        Developer onboarding, build instructions, testing guide
  operations/         Operations runbooks (firewall/ufw, deployment notes)
  agents/             Agent workflow templates and handoff format
  archive/            Historical migration artefacts (phase reports, closed open-items, completed plans)
  img/                Screenshots used in README and wiki
```

## Quick Links

- [Wiki Home](wiki/Home.md) -- start here for user-facing docs
- [Architecture Overview](architecture/README.md) -- Package layout, dependency rules, service lifecycle
- [API Reference](api/README.md) -- REST endpoints, WebSocket streams, auth rules
- [Protocol Documentation](protocols/README.md) -- MQTT encryption, PPPP UDP, HTTP auth flow
- [Development Guide](development/README.md) -- Getting started, building, testing, contributing
- [Firewall / ufw Setup](operations/firewall.md) -- Required UDP rules for PPPP (LAN discovery + session)
- [Migration Plan](MIGRATION_PLAN.md) -- Historical 17-phase migration roadmap
- [Migration Status](wiki/Migration-Status.md) -- Final phase summary
- [Archive](archive/README.md) -- Historical migration artefacts (phase reports, closed open-items)

## Related Files

- [`README.md`](../README.md) -- Project overview, build, install
- [`CLAUDE.md`](../CLAUDE.md) -- AI assistant instructions and project context
- [`SECURITY.md`](../SECURITY.md) -- Security policy and vulnerability reporting
