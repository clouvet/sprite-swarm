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
| `SPRITE_AGENT_IDLE_REAP_MINUTES` | `0` | This agent self-declares reapable after idle this long (0 = never). Home ignores it. |
| `SPRITE_AGENT_WORKER_IDLE_REAP_MINUTES` | `0` (off) | Idle-reap threshold baked into spawned workers. Off by default — the reaper is not PR-aware, so an idle worker may be awaiting review of an open PR. Enable only for fire-and-forget workers. |
| `SPRITE_AGENT_REAP_INTERVAL_SECONDS` | `60` | How often the reaper scans (token-bearing agents only). |
| `SPRITE_AGENT_DEAD_REAP_MINUTES` | `5` | Reap a worker whose heartbeat has been stale beyond this (crashed sprite cleanup). |

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
With `S3_*` set — or, on a sprite, by **auto-discovering the `s3_object_store`
connector** (token-free, by sprite identity; no `S3_*` needed) — the agent registers
itself on boot (`fleet/<id>/status.json`, `fleet/<id>/heartbeat.json`) and
`GET /api/fleet` returns the roster from `ListObjects("fleet/")`. See §4.

## Operational secrets (brain)
Every sprite rehydrates the same secrets from `fleet/config/secrets/` on boot, so any
sprite is equally capable (env values win if explicitly set):
- `sprites-api-token` → spawn/reap/teardown (`SPRITE_API_TOKEN`).
- `github` → git/gh (loaded into process env + a `GIT_CONFIG_*` credential helper; never on disk).
- `fly` → flyctl (`FLY_API_TOKEN`; `flyctl` auto-installed to `~/.fly/bin` if missing).

Seed them with `launch-fleet.sh` (below), or store/update one via the brain directly.
The brain bucket holding these is the fleet's trust boundary — guard its keys + connector.

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

## Long-running tasks (durable background turns)
A turn keeps running when you disconnect. Closing the browser / locking the phone
does **not** abort the work: the grace timer reaps a session's `claude` process only
once it's **idle** with no client attached — while a turn is still generating the
process is kept alive and re-checked (`process.Manager.StartGracePeriod`). The
transcript is written to disk throughout and keepalive holds the VM awake while any
session generates (`hub.IsIdle` checks generation first), so you can start a long
task, leave, and on **refresh/re-attach** the session replays the full history of
work done while you were away — the web equivalent of detaching and re-attaching a
terminal `claude` session.

## Progress visibility across the fleet
Delegated work runs in the **worker's own session**. To watch it, click the worker in
the fleet list — that opens the worker's own URL (a human browser passes the OAuth
gate that blocks *cross-sprite* calls), and durable turns mean its chat shows live
progress on refresh. To ask a *peer* ("how is wk-3 doing?") without attaching, read
its **phase**: each agent self-reports a one-line status to the brain with
`POST /api/fleet/phase {"phase":"<what I'm doing / done: <result>>"}`, which appears in
the roster (`GET /api/fleet`) and in every peer's injected fleet context. A worker
can't read another's transcript, so this note is the cross-agent progress channel;
agents are told (in the fleet affordance) to keep it current, especially on dispatched
work.

## Durable workers + reaping
Workers are **durable workspaces, not one-shots.** A worker that finishes a feature
goes idle and *suspends* (cheap; the keep-awake task releases) — its VM disk (repo +
branch) and session transcript **survive**, so you re-attach later to iterate on its
PR with full context. A **stale heartbeat no longer destroys** a worker (a suspended
worker is indistinguishable from a crashed one over the heartbeat).

Token-bearing agents run a **reaper** that:
- **destroys** only workers that explicitly self-declared done (`POST /api/fleet/done`),
  removing their brain entry (`fleet.ReapTargets`);
- **cleans the brain entry** of a stale worker only if its sprite is **actually gone**
  (verified via `spawn.Exists`) — orphan cleanup, never destroying a live/suspended one
  (`fleet.StaleWorkers`).

**Home is never reaped.** Teardown on demand is presence-aware:
`POST /api/fleet/destroy {"target":"<id>"[, "force":true]}` (the fleet UI's per-worker
**Reap** button) destroys the VM + cleans the brain entry, refusing with **409** if a
human is attached unless `force`. Reaping never deletes the PR/branch (those live on
GitHub) nor the durable shared memory (a separate brain prefix).

Optional idle-reap (`SPRITE_AGENT_WORKER_IDLE_REAP_MINUTES`) is **off by default** — the
reaper isn't PR-aware, so leave it off for workers awaiting review.

## Launching a new fleet
`scripts/launch-fleet.sh` stands up a brand-new fleet from anywhere with Go. Pre-reqs
(once, in the Sprites dashboard): a Tigris bucket + an `s3_object_store` connector
pointing at it, and an `anthropic` connector.

```sh
scripts/launch-fleet.sh --name my-fleet \
  --bucket <tigris-bucket> --s3-access-key <key> --s3-secret-key <secret> \
  [--s3-endpoint https://fly.storage.tigris.dev] \
  --sprites-token <token> [--github-token <token>] [--fly-token <token>]
```

It cross-compiles the linux artifact, then `sprite-agent init` primes the brain (stages
the binary + writes the `sprites-api-token`/`github`/`fly` secrets via **direct S3
keys**) and ignites the home sprite (`role=home`), printing its URL. The home boots,
**self-discovers the s3 + Anthropic connectors**, rehydrates the secrets, and is a live
fleet; subsequent workers reconstitute from the brain. The brain bucket then **stores
those tokens** — guard its keys + connector (that's the fleet's trust boundary).
