# sprite-agent

A single, **symmetric** agent that runs on a [Sprite](https://fly.io/sprites) microVM. Each instance
is identical and fully capable:

- **Session service** — a web chat UI to talk to the sprite's Claude Code, with token-by-token
  streaming and terminal co-presence (the web UI and a `claude --resume` terminal share one
  transcript).
- **GitHub capability** — its Claude can clone, branch, commit, and open PRs.
- **Spawn capability** — it can create another sprite running this same artifact.
- **Minimal fleet brain** — on boot it registers itself into a shared S3/Tigris roster; any instance
  can list the roster.

This is **Phase 1** of the design in [`docs/sprite-agent-V2-plan.md`](docs/sprite-agent-V2-plan.md):
the full symmetric artifact (solo-capable, basic fleet-aware). It is *not* a "leader" — "leader" is
only ever a hat (wherever the human is). Coordination (dispatch, durable memory, presence-routing,
the fleet UI, the control plane) is Phase 2; insertion (take-the-wheel) is Phase 3.

> **Positioning:** this is connective tissue around Claude Code, not a new agent framework. Claude
> Code *is* the agent; we build the session service, fleet brain, spawning, and transports.

## Architecture

```
  You ──chat──► sprite's session service (web UI / WebSocket)
                     │  drives ▼
                  claude CLI (stream-json, deterministic --session-id)
                     │  transcript ▼
                  ~/.claude/projects/.../<id>.jsonl  ◄── terminal co-presence (claude --resume)

  Fleet brain (S3/Tigris):  fleet/<id>/status.json   (each sprite writes only its own keys)
                            roster = ListObjects("fleet/")
```

Built on **claude-hub's** Go supervision kernel (stream-json parsing, singleton process supervision,
`.jsonl` watcher, WebSocket hub fan-out).

## Layout

| Path | Purpose |
|---|---|
| `cmd/sprite-agent/` | entrypoint |
| `internal/config/` | env-driven configuration |
| `internal/process/` | Claude CLI process supervision (lifted from claude-hub) |
| `internal/watcher/` | `.jsonl` transcript watcher for terminal co-presence |
| `internal/session/` | per-session state machine |
| `internal/hub/` | WebSocket hub: fan one Claude session out to N clients |
| `internal/server/` | HTTP server, REST API, embedded web UI |
| `internal/fleet/` | S3/Tigris brain client + roster (self-registration) |
| `internal/spawn/` | sprite spawn capability (sprites API; stubbed without a token) |
| `pkg/claude/` | stream-json protocol types |
| `web/` | embedded PWA chat UI (lifted/trimmed from sprite-mobile v1) |

## Quick start

```sh
go build ./...
go test ./...

# Run the session service (drives the local `claude` CLI):
go run ./cmd/sprite-agent
# then open http://localhost:8080
```

See [`docs/RUNBOOK.md`](docs/RUNBOOK.md) for configuration (env vars) and the full run-from-scratch
instructions.

## Status

Phase 1. See `BUILD_REPORT.md` for what is built-and-verified vs. stubbed, and `DECISIONS.md` for
choices made under ambiguity.
