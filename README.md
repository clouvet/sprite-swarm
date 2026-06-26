# sprite-agent

An opinionated harness for a fleet of **symmetric** Claude Code agents, each running on a
[Sprite](https://fly.io/sprites) microVM. Every instance is identical and fully capable — there is no
special "home" and no "leader" (those are only *hats*: home is a pinned URL; the leader is wherever
your attention is). If home goes away, any worker becomes home; if everything goes away, a new sprite
booted against the brain reconstitutes the whole fleet.

> **Positioning:** this is connective tissue around Claude Code, not a new agent framework. Claude
> Code *is* the agent; sprite-agent provides the session service, fleet brain, spawning, memory, and
> transports.

## What each sprite does

- **Session service** — a web chat UI (PWA) driving the sprite's Claude Code: token-by-token
  streaming (scroll up to read mid-stream without the view yanking back), a rich activity indicator,
  syntax highlighting, evolving chat titles, copy buttons, voice input, and **attachments** (images +
  `doc/docx/xls/xlsx/csv/txt/md`). **Background turns survive disconnect** — close the tab or lock your
  phone mid-task and the work keeps running, replaying in full when you return. Terminal co-presence:
  the web UI and a `claude --resume` terminal share one transcript.
- **GitHub** — its Claude can clone, branch, commit, and open PRs (token from the brain; no creds on
  disk).
- **flyctl** — `fly`/`flyctl` is installed and authenticated on every sprite (token from the brain).
- **Spawn + dispatch** — create another sprite running this same artifact and assign it work; it runs
  in the worker's own session (attach to watch, ask for its status, or pull its result back when done —
  the worker never pushes results at you).
- **Durable workers** — a worker that finishes a feature *persists* (it suspends, cheaply, but isn't
  destroyed); re-attach to it later to iterate on its PR with full context, then **Reap** it.
- **Shared brain** — an S3/Tigris bucket holding the roster, operational secrets, policy, and
  **fleet memory** (a markdown folder synced to/from every sprite, so a new worker boots already
  knowing what the fleet has learned).

## Fleet operations

- **Launch a new fleet** — `scripts/launch-fleet.sh` primes a brain (stages the binary + writes the
  secrets) and ignites a home sprite; everything else reconstitutes from the brain. See below.
- **Delegate & retrieve** — `POST /api/fleet/dispatch {target, task}` runs the task in the worker's
  own session and returns a `session_id`; a direct nudge makes the worker start immediately. Check
  progress with `GET /api/fleet/status?target=<id>` and pull the answer with
  `GET /api/fleet/result?target=<id>&session=<session_id>`. **Home pulls — a worker never pushes its
  result back** (a pushed result would be misread as a new task). `kind:"note"` sends an informational
  FYI that isn't executed.
- **Direct calls** — sprites reach each other over their `.sprites.app` URL with the sprites token as a
  `Bearer` (the dispatch nudge and the status/result pulls ride this); the brain stays the durable
  record + discovery. A human browser instead authenticates through the OAuth gate.
- **Upgrade** — `POST /api/fleet/update` (no body) makes a node self-update **in place**: it fetches the
  staged binary from the brain, verifies it, swaps + re-execs, keeping its VM disk (repo/branch/uncommitted
  work). `{target:"<id>"|"all"}` stages the caller's current binary and rolls that worker / every other
  agent. Each agent reports its binary's `build` hash in the roster, so stale peers are visible (the
  fleet context marks them). Typical flow: rebuild + restart home, then ask it in chat to "update all
  workers." (A node must already run update-capable code; a pre-existing old worker needs one
  reap+respawn to adopt it.)
- **Reap** — the fleet UI's per-worker reap button, or `POST /api/fleet/destroy {target[,force]}`.
  Presence-aware: it refuses (409) if a human is attached, unless `force`.
- **Memory** — sprites read/write `$HOME/.sprite-agent/memory/` (grouped by topic: `repos/`,
  `decisions/`, `how-to/`); it syncs to the brain so the fleet shares learnings.

## Architecture

```
  You ──chat──► sprite's session service (web UI / WebSocket)
                     │  drives ▼
                  claude CLI (stream-json, deterministic --session-id; per-chat working dir)
                     │  transcript ▼
                  ~/.claude/projects/.../<id>.jsonl  ◄── terminal co-presence (claude --resume)

  Fleet brain (S3/Tigris), reached via the s3 connector (token-free, by sprite identity):
    fleet/<id>/{status,heartbeat}.json   per-sprite keys → roster = ListObjects("fleet/")
    fleet/config/secrets/{sprites-api-token,github,fly}   rehydrated on boot (symmetry)
    fleet/config/policy.json             capability/policy control plane
    fleet/memory-fs/<id>/…               frictionless shared memory (synced markdown)
    fleet/tasks/<id>/…                   dispatch inboxes
    fleet/artifacts/sprite-agent-…       the staged binary workers boot
```

Built on **claude-hub's** Go supervision kernel (stream-json parsing, `.jsonl` watcher, WebSocket hub
fan-out), adapted for v2: per-session working dirs, concurrent sessions (a sprite can run a dispatched
task while you attach a separate chat), and stream_event unwrapping for token-level streaming. Sprites
stay awake while working via the local Tasks API (`internal/keepalive`); Claude + the brain are reached
through API-Gateway connectors discovered by sprite identity (`internal/gateway`) — no provider creds
copied onto sprites.

## Layout

| Path | Purpose |
|---|---|
| `cmd/sprite-agent/` | entrypoint + `init` (launch a fleet) |
| `internal/config/` | env-driven configuration |
| `internal/process/` | Claude CLI process supervision (concurrent sessions) |
| `internal/watcher/` | `.jsonl` transcript watcher / history parsing |
| `internal/session/` | per-session state machine |
| `internal/hub/` | WebSocket hub: fan one Claude session out to N clients; attachments |
| `internal/server/` | HTTP server, REST API, uploads, embedded web UI |
| `internal/fleet/` | brain client + roster + secrets + memory + dispatch + policy |
| `internal/spawn/` | sprite spawn/provision + teardown + `LaunchHome` |
| `internal/reaper/` | destroy done workers; clean orphaned brain entries (keep suspended) |
| `internal/keepalive/` | hold the sprite awake while working (Sprite Tasks API) |
| `internal/memsync/` | sync the local markdown fleet-memory dir with the brain |
| `internal/gateway/` | discover API-Gateway connectors (s3 brain, Anthropic) |
| `pkg/claude/` | stream-json protocol types |
| `web/` | embedded PWA chat UI |
| `scripts/launch-fleet.sh` | stand up a brand-new fleet |

## Quick start (run one agent locally)

```sh
go build ./...
go test ./...

# Run the session service (drives the local `claude` CLI):
go run ./cmd/sprite-agent
# then open http://localhost:8080
```

## Launching a fleet

Pre-reqs (once, in the Sprites dashboard): a Tigris bucket + an `s3_object_store` connector pointing
at it, and an `anthropic` connector. Then, from anywhere with Go:

```sh
scripts/launch-fleet.sh --name my-fleet \
  --bucket <tigris-bucket> --s3-access-key <key> --s3-secret-key <secret> \
  --sprites-token <token> [--github-token <token>] [--fly-token <token>]
```

It cross-compiles the binary, primes the brain (stages the artifact + writes the secrets via direct
Tigris S3 keys), and ignites the home sprite, printing its URL. The brain bucket then **stores those
tokens** so every worker reconstitutes from it — guard the bucket's keys + connector; that's the trust
boundary.

See [`docs/RUNBOOK.md`](docs/RUNBOOK.md) for env vars + operations, and
[`docs/sprite-agent-V2-plan.md`](docs/sprite-agent-V2-plan.md) for the design (incl. §0.5 as-built).

## Status

Phases 1 and 2 are built and in daily use. Phase 3 (insertion: take-the-wheel / needs-human) is not
started. See `BUILD_REPORT.md` for what is built-and-verified and `DECISIONS.md` for choices made under
ambiguity.
