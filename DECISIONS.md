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
