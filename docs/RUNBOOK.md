# Runbook — running sprite-agent from scratch

## Prerequisites
- Go 1.25+
- An authenticated `claude` CLI (`claude --version`; Claude Code 2.1.185+ for
  `--session-id` / `--include-partial-messages`).
- `gh` authenticated (for the GitHub capability, M3).
- Optional: S3/Tigris credentials (for the fleet brain, M4).
- Optional: `SPRITE_API_TOKEN` (for live spawn, M4).

## Build & test
```sh
go build ./...
go test ./...
```

## Run the session service
```sh
go build -o sprite-agent ./cmd/sprite-agent
./sprite-agent
# open http://localhost:8080
```

## Configuration (environment variables)

| Var | Default | Purpose |
|---|---|---|
| `SPRITE_AGENT_ADDR` | `:8080` | HTTP listen address (the service's `--http-port`). |
| `SPRITE_AGENT_ID` | hostname | This agent's id in the fleet. |
| `SPRITE_AGENT_WORKDIR` | `/home/sprite` | cwd for the Claude process; its transcript dir is derived from this. |
| `SPRITE_AGENT_PERMISSION_MODE` | `acceptEdits` | `--permission-mode` for Claude (scoped, not skip-all). |
| `SPRITE_AGENT_SETTINGS` | _(unset)_ | path passed to `--settings`. |
| `SPRITE_AGENT_MCP_CONFIG` | _(unset)_ | path passed to `--mcp-config`. |
| `SPRITE_AGENT_ROLE` | `worker` | `home` or `worker`, advertised in the roster. |
| `SPRITE_AGENT_ARTIFACT` | `github.com/clouvet/sprite-agent@main` | bootstrap pointer handed to spawned sprites. |
| `S3_BUCKET` `S3_REGION` `S3_ENDPOINT` `S3_ACCESS_KEY` `S3_SECRET_KEY` | _(unset)_ | fleet brain (Tigris/S3). Brain disabled if `S3_BUCKET` is empty. |
| `SPRITE_API_TOKEN` | _(unset)_ | sprites API token (`org-slug/org-id/token-id/token-value`) for live spawn. Spawn is stubbed if unset. |
| `SPRITE_API_BASE` | `https://api.sprites.dev` | sprites API base URL. |
| `SPRITE_AGENT_SPAWN_PROVISION` | `1` | `0` = bare create (don't provision the agent onto the new sprite). Provisioning needs a brain. |

## Smoke test (M2 acceptance)
```sh
./scripts/smoke.sh
```
Builds, starts the service, and drives one chat turn end-to-end, asserting
token-level streaming then a result. Prints `==> OK` on success.

## Terminal co-presence
Because the web session id is used as Claude's `--session-id`, a terminal can
join the same conversation:
```sh
cd "$SPRITE_AGENT_WORKDIR"   # must match the agent's workdir (Claude derives the project dir from cwd)
claude --resume <session-id>
```
Both the web UI and the terminal read/write the same
`~/.claude/projects/<slug>/<session-id>.jsonl`.

## Fleet brain (M4)
With `S3_*` set, the agent registers itself on boot (`fleet/<id>/status.json`,
`fleet/<id>/heartbeat.json`) and `GET /api/fleet` returns the roster derived from
`ListObjects("fleet/")`. See `docs/sprite-agent-V2-plan.md` §4.

## Spawning a worker that boots + registers (M4 + provisioning)
With `SPRITE_API_TOKEN` and `S3_*` set:
```sh
curl -X POST localhost:8080/api/fleet/spawn -d '{"name_prefix":"wk-","role":"worker"}'
```
The agent: creates a sprite, stages its own binary in the brain bucket + presigns a
download URL, warms the new (cold) sprite, then installs a service that fetches and
runs the binary with the bootstrap env. The worker boots `sprite-agent` and
self-registers — it appears in `GET /api/fleet` (`alive:true`) within ~1–2 min.
Set `SPRITE_AGENT_SPAWN_PROVISION=0` for a bare create (no agent installed).
