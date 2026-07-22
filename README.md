# Sprite Swarm

**Run Claude Code on Sprites from anywhere.** An opinionated harness that runs Claude Code on a
[Sprite](https://fly.io/sprites) microVM behind a web chat UI — then lets one sprite spawn others into
a **symmetric** fleet backed by a shared brain. Drive it from your phone or laptop; leave, and the work
keeps running on the Sprite. Every sprite is identical and fully capable — there is no special "home"
and no "leader" (those are only *hats*: home is a pinned URL; the leader is wherever your attention is).
If home goes away, any worker becomes home; if everything goes away, a new sprite booted against the
brain reconstitutes the whole fleet.

> **Positioning:** connective tissue around Claude Code, not a new framework. It wraps the `claude` CLI
> with a session service, fleet brain, spawning, shared memory, and transports.

_The binary and env vars keep the name `sprite-agent` / `SPRITE_AGENT_*` (built from `cmd/sprite-agent/`) —
that's the one program every sprite runs; "Sprite Swarm" is the fleet they form, and the Go module + repo
are `sprite-swarm`._

## What each sprite does

- **Session service** — a web chat UI (PWA) driving the sprite's Claude Code: token-by-token
  streaming (scroll up to read mid-stream without the view yanking back), a persistent **working
  indicator** with elapsed time (thinking / running a tool / phase), syntax highlighting, evolving chat
  titles, copy buttons, voice input, a **per-conversation model picker** (Fable / Opus / Sonnet /
  Haiku, switchable mid-thread — the transcript resumes so context carries over), and **multi-file
  attachments** (drag-and-drop or the attach button; images + `doc/docx/xls/xlsx/csv/txt/md`). A
  **context view** — a count pill in the header that opens a popover — mirrors what a conversation is
  working with: the git repos in its workspace, the files uploaded to it, and any Discourse topics it
  pulled in (linked back to the posts). **Background turns survive disconnect** — close the tab or
  lock your phone mid-task and the work keeps running, replaying in full when you return. Terminal
  co-presence: the web UI and a `claude --resume` terminal share one transcript.
- **Claude auth** — when a subscription token is configured (from `claude setup-token`, stored in the
  brain and rehydrated fleet-wide), the whole fleet runs Claude Code on your **Claude subscription**, not
  the metered API — the sensible default for a light, single-user fleet. Without a token it falls back to
  the Anthropic API through an identity-authed connector (no key copied), and you can force that per
  sprite with `SPRITE_AGENT_CLAUDE_AUTH=connector`.
- **GitHub** — its Claude can clone, branch, commit, and open PRs (token from the brain; no creds on
  disk).
- **flyctl** — `fly`/`flyctl` is installed and authenticated on every sprite (token from the brain).
- **Discourse** (optional) — with a `discourse` secret configured, its Claude reads your Discourse
  forums **read-only** (paste a topic link → it pulls the thread in) via the official
  [`@discourse/mcp`](https://github.com/discourse/discourse-mcp) server; one profile can serve several
  sites. Absent ⇒ off. See *Launching a fleet* below.
- **Worker env vars** — set in-memory environment variables on a worker (e.g. a `DISCOURSE_API_KEY` a
  dev app needs) from the UI; the harness injects them into every Claude process it spawns, so the
  tools/apps the agent runs inherit them. **RAM-only** — never written to disk or the brain, cleared
  on restart, values can't be read back, and redacted from the chat stream. Changes force-restart
  active sessions so they apply at once. (An in-memory `.env.local` for a dev session — not a secret
  vault; the value still lives in an environment the agent can read, so pair it with scoped/read-only
  credentials.)
- **Spawn + dispatch** — create another sprite (named after its task when you describe one, e.g.
  `wk-posthog-integration`) running this same artifact and assign it work; it runs in the worker's own
  session (attach to watch, ask for its status, or pull its result back when done — the worker never
  pushes results at you).
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
- **Set boot env** — `POST /api/fleet/set-env {target, env}` patches a running sprite's service env and
  restarts it (VM disk survives), so it adopts a new posture without a respawn — e.g.
  `{"env":{"SPRITE_AGENT_BOOT_UPDATE":"0"}}` to pin its build so it won't auto-adopt the staged binary.
  Reserved id/brain/addr keys can't be overridden; same 409-on-attached-human guard as destroy. (Home has
  no managed service, so it's not settable this way.)
- **Reload secrets** — `POST /api/fleet/reload-secrets` re-reads the brain and re-applies the git/gh +
  flyctl creds **in place, no restart**, so newly-spawned subprocesses pick up a rotated token;
  `{target:"all"}` fans it out to every worker. (Sprites-API + Claude tokens are bound at spawn/launch, so
  rotating those still needs a restart.)
- **Host an app** — `POST /api/fleet/deploy-app {artifact_url, run, http_port}` creates a **bare** sprite
  (no agent) that fetches the app tarball (staged in the brain) and runs it on its `http_port`, so the
  app owns that sprite's URL (behind org login). Agent sprites never host apps themselves — the agent
  already owns port 8080 (you'd hit a 409) — they *deploy* to a dedicated sprite. Worker flow: build →
  tar → stage tarball to the brain → `deploy-app` → get the URL.
- **Reap** — the fleet UI's per-sprite destroy button, or `POST /api/fleet/destroy {target[,force]}`.
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
    fleet/config/secrets/{sprites-api-token?,github?,fly?,claude-oauth-token?,discourse?}  rehydrated on boot (all optional)
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
copied onto sprites. Spawn/reap can likewise route through a `custom_api` connector fronting the Sprites
API, so the spawn token need not sit on a sprite or in the brain either (token-free fleet); the brain
secrets are all optional and a fleet runs with only what's connected.

**Capability model:** anything non-optional rides an identity-authed **connector** — the brain
(`s3_object_store`), Claude (`anthropic`), and the Sprites API / spawn-reap (`custom_api`) — so a fleet
needs no stored token to operate. Only the **optional** CLIs use a token: GitHub (`git`/`gh`) and Fly
(`flyctl`), because they need a raw credential and hit endpoints connectors don't proxy. Absent ⇒ that
capability is simply off (the agent is told so).

## Layout

| Path | Purpose |
|---|---|
| `cmd/sprite-agent/` | entrypoint + `init` (launch a fleet) |
| `internal/config/` | env-driven configuration |
| `internal/process/` | Claude CLI process supervision (concurrent sessions) |
| `internal/watcher/` | `.jsonl` transcript watcher / history parsing |
| `internal/session/` | per-session state machine |
| `internal/hub/` | WebSocket hub: fan one Claude session out to N clients; attachments |
| `internal/server/` | HTTP server, REST API, uploads, per-chat context, embedded web UI |
| `internal/secret/` | worker-scoped in-memory env vars injected into Claude processes |
| `internal/fleet/` | brain client + roster + secrets + memory + dispatch + policy |
| `internal/spawn/` | sprite spawn/provision + teardown + `LaunchHome` |
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

## Deploy your own fleet (hosted installer)

The quickest way to stand up a fleet is the hosted one-click installer:
**[deploy-sprite-swarm.fly.dev](https://deploy-sprite-swarm.fly.dev)**. Give it a Fly org token and a
Claude token (plus optional GitHub / Fly / Discourse) — it **mints your Sprites token from the Fly token**,
so there's nothing to copy from the dashboard — and it provisions a storage
bucket, an `s3_object_store` connector for it, the brain, and a home sprite **into your own Fly org**,
then returns your home URL and one-time storage keys. The fleet it stands up is **token-free** — it
registers the connector and runs `init --brain-gateway`, so sprites reach the brain by their own identity
and no S3 keys are copied onto them. Under the hood it builds `sprite-agent` from a pinned commit.

Source — including a walkthrough of exactly how it handles your credentials (used in memory for one
request, never logged or stored) — is [`clouvet/deploy-sprite-swarm`](https://github.com/clouvet/deploy-sprite-swarm).

Prefer the CLI? Use `scripts/launch-fleet.sh` below (also token-free with `--brain-gateway`).

## Launching a fleet

Pre-reqs (once, in the Sprites dashboard): a Tigris bucket + an `s3_object_store` connector pointing
at it, and an `anthropic` connector. Optionally a `custom_api` connector fronting `https://api.sprites.dev`
for token-free spawn (then skip `--sprites-token`). Then, from anywhere with Go:

```sh
scripts/launch-fleet.sh --name my-fleet \
  --bucket <tigris-bucket> --s3-access-key <key> --s3-secret-key <secret> \
  --sprites-token <token> [--github-token <token>] [--fly-token <token>] \
  [--claude-oauth-token <token>] [--discourse-profile <file.json>] \
  [--brain-gateway <s3_object_store connector URL>]
```

**Token-free brain (optional):** pass `--brain-gateway https://api.sprites.dev/v1/gateway/s3_object_store/<id>`
(your `s3_object_store` connector) and the fleet runs token-free — sprites reach the brain by their own
identity and **no S3 keys are copied onto them**. You still pass `--s3-access-key`/`--s3-secret-key` (this
launch host isn't a sprite, so it primes the brain with the keys); the running fleet just doesn't carry
them. This is also how you migrate a key-based fleet (e.g. one stood up by the installer) to the connector:
re-run against the same bucket with `--brain-gateway`, then reap the old workers so they respawn token-free.

**Claude auth:** by default the fleet drives Claude through the **Anthropic connector** (metered API,
authed by sprite identity, no key copied). Pass `--claude-oauth-token` (from `claude setup-token`, run
once on a machine with a browser) and the whole fleet instead uses your **Claude subscription** —
cheaper for a light, single-user fleet. The token is stored in the brain and rehydrated by every
sprite; fall back to the API on any sprite with `SPRITE_AGENT_CLAUDE_AUTH=connector`.

**Discourse (optional):** store a `discourse` secret and the fleet gains read-only access to your
Discourse forums via the [official `@discourse/mcp`](https://github.com/discourse/discourse-mcp)
server — paste a topic link and Claude pulls the thread in as context. The secret is the MCP
profile, an `auth_pairs` array so one server serves several sites (e.g. a private + a public forum):

```
sprite-agent put-secret --name discourse --file profile.json   # {"auth_pairs":[{"site":..,"api_key":..,"api_username":..}]}
```

On boot each sprite writes a `0600` profile + an `mcp.json` and points Claude's `--mcp-config` at it
(read-only; no writes). Skipped when `SPRITE_AGENT_MCP_CONFIG` is set, so an operator-supplied MCP
config wins. Optional by construction: fleets without the secret get no Discourse server.

It cross-compiles the binary, primes the brain (stages the artifact + writes the secrets via direct
Tigris S3 keys), and ignites the home sprite, printing its URL. The brain bucket then **stores those
tokens** so every worker reconstitutes from it — guard the bucket's keys + connector; that's the trust
boundary.

See [`docs/RUNBOOK.md`](docs/RUNBOOK.md) for env vars + operations.

## Credits

The chat UI is set in [MonoLisa](https://www.monolisa.dev/) — MonoLisa Text for the interface, MonoLisa
Code for code and monospace.

## Status

**In active daily use.** The per-sprite session service and fleet coordination are built and running a
live fleet — spawn, dispatch, pull-result, reap, shared memory, in-place upgrade, the web chat UI, and
everything in the capabilities above works today.
