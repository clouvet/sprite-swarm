# BUILD_REPORT — sprite-agent Phase 1

Autonomous build of Phase 1 (DESIGN §8 / BUILD brief M1–M4). Open PR:
**https://github.com/clouvet/sprite-agent/pull/1** (`phase-1-build` → `main`).

`go build ./...`, `go vet ./...`, and `go test ./...` are green from a fresh clone.

## Inputs detected at startup
| Input | Present? | Effect |
|---|---|---|
| `claude` CLI (2.1.186) | ✅ authed | M2+ live |
| `gh` CLI (clouvet, repo scope) | ✅ | M1/M3 live |
| Go toolchain (1.25.1) | ✅ | build |
| claude-hub source | ✅ cloned for reference | kernel lifted |
| S3/Tigris creds + bucket `sprite-agent` | ✅ | **brain live** |
| `SPRITE_API_TOKEN` | ❌ at build → ✅ provided after | spawn **stubbed at build**, then **live-verified** once the token arrived (create call confirmed; see below) |
| Target repo `clouvet/sprite-agent` (empty) | ✅ | pushed into it |

## Built and verified

### M1 — Repo bootstrap
- Go module `github.com/clouvet/sprite-agent`, layout `cmd/ internal/ pkg/ web/ docs/`, `.gitignore`, README.
- **Proof:** `go build ./...` green; initial commit pushed to `main`; working branch `phase-1-build`.

### M2 — Session service + web UI + Claude chat loop  *(riskiest slice, done first)*
- Lifted claude-hub's kernel: `pkg/claude` (stream-json types), `internal/process` (process
  supervision, singleton, grace period), `internal/watcher` (`.jsonl` tail), `internal/hub`
  (WS fan-out), `internal/session` (state machine).
- Drives `claude --print --output-format stream-json --input-format stream-json
  --include-partial-messages --permission-mode <mode> [--settings …]`, with a **deterministic
  `--session-id`** (resumes when the transcript already exists). Unwraps the `stream_event`
  envelope so the UI receives top-level `content_block_*`/`message_*`; drops the redundant full
  `assistant` message to avoid double-render.
- Embedded PWA via `go:embed` (`web/assets`, trimmed from sprite-mobile v1). REST: `/api/sessions`,
  `/health`, `/api/fleet`, `/api/fleet/spawn`; WS `/ws?session=<id>`.
- **Proof — streaming:** `./scripts/smoke.sh` (and `go run ./cmd/smoke`) creates a session, opens
  the WS, sends a message, and asserts it observed token-level `text_delta` then a `result`:
  → `streamed reply: "SMOKE_OK"` … `SMOKE PASS`.
- **Proof — co-presence:** the web spawn used `--session-id <id>`, writing
  `~/.claude/projects/-home-sprite/<id>.jsonl`; a terminal `claude --resume <id>` (run from the same
  workdir) loaded that conversation (recalled the earlier reply) and **appended to the same file**
  (13361 → 18236 bytes). Web and terminal share one transcript.
- **Unit tests:** `stream_event` unwrapping (`pkg/claude`), transcript parsing incl. marker
  filtering (`internal/watcher`).

### M3 — GitHub capability
- In headless `--print` mode, `acceptEdits` gates network/mutating Bash (`git`/`gh`) and auto-declines
  with no approver. Fixed by scoping tools via a `--settings` **allow-list** (git/gh/go/file tools)
  with a deny-list for catastrophic commands — embedded in the binary and materialized to
  `<workdir>/.sprite-agent/claude-settings.json` on boot (`SPRITE_AGENT_SETTINGS` overrides).
- gh token is ambient (env / git credential-helper) for Phase 1, per the brief.
- **Proof:** driven **over the session-service WebSocket** (seam #2 — the same path a human uses),
  the agent cloned `clouvet/sprite-agent-scratch`, branched, committed, pushed, and ran `gh pr create`
  → **PR #1 OPEN** at https://github.com/clouvet/sprite-agent-scratch/pull/1
  (`gh pr list` confirms `state: OPEN`, branch `sprite-agent-m3`).

### M4 — Minimal fleet brain *(live)*
- `internal/fleet`: a `Brain` interface with an S3/Tigris implementation. On boot the agent
  `Register`s by writing **only its own keys** (`fleet/<id>/status.json`, `fleet/<id>/heartbeat.json`),
  refreshes a heartbeat on an interval, and derives the roster from `ListObjects("fleet/")` + a pure
  `BuildRoster` merge (DESIGN §4.1 pattern 1 — no shared index to race on). `GET /api/fleet` returns it.
- **Proof — live:** two agents (`agent-A` role=home, `agent-B` role=worker) registered into real
  Tigris (`s3://sprite-agent`); `GET /api/fleet` on **either** returned both, each `alive:true`.
  (Test keys deleted from the bucket afterward.)
- **Unit tests:** roster liveness/TTL/sort/key-derivation; `Register`→roster round-trip and
  multi-agent roster against an in-memory fake brain.

### Two seams (DESIGN §7)
- **#1 workers are real sessions:** every sprite runs this same session service and is addressable
  as a chat. Verified by the service itself.
- **#2 agent ↔ fleet over the human's API:** the agent reaches the fleet via `/api/fleet` and
  `/api/fleet/spawn` — the same REST/session surface a human uses; the fleet affordance prompt points
  the agent there.

### Spawn — live call verified (token provided post-build); artifact provisioning remains
A `SPRITE_API_TOKEN` was supplied after the initial build, so the live call was corrected and verified:
- **Corrected against the live API** (org `cl-sprites`): the create endpoint is `POST /v1/sprites`
  (the initial `/v1/orgs/<org>/sprites` guess was wrong → 404); auth is `Authorization: Bearer
  <full-token>`; the body is `{name (required), env, labels}`. `apiSpawner` and its tests were updated.
- **Verified end-to-end:** with the token loaded, the running agent's own endpoint
  `POST /api/fleet/spawn {"name_prefix":"wk-","role":"worker"}` returned **HTTP 200** and created a
  real sprite (`wk-080551f5`, with id + `…sprites.app` URL) — i.e. the agent created another sprite
  over the same REST API a human uses (seam #2). The test sprite was then destroyed (DELETE → 204).
- **Remaining gap — artifact provisioning (the honest limitation):** a bare create yields a
  *base-environment* sprite; it does **not** run `sprite-agent`, so it does **not** register
  (`GET /api/fleet` showed only the home agent after the spawn, as expected). Making a spawned sprite
  boot this artifact and self-register needs a follow-up provisioning step on the new sprite — push or
  `git clone`+`go build` the binary, then run it as a service (`--http-port`) with the bootstrap env
  (already assembled by `BootstrapEnv`: brain pointer + artifact + role + id). That sequence (via the
  sprites exec/fs API) is the next increment; the create call and bootstrap env it depends on are now
  done and verified.
- **Unit-tested:** token parse (incl. malformed), `New` stub-vs-live selection, `BootstrapEnv`
  (with/without brain), create-request assembly + name synthesis.

### Per-prefix brain credential scoping (DESIGN §6.3) — Phase 2
Phase 1 uses one bucket-scoped key (per the brief). Phase 2: per-agent prefix-scoped creds
(`fleet/<id>/*` write) + `fleet/config/*` write-protection.

## Assumptions
Mirror of `DECISIONS.md` — notably: deterministic `--session-id` (drops v1's rename dance); scoped
`--settings` allow-list instead of `--dangerously-skip-permissions`; gh via ambient env for Phase 1;
brain live / spawn stubbed; session metadata as in-memory+JSON (not SQLite yet); markdown via CDN with
offline fallback.

## How to run from scratch
```sh
git clone https://github.com/clouvet/sprite-agent.git && cd sprite-agent
go build ./... && go test ./...

# session service (drives the local authenticated `claude` CLI):
go build -o sprite-agent ./cmd/sprite-agent
./sprite-agent                      # http://localhost:8080

# scripted M2 acceptance:
./scripts/smoke.sh

# with the fleet brain (M4): export S3_BUCKET/S3_REGION/S3_ENDPOINT/S3_ACCESS_KEY/S3_SECRET_KEY
#   then GET /api/fleet returns the roster.
# with live spawn (M4): export SPRITE_API_TOKEN (+ SPRITE_API_BASE if needed).
```
Full env-var reference and terminal co-presence steps: `docs/RUNBOOK.md`.

## Out of scope (Phase 2/3, not started)
Task dispatch/assignment, durable shared memory, presence-routing, the fleet UI
(attach-to-worker), the capability/policy control plane, chat bridges, take-the-wheel / needs-human.
