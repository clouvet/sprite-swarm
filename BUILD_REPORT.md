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

### Spawn — create + full provisioning, live-verified
A `SPRITE_API_TOKEN` was supplied after the initial build; the live call was corrected and the
spawned worker now boots this artifact and self-registers.
- **Create corrected against the live API** (org `cl-sprites`): endpoint is `POST /v1/sprites` (the
  initial `/v1/orgs/<org>/sprites` guess → 404); auth `Authorization: Bearer <full-token>`; body
  `{name (required), env, labels}`.
- **Provisioning** (`internal/spawn/artifact.go` + `api.go`): exec/fs are control-ws (SDK-only), so
  provisioning uses the **plain-REST services API + a presigned URL**: the spawner stages its own
  binary to the brain bucket (`fleet/artifacts/…`), presigns a GET URL, and installs a service on the
  new sprite (`PUT /v1/sprites/<name>/services/sprite-agent`) whose command curls the binary and runs
  it with the bootstrap env (S3 pointer + agent id/role/artifact) and an `http_port` for keep-alive.
- **Cold-sprite gotcha (found + fixed):** a freshly-created sprite is *cold*; a service PUT to it
  returns 200 but does **not** persist. The spawner now **warms** the sprite (via a `POST …/exec`)
  and polls status until non-cold, then PUTs and **confirms** the service stuck (retrying once).
- **Verified end-to-end:** `POST /api/fleet/spawn {"name_prefix":"wk-","role":"worker"}` on the
  running agent created `wk-6f56df08`, which downloaded the binary, booted, and **self-registered** —
  `GET /api/fleet` on the home agent then showed it `alive:true` (seam #2: the agent created and
  provisioned a peer over the same REST API a human uses). Test sprites destroyed + brain keys cleaned.
- **Tested:** token parse, `New` stub-vs-live, `BootstrapEnv`, create-request + name synthesis, and an
  `httptest` fake of the sprites API locking the warm→PUT→confirm flow (cold-sprite behavior included).

### Auto-reap — workers come and go, verified
Spawned workers clean themselves up so the fleet doesn't accumulate zombies (DESIGN §2.3).
- **Policy** (`fleet.ReapTargets`, pure + unit-tested): reap a worker that self-declared `Reapable`
  (idle past its threshold, or told done) or whose heartbeat is stale beyond `DeadReapAfter`
  (crashed sprite). **Home is never reaped** (DESIGN §4.2 — home is pinned).
- **Reaper** (`internal/reaper`): runs only on token-bearing agents; each scan destroys reap targets
  via the sprites API, then removes their brain entries (sprite first, brain second; a failed destroy
  leaves the brain entry for retry). Workers never destroy themselves — the privileged token stays on
  the reaper.
- **Triggers:** explicit done (`POST /api/fleet/done`, e.g. **after a PR merges**), a dead heartbeat,
  or — **only if opted in** — idle. Idle reaping defaults **off** (`SPRITE_AGENT_WORKER_IDLE_REAP_MINUTES=0`)
  because the reaper is not PR-aware: a worker awaiting human review of an open PR is "idle" and must
  not be auto-destroyed (DESIGN §10: reap *after the PR merges*, a human event). PR/branch survive on
  GitHub regardless of reaping.
- **Memory safety:** `RemoveAgent` deletes only the two coordination keys (status + heartbeat), never
  a blanket `fleet/<id>/*` prefix, so future durable shared memory (Phase 2, under a separate
  `fleet/memory/…` prefix per DESIGN §4.1) can never be wiped by reaping a worker.
- **Verified end-to-end:** spawned `wk-c4fd419b` (1-min idle-reap, 20s reaper); it registered, sat
  idle, and at **+152s** the reaper logged `reaped wk-c4fd419b (destroyed sprite + removed brain
  entry)` — `GET /v1/sprites` then showed no `wk-` sprites. Tests cover the policy, the idle→reapable
  transition (home exempt), and the reaper loop (destroy-then-remove; keep-on-failure).

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

## Out of scope for Phase 1 (Phase 2 now built — see below; Phase 3 not started)
Phase 3 (insertion): take-the-wheel handoff, needs-human signals. Chat bridges (Slack/Telegram).

---

# Phase 2 — Coordination (built after Phase 1 merged)

Turns spawned instances into a *coordinated* fleet. All five pieces built, tested, and
(where live infra allows) verified end-to-end. Branch `phase-2-build`.

## Key platform finding that shaped the transport
A private sprite's public URL is gated by **Fly OAuth** (a cross-sprite HTTP call returns the
"Authentication Required" login wall, not the app). So agent→agent delivery can't go directly over a
worker's session URL without making workers public (which would expose the chat UI). Decisions that
follow from this are noted per-feature.

## P2.1 — Dispatch (assign work to a peer)  ✅ verified
- The brain is the authenticated delivery channel (given the OAuth wall): `Dispatch` writes
  `fleet/tasks/<to>/<ts>-<uuid>.json` (append-only **visible fleet state**, which DESIGN §10 wants);
  every agent polls its own inbox and **injects** unseen tasks into a local session via
  `hub.InjectMessage`, so the task materializes in the worker's real transcript (**seam #2 holds**).
  Dedup via a per-agent `seen-tasks.json`. `POST /api/fleet/dispatch {target, task}`.
- **Deviation recorded:** DESIGN §10 specifies the session API as the delivery *transport*; the OAuth
  gate forces brain-as-transport. The seam (task lands in the worker's session) is preserved.
- **Verified:** dispatch-to-self → poller "accepted task → session" → Claude replied in the transcript.
  And cross-sprite (below) once workers could run Claude.

## P2.2 — Durable shared memory  ✅ verified
- Append-only per-author entries `fleet/memory/<id>/<ts>-<uuid>.json`; always-loaded **index** +
  on-demand **body** retrieval (the §4 scaling rule). REST: `POST/GET /api/memory`,
  `GET /api/memory/{author}/{id}`, `GET /api/memory/context`. Lives under a separate prefix so it
  **survives reaping** (RemoveAgent touches only status+heartbeat).
- **Verified:** write → index (no bodies) → get body; unit test asserts memory outlives a reaped worker.

## P2.3 — Presence-routing + live fleet-state injection  ✅ verified (unit)
- Each agent advertises whether a human is attached + to which session (`Status.Present/Session` via a
  hub attendance probe, §2.4). `GET /api/fleet/context` renders live roster + presence + memory index;
  a `UserPromptSubmit` hook injects it **each turn** (DESIGN §5) so the agent knows who exists, what
  they're doing, where the human is — with an explicit **DEFER** instruction for attended workers.
- **Verified:** unit test asserts the context flags an attended worker DEFER and lists titles (no bodies).

## P2.4 — Fleet UI + attach-to-worker  ✅ built
- Sidebar shows alive dot, role, presence (👤) / reapable (⌛) badges, phase; a **+ worker** button
  spawns. Clicking an agent **attaches**: opens its session URL in a new tab — the human is an org
  member so their browser passes the OAuth gate (the clean answer to cross-sprite reachability for the
  human). Agents advertise their URL in the roster; the spawner hands each worker its own URL.

## P2.5 — Capability/policy control plane  ✅ verified
- Layered policy (defaults → role → per-agent override, most-specific wins) in
  `fleet/config/policy.json`; built-in baseline so partial docs inherit. Real enforcement:
  `tools.permission_mode` drives the agent's Claude `--permission-mode`; `spawn.max_total` gates
  `POST /api/fleet/spawn` (403 when over). Visibility: `GET /api/policy` + a UI policy line.
- **Guardrail (can-modify-policy human-held):** agents only READ `fleet/config/*`; no agent code
  writes it. `docs/fleet-policy.example.json` is the operator write path.
- **Verified:** control-plane doc in brain → `/api/policy` reflects it; `permission_mode=plan` applied;
  spawn cap 0 → **403, no sprite created**.

## Phase 2 — built behind a tradeoff / remaining hardening
- **Worker Claude auth = API Gateway connector (no creds copied).** A fresh sprite has no Anthropic
  creds, so dispatched workers could register but not *run* Claude. The spawner now discovers the
  org's **Anthropic connector** (`GET /v1/gateway/list`) and hands the worker
  `ANTHROPIC_BASE_URL=<gateway base>` + a placeholder key, so the worker's Claude authenticates by the
  sprite's own Fly identity through the gateway — **no token on the box** (DESIGN §3.2). Connectors are
  `allow_all` for this org, so any worker qualifies. **Verified:** a worker spawned with *no* copied
  credential processed a dispatched Claude task in ~20s (dispatch→pull→inject→act). The earlier
  credential-copy is retained only as an explicit fallback (`SPRITE_AGENT_PROPAGATE_CLAUDE_CREDS=1`)
  for orgs without an Anthropic connector. See `internal/gateway`.
- **GitHub on workers — REST via gateway works; git transport does not.** The GitHub connector gateway
  proxies the GitHub **REST API** (`/user`, `/repos/...`, PR/issue creation) authenticated by sprite
  identity — verified (`GET …/gateway/github/<id>/user` → 200). But `git` clone/push uses smart-HTTP
  against github.com, which the REST gateway does not proxy, so a worker that must clone/commit/**push
  code** still needs a git credential (which the gateway model deliberately avoids). Options: do PR/file
  ops via the gateway REST; or hand workers a scoped git credential; or use a GitHub App installation
  token. Not yet wired — surfaced for a decision.
- **Storage-level policy/identity scoping (DESIGN §6.2/§6.3):** the guardrail and per-agent brain
  integrity are enforced at the *app layer* (no write code path) but not yet at the *storage* layer
  (Phase 1/2 use one bucket-scoped key). Per-prefix-scoped creds (`fleet/<id>/*` write,
  `fleet/config/*` read-only) are the remaining hardening to make it physically enforced.
- **Dispatch delivery** is brain-pull, not session-API-push (the OAuth-wall deviation above).

## Phase 2 — how to use
```
POST /api/fleet/spawn   {name_prefix, role}      # create + provision a worker
POST /api/fleet/dispatch {target, task}          # assign work (lands in the worker's session)
POST /api/fleet/done                             # mark self done (reap after PR merges)
GET  /api/fleet         | /api/fleet/context     # roster (json) | live context (text, hook-injected)
GET/POST /api/memory    | GET /api/memory/{a}/{id}  # shared memory index/write | body
GET  /api/policy                                 # effective capability policy
```
Control-plane policy: write `docs/fleet-policy.example.json` to brain key `fleet/config/policy.json`.
