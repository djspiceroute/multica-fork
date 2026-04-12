# Maintainers Notes (Fork)

## Fork-specific changes

This fork currently includes local changes focused on agent runtime reliability and review flow consistency:

- Gemini runtime parity:
  - Added Gemini/Hermes path handling in the daemon so Node is discoverable even in GUI-launched environments.
  - Added explicit `MULTICA_NODE_PATH` override support for deterministic Node resolution.
- Review policy:
  - Tightened done-transition behavior to align with reviewer-gated completion policy.
- CLI sync behavior:
  - Added CLI-first GitHub reconciliation behavior to keep local/remote issue state in sync.
- Diagnostics:
  - Improved provider readiness and runtime diagnostics to make online/offline causes clearer.

## Running daemon locally

From repo root:

```bash
make setup
make daemon
```

Or start full app stack:

```bash
make start
```

## Quick runtime health check

Use these checks after daemon start to confirm provider availability:

```bash
multica daemon status
multica runtime list
```

Expected: local runtimes for `codex`, `claude`, and `gemini` should appear as `online`.

## Key environment variables

- `MULTICA_GEMINI_PATH`:
  - Absolute path to Gemini/Hermes CLI binary if autodetection is not sufficient.
- `MULTICA_NODE_PATH`:
  - Absolute path to Node binary used by Gemini/Hermes runtime.
- `MULTICA_CODEX_PATH`:
  - Absolute path to Codex CLI binary (optional explicit override).
- `MULTICA_CLAUDE_PATH`:
  - Absolute path to Claude CLI binary (optional explicit override).
- `LOG_LEVEL`:
  - Server/daemon log verbosity (`debug`, `info`, `warn`, `error`).
