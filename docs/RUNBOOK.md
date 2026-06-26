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
| `SPRITE_API_TOKEN` | _(unset)_ | sprites API token (`org-slug/org-id/token-id/token-value`) for live spawn. If unset, spawn falls back to a `custom_api` connector; stubbed only if neither is available. |
| `SPRITE_API_BASE` | `https://api.sprites.dev` | sprites API base URL (token mode). |
| `SPRITE_API_GATEWAY` | _(unset)_ | gateway base URL of a `custom_api` connector fronting the Sprites API (token-free spawn). Auto-discovered when no token; set to override. |
| `SPRITE_API_CONNECTOR_ID` | _(unset)_ | pin which `custom_api` connector to use for the Sprites API (since `custom_api` is generic); empty = first discovered. |
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
Capability model: **non-optional capabilities ride identity-authed connectors** (the brain via
`s3_object_store`, Claude via `anthropic`, the Sprites API via `custom_api`) — no stored token needed.
A token is used only for the **optional** CLIs that need a raw credential and hit endpoints connectors
don't proxy: GitHub (`git`/`gh`) and Fly (`flyctl`). So the secrets below are all optional.

Every sprite rehydrates the same secrets from `fleet/config/secrets/` on boot, so any
sprite is equally capable (env values win if explicitly set). All are **optional** —
an absent one just means that capability isn't wired (nothing fails):
- `sprites-api-token` → spawn/reap/teardown (`SPRITE_API_TOKEN`). **Optional:** if absent,
  spawn falls back to a `custom_api` gateway connector (see below) — token-free.
- `github` → git/gh (loaded into process env + a `GIT_CONFIG_*` credential helper; never on disk).
- `fly` → flyctl (`FLY_API_TOKEN`; `flyctl` auto-installed to `~/.fly/bin` if missing).

Seed them with `launch-fleet.sh` (below), or store/update one via the brain directly.
The brain bucket holding these is the fleet's trust boundary — guard its keys + connector.

### Token-free spawn (Sprites API via a `custom_api` connector)
The spawn token need not live on a sprite or in the brain. Create a **Custom API**
connector (Sprites dashboard) with upstream `https://api.sprites.dev`, injecting the
token as `Authorization: Bearer`. When no `sprites-api-token` is present, the agent
discovers it (`gateway.SpritesAPIBase`, pinned by `SPRITE_API_CONNECTOR_ID` since
`custom_api` is generic) and routes spawn/reap through `<gateway>/v1/sprites/...` with
no auth header — the gateway injects the credential by sprite identity, like the s3 /
Anthropic connectors. Drop the brain secret to cut a fleet over: home + every future
worker go token-free at once (workers never receive the token in their boot env). This
covers sprite-agent's own `/api/fleet/*` ops only — the `sprite` CLI / direct
`api.sprites.dev` calls use separate auth and won't work token-free.

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

## Direct sprite-to-sprite calls
Sprites call each other's session service directly: hit the peer's **`.sprites.app`
URL** (the one stored in the roster `url`) with the **sprites token as a `Bearer`**
(`Authorization: Bearer $SPRITE_API_TOKEN` — the same token + format the spawn API
uses), and the request reaches its sprite-agent app. That auth mode ("sprite", the
URL default) admits org members via browser **and** org tokens. Unauthenticated
browser access is OAuth-gated (302), which is why a human just clicks a worker to open
it, while agents present the bearer. (Note: `<sprite>-<org>.sprites.dev` is *not* the
app ingress — it returns a platform 404; use the `.sprites.app` URL.) The brain stays
the durable record + discovery; these direct calls carry the live, low-latency
coordination below.

## Delegating work: dispatch → status → result (pull-based)
Delegation follows one fixed protocol (baked into the fleet affordance so agents don't
improvise):
1. **Assign** — `POST /api/fleet/dispatch {"target":"<id>","task":"…"}`. The task is
   recorded durably in the brain (`fleet/tasks/<id>/`) **and** the assigner fires a
   content-free nudge (`POST /api/fleet/nudge`) at the target so it drains and starts
   **immediately** instead of waiting for its slow backstop poll (a nudge also wakes a
   suspended worker). The response carries a `session_id` — the worker's session for
   this work. Add `"kind":"note"` to send an informational FYI that is delivered but
   **not executed** (the guardrail against a stray message being run as a task).
2. **Progress** — `GET /api/fleet/status?target=<id>` merges the peer's last-published
   **phase** (`POST /api/fleet/phase {"phase":"…"}`, surfaced in the roster + injected
   context) with a live authenticated pull of its `/health` (generating now? sessions?).
3. **Result** — `GET /api/fleet/result?target=<id>&session=<session_id>` pulls the
   worker's final message from that session (`ready:true` once done). **Home pulls; a
   worker never dispatches its result back** — a result delivered through dispatch is
   misread as a new task and executed, which is exactly how a runaway happens.

To *watch* a worker directly, click it in the fleet list — that opens its own URL in
your browser, and durable turns mean its chat shows live progress on refresh.

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

## Upgrading running workers (in-place self-update)
A worker runs the binary it booted with — spawn hands it home's binary at spawn time,
and it doesn't change afterward. To push new code to a *running* worker without
reap+respawn (which would lose its VM disk):

Each agent hashes its own binary at boot and reports it as `build` in the roster
(`GET /api/fleet`) and the injected fleet context, which flags peers on a different
build than yours as **(stale)** — so "who needs updating" is visible.

- **`POST /api/fleet/update`** (no body) — this node self-updates: fetch the staged
  binary from the brain (`config.ArtifactKey`), and if it differs from the running
  build, verify it (ELF + size), swap it in place (old kept as `<binary>.bak`),
  respond, then re-exec. The VM disk (repo/branch/uncommitted work) survives.
- **`POST /api/fleet/update {"target":"<id>"|"all"}`** — the caller stages its own
  current binary to the brain, then tells that worker / every other agent to
  self-update via direct authenticated (Bearer) calls.

Typical rollout: rebuild + restart home, then `POST /api/fleet/update {"target":"all"}`
(or just ask home in chat to "update all workers"). **Bootstrap caveat:** a node must
already run update-capable code — a pre-existing worker on an older build `404`s on
`/api/fleet/update` and needs one reap+respawn to adopt the mechanism; after that it
updates in place. The `build` hash is of the compiled binary (Go builds aren't
byte-reproducible), so any home rebuild marks workers stale until rolled.

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
