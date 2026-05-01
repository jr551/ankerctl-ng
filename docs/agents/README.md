# Agent Workflow (Codex-Compatible)

This repository already contains agent profiles and persistent memories in:

- `.claude/agents/*.md`
- `.claude/agent-memory/<agent>/MEMORY.md`

This folder adds a lightweight workflow so these can be reused consistently from the terminal.

## Structure

- `docs/agents/README.md`: usage and conventions
- `docs/agents/HANDOFF_TEMPLATE.md`: task handoff format
- `scripts/agent-context.sh`: build a prompt context from role + memory + task

## Quick Start

List available agents:

```bash
./scripts/agent-context.sh --list
```

Build full context for an agent and a task:

```bash
./scripts/agent-context.sh go-migration-coordinator \
  --task "Review Phase 4 and produce implementation TODOs"
```

Save context to a file:

```bash
./scripts/agent-context.sh protocol-reverse-engineer \
  --task "Validate PPPP state behavior" \
  > /tmp/agent-prompt.txt
```

## Conventions

- Agent name maps to:
  - profile: `.claude/agents/<name>.md`
  - memory dir: `.claude/agent-memory/<name>/`
- Keep `MEMORY.md` concise; move details into topic files.
- Use the handoff template for cross-agent work to keep tasks auditable.

## Recommended Flow

1. Pick agent (`--list`).
2. Write a focused task with expected output.
3. Generate context (`agent-context.sh`).
4. Execute task.
5. Update `.claude/agent-memory/<agent>/MEMORY.md` with stable learnings only.

## Report Lifecycle

```
docs/agents/reports/       ← inbox: reports awaiting review
        ↓  (after review verdict: approved / approved-with-fixes)
docs/archive/agent-reports/ ← done: reviewed and closed
```

- Completion reports (`COMPLETION_TEMPLATE.md`) are created in `docs/agents/reports/`.
- After Claude reviews and the verdict is **approved** or **approved-with-fixes**, the agent
  moves both the completion report and the review report to `docs/archive/agent-reports/`.
- Reports still in `docs/agents/reports/` = pending review.
- Reports in `docs/archive/agent-reports/` = reviewed and closed.
