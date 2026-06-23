# DECISIONS

Choices made under ambiguity while building Phase 1 unattended. Format: `<decision> — <why>`.
The build brief instructs taking the documented default and recording it here rather than blocking.

## M1 — Repo bootstrap
- **Go module path `github.com/clouvet/sprite-agent`, Go 1.25** — matches the target repo and the
  installed toolchain (go1.25.1); claude-hub used go1.21 but lifting its code forward is trivial.
- **Layout `cmd/ internal/ web/ pkg/ docs/`** — brief specifies `cmd/ internal/ web/`; added `pkg/`
  for the lifted `pkg/claude` protocol types (as in claude-hub) and `docs/` to vendor the design +
  build briefs into the repo so it builds/reads standalone from a fresh clone.
- **Working branch `phase-1-build`** — brief says "open a working branch"; never force-push, never
  commit straight to `main` after the initial bootstrap.
- **Design doc filename** — the brief references `sprite-agent-v2-plan.md`; the actual file shipped
  is `sprite-agent-V2-plan.md` (capital V). Used the real file.

## M2 — Session service + chat loop
- **`stream_event` envelope unwrapping** — empirically, `claude --include-partial-messages` wraps
  streaming deltas as `{"type":"stream_event","event":{...}}`. The lifted sprite-mobile frontend
  expects the inner event types (`content_block_delta`, `message_stop`, …) at top level, so the hub
  unwraps `stream_event` and forwards `.event` to clients. This is the main adaptation over
  claude-hub (which predates partial-message streaming).
- **Deterministic `--session-id`** — DESIGN §9 drops v1's UUID-rename dance. The web session id *is*
  the Claude session id. Spawn logic: if `<id>.jsonl` exists → `--resume <id>`, else
  `--session-id <id>`. This makes terminal co-presence (`claude --resume <id>`) work directly.
- **Scoped permissions, not `--dangerously-skip-permissions`** — DESIGN §3.1. Default
  `--permission-mode acceptEdits` (overridable via `SPRITE_AGENT_PERMISSION_MODE`), with optional
  `--settings` / `--mcp-config` files. claude-hub used a blanket skip; we do not.
- **Web UI lifted from sprite-mobile v1, trimmed** — brief says "lift/trim v1's chat UI". v1's
  vanilla-JS PWA (`public/`) already speaks the stream-json protocol. Trimmed: the UUID-rename
  client logic (dead under deterministic ids) and multi-session sidebar features not needed for
  Phase 1's single-session focus, kept streaming/markdown/PWA.
- **HTTP port `:8080`** — DESIGN §3.1 says the service claims an `--http-port`; 8080 is the default,
  overridable via `SPRITE_AGENT_ADDR`. The sprites services subsystem maps the public URL to it.

## M3 — GitHub capability
- **gh token via environment (ambient), not gateway-injected** — DESIGN prefers API-Gateway
  connectors, but the brief explicitly permits "env for Phase 1 — record which in DECISIONS". The
  `gh` CLI is already authenticated on the sprite (`gh auth status` → clouvet), and git is
  configured to use it as a credential helper, so the agent's Claude inherits GitHub access with no
  secret copied into the repo. Gateway-injected connectors are a Phase 2 hardening.
- **Scope tools with a `--settings` allow-list, not a blanket skip** — empirically, in headless
  `--print` mode `--permission-mode acceptEdits` gates network/mutating Bash (`git`, `gh`) and, with
  no interactive approver, auto-declines them — so the first PR attempt did nothing. Fix per DESIGN
  §6.2 ("which tools/shell → Claude Code `--settings`/`--allowedTools`"): ship a scoped settings file
  (`internal/config/default-claude-settings.json`) that allows `git`/`gh`/`go`/file tools and denies
  catastrophic commands (`rm -rf /*`, `sudo`, `dd`, `mkfs`, fork-bomb). This is least-privilege, not
  `--dangerously-skip-permissions`.
- **Embed + materialize the settings file** — so the capability is on by default and
  deploy-layout-independent, the settings JSON is embedded in the binary and written to
  `<workdir>/.sprite-agent/claude-settings.json` on boot; `SPRITE_AGENT_SETTINGS` overrides.
- **Note on host settings** — this sprite's host `~/.claude/settings.json` sets
  `defaultMode: bypassPermissions`; the agent nonetheless passes its own scoped `--permission-mode`
  + `--settings`, which is what a least-privilege deploy (without a permissive host default) relies
  on. The scoping is the agent's contribution regardless of host config.
- **Scratch repo `clouvet/sprite-agent-scratch`** — created for the acceptance test; the agent
  opened PR #1 there, left open as evidence.

## M4 — Spawn + minimal brain
- **Spawn is stubbed (no `SPRITE_API_TOKEN`)** — the token was absent from the environment at build
  time. Per the brief, the capability is built behind a clean interface (`internal/spawn`), the
  roster/registration logic is unit-tested, and the live sprites-API call is stubbed and documented.
- **Brain is live (S3/Tigris creds present)** — `S3_*` env vars were present (Tigris, bucket
  `sprite-agent`), so the brain client is real, not stubbed. Self-registration writes
  `fleet/<id>/status.json`; roster = `ListObjects("fleet/")`, per DESIGN §4.1 pattern 1
  (derive-the-index; each sprite writes only its own keys → no concurrent-write corruption).
- **Brain key scoping deferred** — DESIGN §6.3 wants per-agent prefix-scoped S3 creds; the brief
  says that per-prefix scoping is a Phase 2 refinement and Phase 1 uses a single bucket-scoped key.
  Used the provided single key.
- **`apiSpawner` API — verified once a token was provided.** Initially the live call could not be
  exercised (no token), so the endpoint was a documented guess. A token (`cl-sprites`) was supplied
  afterward and the call was corrected + verified against the live API: create is `POST /v1/sprites`
  (the `/v1/orgs/<org>/sprites` guess 404'd); auth is `Bearer <full-token>`; body `{name, env,
  labels}` with `name` required. Verified live via `POST /api/fleet/spawn` (HTTP 200, real sprite
  created, then destroyed). Base URL defaults to `https://api.sprites.dev`, overridable via
  `SPRITE_API_BASE`.
- **Spawn provisioning — built (Phase 1.5, user-requested).** A bare create yields a base sprite that
  doesn't run sprite-agent; full "spawn → boot → register" was then built and verified. Decisions:
  - **Provision over the services API + a presigned URL, not exec/fs.** exec and fs are multiplexed
    over a control WebSocket (`sprite-capabilities: control-ws`) that plain HTTP can't drive; the
    services API is plain REST. So the spawner stages its own binary to the brain bucket, presigns a
    GET URL, and installs a service that curls+runs it. Avoids an SDK dependency and a build/clone on
    the worker; only S3 creds (carried in the bootstrap env) are needed.
  - **Stage the binary in the brain bucket** (`fleet/artifacts/sprite-agent-linux-amd64`) rather than
    cloning+building on the worker — self-contained binary (go:embed UI), no GitHub auth needed on the
    worker for registration. Re-staged per spawn (simple; dedup is a later optimization).
  - **Warm before provisioning.** A cold sprite accepts a service PUT (200) but doesn't persist it;
    the spawner warms via `POST …/exec`, polls status until non-cold, PUTs, then confirms + retries
    once. This was the difference between a worker that registers and one that never boots.
  - **`SPRITE_AGENT_SPAWN_PROVISION=0`** opts back into a bare create; provisioning requires a brain.
- **Auto-reap — reaper-on-home, not self-destruct (user-requested).** Workers are reaped by a
  token-bearing agent's reaper, not by destroying themselves, so the privileged sprites token never
  has to be handed to workers (least privilege). A worker only *advertises* `Reapable` in its status
  (idle past threshold, or `POST /api/fleet/done`); the reaper decides and destroys. **Home is always
  protected** (`fleet.ReapTargets` skips role=home). Dead workers (stale heartbeat) are also cleaned
  up. Sprite is destroyed before its brain entry; a failed destroy keeps the entry for retry. PR-merge
  auto-detection isn't built — `POST /api/fleet/done` is the hook an orchestrator calls post-merge.
- **Spawn addressable over the same API** — `POST /api/fleet/spawn` returns `501` with a clear
  reason when stubbed (capability present, live call not), keeping seam #2 (agent talks to the fleet
  over the same API a human uses) honest.
- **Fleet affordance via `--append-system-prompt`** — DESIGN §5 ("nothing tells it that's an
  option"): a static blurb at spawn tells the agent it's a fleet peer, where the roster is
  (`/api/fleet`), and how/whether it can spawn. Per-turn live-state injection is Phase 2.
- **Multi-agent roster demonstrated by two registrations** — spawn is stubbed, so the M4
  "A spawns B, both in roster" shape was proven by running two real agents (home + worker) that
  register into live Tigris; the roster logic is also unit-tested with a fake brain. Test entries
  were cleaned from the bucket afterward.

## Cross-cutting
- **Session metadata: in-memory + JSON file** (`<workdir>/.sprite-agent/sessions.json`) rather than
  the embedded SQLite mentioned in DESIGN §3.1 — Phase 1 needs only titles/timestamps for the UI
  list; the transcript `.jsonl` is the source of truth. SQLite can come when metadata grows.
- **UUIDs without a dependency** — `crypto/rand` v4 UUIDs (Claude's `--session-id` needs a valid
  UUID); avoids adding a uuid module.
- **Markdown via CDN with graceful fallback** — the embedded UI loads `marked` from a CDN for rich
  rendering but degrades to escaped plaintext if offline, keeping the binary self-contained while
  avoiding bundling a markdown parser.
- **Commit attribution** — used the Co-Authored-By trailer required by this environment's
  conventions (sprite-mobile v1's "no co-author" rule is a different repo's policy and does not
  apply here).
