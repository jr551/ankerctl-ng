# Archive

Historische Artefakte aus der Migrationsphase. Keine aktiven Dokumente.

This directory preserves documentation from the v1.0.0 migration effort
(2026-03 to 2026-05) for historical reference. Nothing here reflects the
current state of the project — see [`docs/README.md`](../README.md) for the
active documentation index.

## Contents

| Path | Description |
|---|---|
| [`agent-reports/`](agent-reports/) | Phase QA reviews, codex completion reports, security audits, and gap analyses — reviewed and closed. New reports land here after review (see below). |
| [`OPEN_ITEMS.md`](OPEN_ITEMS.md) | Final open-items tracker. All entries were resolved before the v1.0.0 release. |
| [`phase4-plan.md`](phase4-plan.md) | Implementation plan for Phase 4 (Middleware + HTTP server). Completed and merged. |

## Report Flow

New completion and review reports start in `docs/agents/reports/` (pending review).
After a review is completed and the verdict is **approved** or **approved-with-fixes**,
both reports are moved here (`docs/archive/agent-reports/`) by the reviewing agent.

This gives a clear audit trail: anything still in `docs/agents/reports/` needs review;
everything here is closed.

## When to Read These

- Investigating *why* a particular design decision was made during the migration.
- Reconstructing the historical context of a bug or audit finding.
- Onboarding contributors who want to understand the project's evolution.

For active work, use [GitHub Issues](https://github.com/jr551/ankerctl_go_remake/issues)
and the live documentation under `docs/`.
