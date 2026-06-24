# Sprite Fleet (`sprite-agent`) — v2 Design Plan

> **Status:** **Phases 1 and 2 built and verified** (Phase 3 not started). App name: **`sprite-agent`**
> — repo `https://github.com/clouvet/sprite-agent`. Build brief: `sprite-agent-BUILD.md`.
> **Date:** 2026-06-22 (design); reconciled with the as-built implementation 2026-06-23.
> **Relationship to v1:** current `sprite-mobile` is **v1**. This is **v2**, reusing v1's web UI
> components. v1 stays as-is for reference/fallback.
>
> **Reading this doc:** the sections below are the original design intent. Where the implementation
> diverged from or realized that intent, see **§0.5 As-built status** (next) — it is authoritative for
> current reality, and load-bearing sections carry inline **(As built: …)** notes.

---

## 0. What this is (positioning)

This is **an opinionated agent harness** — a *fabric* for running and coordinating a fleet of
agents — not a new agent framework. **Claude Code is the agent;** we don't build a planner,
reasoning loop, or tool-dispatch engine. Everything here is **connective tissue around Claude Code
instances**: session service, fleet brain, spawning, presence, transports.

Mental model: Claude Code already has in-process subagents (the Task tool) — ephemeral, single
machine, no persistence, no shared memory. **This is the distributed, persistent, isolated version
of that** — subagents that are real machines, outlive a session, share a brain, and are reachable
individually from anywhere.

Three load-bearing opinions:
1. **One agent = one sprite (substrate).** The isolation unit is a hardware-isolated microVM with a
   persistent filesystem and its own git checkout — not a thread/container/function. This is why
   parallel agents don't corrupt each other, why state persists, and why services/checkpoints/
   gateway come for free.
2. **Symmetric peers + shared-memory brain, no central orchestrator (topology).** Unlike
   controller-based frameworks (LangGraph/CrewAI/AutoGen), every node is identical, coordination is
   via shared S3 state, and there's no privileged conductor to be a single point of failure.
3. **Human-presence-as-leader, transport-agnostic interface (control).** "Leader" is wherever your
   attention is, not a role in the software; the interface is a session protocol any chat can
   render (§2.5).

**Honest cost:** hard-coupled to Sprites. It does not run on a laptop or generic cloud — the bet is
trade portability for real isolation + persistence + the platform doing the heavy lifting (URLs,
services, gateway, checkpoints). If portability ever became a goal, opinion #1 is what you'd give
up, and most of the rest would need rethinking.

---

## 0.5 As-built status (reality — authoritative)

Phases 1 and 2 are implemented and verified end-to-end on the `cl-sprites` org. This section records
what was actually built and where it diverged from the design intent below (and why). Phase 3
(insertion: take-the-wheel / needs-human) is **not** started.

**Every sprite is identical — "home" is only a hat.** No capability or config is gated to a role. Any
sprite can chat, spawn, reap, dispatch, and read/write memory; if home disappears any worker can take
the hat; if everything disappears, one new sprite booted against the brain reconstitutes the fleet.

**Secrets & credentials — connectors + the brain, not per-sprite copies.**
- **Anthropic (Claude):** via the Sprite **API Gateway connector** — the agent points Claude at the
  connector's `ANTHROPIC_BASE_URL`; the gateway authenticates by the sprite's Fly identity. No key on
  the sprite. (Matches §3.2 intent.)
- **Brain (Tigris/S3):** reached **token-free via the `s3_object_store` connector** (GET/PUT/DELETE +
  ListObjectsV2, identity-authed). No S3 keys on workers. A sprite **discovers** the connector via
  `GET /v1/gateway/list`, so the only thing it needs to reach the brain is to be in the org. (Supersedes
  §6.3's per-agent prefix-scoped S3 keys, which are now *future* hardening.)
- **GitHub & the Sprites API token:** stored **in the brain** under `fleet/config/secrets/` and
  rehydrated by every sprite on boot — `github` (for git/gh) and `sprites-api-token` (so any sprite can
  spawn/reap). This is a deliberate **deviation** from §3.2's "secrets never copied onto sprites": the
  GitHub connector proxies only the REST API, not `git` smart-HTTP transport, and won't mint a token, so
  real `git` needs a token in hand. Per the symmetry model the brain *is* the fleet's root of trust, so
  the token lives there and is loaded into process env only (git via a `GIT_CONFIG_*` credential helper;
  never written to disk). Trade-off: brain access == fleet-wide capability by design.

**Permissions.** The fleet runs Claude with **`--dangerously-skip-permissions` by default**, fleet-wide
(an autonomous fleet shouldn't stall on prompts). This **reverses** the design's "scoped
`--permission-mode`/`--settings`, not a blanket skip" (§3.1/§6.2); the scoped path is now opt-in
(`SPRITE_AGENT_DANGEROUS_SKIP=0`). `--settings` is still passed for the per-turn fleet-context hook.

**Coordination (Phase 2), as built.**
- **Dispatch:** delivered **via the brain** (`fleet/tasks/<to>/…`, append-only visible state); each
  sprite polls its own inbox and injects the task into its own local session, so it still materializes
  in that sprite's transcript (seam #2 holds). This **deviates** from §10's "session API is the delivery
  transport (no S3-inbox)" because private sprite URLs are **Fly-OAuth-gated**, so direct cross-sprite
  session calls aren't possible.
- **Durable memory:** `fleet/memory/<author>/…`, append-only; always-loaded index + on-demand bodies;
  survives reaping. Injected into context each turn via a `UserPromptSubmit` hook (with the roster +
  presence) — realizing §5's "inject live fleet state each turn."
- **Presence-routing:** each agent advertises whether a human is attached; the per-turn fleet context
  flags attended workers as "defer."
- **Fleet UI + attach:** sidebar with status/presence; **attach** opens a worker's URL in the browser
  (the human is an org member, so their browser passes the OAuth gate that blocks sprite-to-sprite HTTP).
- **Control plane:** `fleet/config/policy.json` (defaults → role → per-agent), enforced via the spawn cap
  and (when not skipping) permission mode; agents only **read** `fleet/config/*` (the human-held
  guardrail is app-layer; storage-level per-prefix enforcement is future).
- **Spawn + provisioning:** create via `POST /v1/sprites`; the worker boots this same artifact (binary
  staged in the brain, fetched via the connector) and self-registers. Cold sprites are warmed before the
  service is installed. **Auto-reap:** idle/done/dead workers are destroyed + their coordination keys
  removed; idle-reap defaults **off** (not PR-aware); home is never reaped.

**Recovery.** Because the brain is reached by identity (connector) and all capabilities + secrets
rehydrate from it, standing up a new sprite in the org brings the fleet back within minutes — no
out-of-brain bootstrap secret to hand over.

### Additions since the initial Phase 1–2 cut

- **Fleet onboarding — `scripts/launch-fleet.sh` + `sprite-agent init`.** Stand up a brand-new fleet
  from off-account: prime the brain (stage the binary + write the secrets via **direct Tigris S3
  keys**) and ignite one home sprite (`spawn.LaunchHome`); everything else reconstitutes. The binary
  now **self-discovers the Anthropic connector** on boot (sets `ANTHROPIC_BASE_URL`) when unset and
  there's no OAuth login, so a freshly-ignited home can run Claude.
- **flyctl on every sprite.** A third brain secret, `fleet/config/secrets/fly`, rehydrated on boot;
  `flyctl` is auto-installed to `~/.fly/bin` if missing and authenticated via `FLY_API_TOKEN`.
- **Durable workers (reaper reworked).** A stale heartbeat **no longer destroys** a worker — a
  *suspended* worker (finished a feature, awaiting follow-up) looks identical to a crashed one over
  the heartbeat, so we only destroy on **explicit done**, and clean a stale worker's brain entry only
  if its sprite is **actually gone** (`spawn.Exists`). Follow-up = re-attach to the still-alive worker
  (its disk + transcript survive suspend). Teardown is a presence-aware endpoint
  `POST /api/fleet/destroy {target[,force]}` (409 if a human is attached) + a per-worker **Reap**
  button in the UI. (Supersedes the older "idle/dead destroyed" line above.)
- **Keep-awake.** `internal/keepalive` holds the sprite active via the **local Tasks API**
  (`/.sprite/api.sock`) while Claude is generating / a client is attached, so autonomous tasks don't
  suspend mid-run; it releases when idle so an idle sprite still pauses.
- **Async spawn.** `/api/fleet/spawn` returns as soon as the sprite is **created**; warm/provision runs
  in the background — so the call stays under the proxy timeout (no spurious 502).
- **Frictionless shared memory.** Beyond the JSON memory API, `internal/memsync` syncs a local markdown
  folder (`$HOME/.sprite-agent/memory/`, grouped `repos/` `decisions/` `how-to/`) with the brain
  (`fleet/memory-fs/`): boot-pull (inherit), write-push (a plain file write), topic-grouped `MEMORY.md`.
- **Per-chat working dirs.** Each chat runs in `/home/sprite/chats/<session-id>` so concurrent chats
  don't clobber each other; knowledge is shared via the brain, not the filesystem. (The process
  supervisor now runs **concurrent** sessions — the claude-hub singleton was removed — so attaching a
  chat to a busy worker no longer kills its task.)
- **Chat UI.** Substantial pass: centered new-chat composer that docks once active; all controls inside
  one composer card; image **and** document attachments (`doc/docx/xls/xlsx/csv/txt/md`) fed to Claude
  (images as native blocks, text inlined, binary by saved path); voice input; syntax highlighting;
  evolving chat titles (cheap one-shot model); message + per-code-block copy buttons; recessed sidebar
  with show/hide; chat history (incl. images) on refresh; pre-paint state to avoid load flashes.

---

## 1. What we're building

A web interface to a sprite running Claude Code that can use GitHub, where you mostly talk to
one sprite ("the leader") but that sprite can **spin up other sprites** to do isolated pieces of
work instead of doing everything on its own filesystem — and you can drop into any of those
sprites to steer it when needed.

The key reframe from a single app: **the sprite is the isolation unit.** Instead of one box
juggling many Claude processes that step on each other (the reason v1 split out `claude-hub`),
each unit of work gets its own sandbox — its own filesystem, its own Claude, its own git branch.
Parallelism scales **out** across sprites, not **up** within one.

### Goals
- Web UI (mobile-friendly PWA) to talk to a sprite's Claude — reuse v1's chat surface.
- Each sprite can use GitHub (clone / branch / PR).
- One sprite can create and coordinate other sprites for parallel/isolated work.
- Mostly talk to a "leader," with the option to insert into a worker's conversation.
- Any sprite can be promoted to leader — **promotion is a human decision, never automated.**
- Sprites know about each other in a **distributed** way, not via a single hard-coded orchestrator.

---

## 2. Core principles

### 2.1 Symmetric peers — "leader" is a hat, not a build
Every sprite is **identical and fully capable**. Each one runs:
- the **session service** (so a human can chat with its Claude over the web), and
- the **fleet capability**: sprite-management tools (sprites API/MCP) + a Tigris/S3 client +
  a baked-in "you are part of a fleet" skill + live fleet state injected into context each turn.

"Leader" is just *whichever sprite you're currently talking to*. **Promotion** = you point your
attention at a worker (and optionally flip a `role=coordinator` marker in shared state). It's
near-instant because capability is uniform — nothing has to be installed or reconfigured. No
sprite is a single point of failure.

### 2.2 Two channels, kept separate
A muddle we explicitly corrected: the leader does **not** orchestrate workers through the chat UI.

| Channel | Purpose | Mechanism |
|---|---|---|
| **Human ↔ sprite** | You chat with a sprite's Claude | Session service / web UI (the `sprite-agent` app) |
| **Sprite ↔ sprite** | Leader creates/dispatches/manages workers | sprites API/MCP (create/list/destroy/exec) + Tigris/S3 |

The leader orchestrates over the **sprite↔sprite** channel directly (Claude making tool calls) —
**not** by driving the web app. The two channels meet at exactly one place: the worker's Claude
**session transcript**. The leader writes a task into it (via API/exec); the human can later
attach and read/join that same transcript. That convergence on the *transcript as a shared
artifact* is what makes "insert myself into a worker" cheap.

### 2.3 Sprites are ephemeral; the brain is durable
Workers come and go (destroyed after their PR merges). What they learn persists in shared
storage and the next sprite inherits it. Institutional memory lives in S3, **not** in any one
sprite — so no coordinator is load-bearing.

### 2.4 Don't push — write state, render by presence
No sprite ever "sends a report" or "fires a notification." A sprite **writes down what's true**
(status, result, memory) into the shared brain; every surface — the leader's context, the fleet
view, your attached session, the notification layer — is a **subscriber** that decides whether to
show a fact based on whether its audience already has it. Two consequences:

- **Coordination fact ≠ human notification.** "Worker finished" is a durable fact written silently
  for the *fleet* (the leader may need it to sequence the next step). Whether *you* are told is a
  separate question answered entirely by your presence.
- **Presence is a first-class signal.** The fleet tracks which session you're attached to (write
  it to the board: `human → attached to <id>`). The rule follows mechanically: **never surface a
  fact to someone who already has it.** Watching `build-x` finish → zero notifications, no leader
  narration of `build-x`, by construction. Presence also gates *control*: if you've taken the
  wheel on a worker, the leader **defers** on that thread. A finish event has three possible
  surfaces (badge, notification, leader message); when you're present it must produce **zero**.

### 2.5 The UI is a client, not the app — the session interface is transport-agnostic
What an agent exposes is a **session interface** (post a message, stream events, observe,
presence), *not* "a web UI." The sprite-mobile PWA is the **reference client**, not the product.
Any chat front end — PWA, Slack, Discord, Telegram, Signal, SMS, CLI, terminal — is just another
client/renderer of the same session, via a thin bridge/adapter. Consequences:
- Drive the fleet from wherever you already are; an external chat "hooks up to a nearby agent."
  "Leader = wherever the human is" (§2.3/§3) generalizes across transports — your Slack thread is
  where you are.
- **Co-presence spans transports:** start a task in the PWA, answer a *needs-human* prompt in
  Telegram, both reflect the same session (presence + transcript are shared, §2.4).
- Platform plumbing: outbound via the sprites **API Gateway Slack connector**; inbound via the
  sprite's public URL as a **webhook**. No separate server required for Slack-class transports.
- **Renderers degrade gracefully:** token streaming in the PWA; per-turn message edits in
  Slack/Telegram; voice/images per platform. Transport-agnostic at the protocol level, not
  pixel-for-pixel.
- A chat bridge is an **edge adapter, not load-bearing** — if it dies, that channel goes dark but
  the fleet keeps running (the brain is still S3).

This keeps seam #1 honest: agents are real sessions behind a clean protocol, never "a web app."

---

## 3. Architecture

```
   You ──chat──► any sprite's session service (web UI, native URL + private_access: members)
                      │
   ┌──────────────────┼──────────────────┐   every sprite is identical:
   ▼                  ▼                  ▼     • session service        (human chat)
 Sprite A ◄─────► Sprite B ◄─────► Sprite C    • sprites API/MCP         (spawn/dispatch/exec)
        \            │            /            • Tigris/S3 client        (status + inbox + memory)
         \           ▼           /             • fleet skill + injected fleet state
          ►  Tigris/S3 "brain" ◄               "leader" = whoever you're talking to
             (roster mirror, status, inboxes, memory)
```

### 3.1 The `sprite-agent` service (runs on every sprite)
A single supervised service (installed via the sprites **services** subsystem, claims an
`--http-port` so the sprite auto-starts on incoming request). Responsibilities:
- Serve the embedded PWA (`go:embed`) + REST (sessions, uploads, memories, fleet list).
- WebSocket hub: fan one Claude session's stream out to N connected clients (web + terminal
  co-presence preserved from v1).
- **Drive the `claude` CLI over stream-json** (claude-hub's proven approach — there is no Go Agent
  SDK, and we don't need one per §0): deterministic `--session-id`, `--include-partial-messages`
  for token streaming, scoped `--permission-mode`/`--settings` instead of blanket
  `--dangerously-skip-permissions`, `--mcp-config` (file) to wire in the sprite's `/mcp` + the
  sprites remote MCP. Hooks via `settings.json`.
  **(As built: reversed — the fleet runs `--dangerously-skip-permissions` by default for autonomy;
  `--settings` is still passed for the per-turn fleet-context hook. See §0.5.)**
- Supervise that process (singleton, grace period, crash handling) — **lifted from claude-hub.**
- Watch `~/.claude/projects/*.jsonl` for terminal co-presence (shared transcript = source of truth).
- Light metadata store (embedded SQLite) for sessions/titles/memories.
- Fleet client: read/write the Tigris/S3 brain; expose fleet state to Claude's context; manage
  sprites via **`sprites-go`** (more complete than the JS SDK — has `private_access`, etc.).

### 3.2 Stack decisions (settled)
- **Backend:** single **Go** binary per sprite, built on **claude-hub's supervision kernel**, with
  the PWA embedded via `go:embed`. Drives the `claude` CLI directly (stream-json) rather than an
  Agent SDK — per §0 we build supervision/plumbing, not agent logic, so the SDK's value doesn't
  apply; claude-hub already proves the CLI path. Single static binary deploys cleanly as a sprite
  service. *(Revises the earlier "single Bun/TS service" call; claude-hub moves from drop → kernel.)*
- **Frontend (the first client):** Vite + lightweight framework (Svelte/Preact) + `vite-plugin-pwa`;
  lift v1's chat/streaming/image/voice UI. JS/TS — but it's a **client** of the transport-agnostic
  session protocol (§2.5), not the same process as the backend; the languages needn't match, which
  sharpens seam #1.
- **Access control:** native sprite URL with `private_access: members` — drops the v1
  Tailscale tower (tailscaled + tailnet-gate).
  **(As built: spawned workers default to `private_access: admins`; a human attaches from the browser,
  which passes the Fly-OAuth gate. That same gate blocks sprite-to-sprite HTTP, which is why dispatch
  goes via the brain — §0.5/§10.)**
- **Secrets:** GitHub + Anthropic via the sprites **API Gateway** connectors — credentials are
  injected by the platform and **never copied onto sprites** (`ANTHROPIC_BASE_URL` → gateway).
  **(As built: true for Anthropic + the brain/Tigris, both via connectors. NOT achievable for GitHub —
  the connector proxies only the REST API, not `git` transport — so the GitHub token (and the Sprites
  API token) are stored in the brain and rehydrated by every sprite; loaded into process env only,
  never written to disk. See §0.5.)**
- **Discovery:** sprites-api **labels** (authoritative membership) + Tigris/S3 (live coordination).

---

## 4. The fleet "brain" (Tigris/S3)

S3 is the shared brain/memory for the fleet. Structure it as **two layers** so it scales:

### Layer 1 — Coordination state (fast, ephemeral)
Who's here, what they're doing, peer inboxes. High-churn; heartbeats carry a timestamp/TTL so a
dead sprite stops looking alive.

### Layer 2 — Durable memory / knowledge (append-only, grows)
Facts, decisions, learnings, artifacts. This is v1's **session-memories** feature promoted from
local markdown to shared fleet memory.

### Scaling rule
**Never pour the whole brain into context.** Keep a small **always-loaded roster/index** + a
**deeper memory retrieved on demand** — the same shape as Claude Code's own `MEMORY.md` index +
on-demand fact files. "More capable with more sprites" comes from a *growing retrievable memory*,
not from cramming everything into every head.

### New-sprite awareness
A new sprite becomes fleet-aware by **reading the board on boot** (and injecting it into context),
so its very first turn knows the fleet. For immediacy, the **spawner announces** the new sprite
(writes its entry, pokes peers' inboxes) rather than waiting for the next poll.

### 4.1 Avoiding concurrent-write corruption of the index
**Principle:** never have a single index file that workers race to overwrite (that's the classic
lost-update: last writer clobbers everyone). Design the contention away; use locks only as a last
resort.

1. **Derive the index, don't store it (default).** Each sprite writes **only its own keys**
   (`fleet/<id>/status.json`, `fleet/<id>/heartbeat.json`); the index = `ListObjects("fleet/")`
   at read time. Two writers never touch the same key, so collisions are impossible. Use for the
   **roster/status** layer.
2. **Append-only for shared memory.** Many contributors → unique, collision-proof keys:
   `fleet/memory/<sprite-id>/<timestamp>-<uuid>.json`. Simultaneous writes are just different
   objects. Index = `LIST` + merge, or periodic compaction. (Use sprite-id + uuid, **not** a bare
   timestamp, so near-simultaneous writes can't collide.)
3. **Materialized index, if you want one:** either a **single compactor** (one writer rebuilds
   `index.json` from per-writer objects; make the role a *lease* so it survives the compactor
   dying), or **conditional writes / compare-and-swap** — read index + ETag, modify, `PUT` with
   `If-Match: <etag>`; on **412** re-read and retry with backoff.
4. **Locks/leases only when truly needed:** create-only write (`PUT … If-None-Match: *`); exactly
   one writer wins. Store `{owner, expires_at}` and honor a TTL so a crashed holder self-expires.
   Use for "who's the compactor / coordinator," not routine writes.

Tigris's strong consistency + conditional-request support make patterns 3–4 clean rather than
best-effort (confirm exact `If-Match`/`If-None-Match` header support at build time).

---

## 4.2 Bootstrap, recovery, and changing home

**The fleet is reconstructible from the brain.** Hooking a new agent into the brain is the *same*
operation whether it's the 1st agent or the 50th — only *who triggers it* differs. On boot every
agent:
1. Reads a **bootstrap pointer** (where the brain is + how to auth), then reads the brain.
2. **Registers** itself (writes `fleet/<id>/status.json`).
3. **Rehydrates**: durable memory, fleet config/policy, open task records, external-chat bindings.
4. **Reconciles**: peers with expired heartbeats = dead → orphaned tasks reassigned/surfaced;
   bindings pointing at dead agents → rebound; claim `role=home` if designated.

The **only** thing that can't live in the brain is the pointer *to* the brain (S3 location +
creds) — chicken-and-egg, so it's **bootstrap input** passed at spawn time (sprite config /
gateway). Everything else rehydrates.

**Total loss (all agents destroyed, brain survives)** recovers by creating **one** sprite with the
bootstrap pointer — it reads the brain and the fleet is alive. Who issues that first create when no
agent exists:
- **Default:** a human one-liner (`sprite-agent bootstrap --brain s3://…`). Genesis is deliberate.
- **Optional:** a tiny **seed watchdog** (cron / minimal Fly app) that guarantees ≥1 home exists;
  acts only when the fleet hits zero. Not a coordinator. Add later for auto-heal.
In practice you rarely hit zero because **home is pinned (never auto-reaped)**.

**Changing home** is cheap because home holds no unique state — it's a normal agent plus three
things: a **role lease** (`role=home`), a **lifecycle pin** (don't reap), and the **stable
entry-URL binding**. Promotion (a **human** action) moves those three to another agent: new home
claims the lease + pin + URL (+ home-hosted duties like the chat bridge), reads what it needs from
the brain, old home drains and becomes reapable. Clients/bridges resolve "home" via `role=home`
(or a thin redirector), so they follow automatically.

## 5. Making a sprite actually *use* the fleet (know it can spawn)

The v1 pain — "the leader doesn't know it can/should create sprites" — is an **affordance
problem, not an architecture one.** A sprite won't spawn workers if the tools aren't connected,
nothing tells it that's an option, or it can't see its peers. Fix all three, on **every** sprite:
- **Connect the tools** — sprites MCP wired in via `--mcp-config` so "create a sprite" is a real,
  visible tool call.
- **Bake in a fleet skill / CLAUDE.md** — "You're a peer in a fleet. For parallel or isolated
  work, spin up a worker sprite instead of doing it here. Here's how." (sprite-env already ships
  base-env skills to every sprite, e.g. `sprite-api-gateway` — same mechanism.)
- **Inject live fleet state into context** each turn — "current fleet: A (task=auth, busy),
  B (idle)…" from the S3 board — so the sprite always knows who exists and what they're doing.

---

## 6. Human-in-the-loop: talk to the leader, insert into a worker

Because every worker is a real chat session (not a headless job), "insert into a worker" is just
the co-presence feature pointed at a different sprite — the leader and you are both clients of the
worker's one transcript.

- **Observe by default; take the wheel on request.** Attaching to a worker is read-only (watch the
  live stream). A "Take over" action makes you an active participant and **pauses the leader's
  auto-polling** of that worker; releasing resumes it (leader re-reads the transcript to catch up).
  One driver at a time.
- **Workers raise a hand.** A worker that hits a permission prompt / ambiguous decision emits a
  *needs-human* signal; the fleet view badges it; you tap in. Insertion is usually pulled by the
  work, not pushed by you.
- **Leader stays aware.** Your message lands in the worker's transcript, so the leader folds your
  intervention into its plan on its next sync.

---

## 6.1 Checking on progress (observability)

Driven by §2.4 — **nobody pushes reports; the worker writes state, you write presence, and each
surface renders only what its audience doesn't already have.** Three surfaces, increasing detail;
you never have to route through the leader to check, and you never get pinged about what you're
already watching:

1. **The leader chat (default).** The leader, mid-orchestration, *reads* the workers' state and
   reflects relevant changes into the conversation you're in — but only for workers you're **not**
   currently attached to. Best for "I mostly talk to the leader."
2. **The fleet view (glance).** A sidebar listing every sprite with live status, read **straight
   from the S3 board** — works even if the leader is idle/suspended. Example row:
   `build-x · feat/login-oauth · ● working · "running tests (3/12)" · 8s ago`.
3. **Attach to the worker (ground truth).** Tap a worker → your UI attaches to its session service
   and streams the live transcript (read-only by default; "take the wheel" to steer — at which
   point the leader defers on that worker per §2.4).

**What the worker writes (silently, not as a push):**
- **Status** — its phase to `fleet/<id>/status.json` each turn, cleanest via a Claude Code **hook**
  (`PostToolUse`/`Stop`) or the session service watching the transcript. Feeds the fleet view.
- **Transcript** — its `.jsonl`, live ground truth available the moment you attach.
- **Completion** — on finish (PR opened / `Stop`): `status: done` + a result object. A durable
  fact for the fleet; whether it reaches *you* is decided by presence, not by the worker.

**Notifications are the absent-attention case only.** The PWA can push — but only for a worker you
are *not* watching, with the app backgrounded, when something needs you (a *needs-human* hand:
permission prompt, ambiguous decision). If you're already there, nothing fires.

The worker won't suspend mid-build (services keep it alive while running); if it idles, the fleet
view shows last-seen and tapping in resumes it (http-port auto-start).

## 6.2 Capabilities & the control plane

Powers are a **layered capability model**, not one-off toggles. Capabilities: `can-merge`,
`can-spawn` (+ how many / which labels), `can-push-main`, `can-spend` (budget), `can-access-secret-X`,
`can-deploy`, etc.

- **Two layers with inheritance:** a **fleet-wide default** + **per-agent overrides**. Effective
  powers = `merge(fleet_default, agent_override)`. e.g. `merge = human` fleet-wide, overridden to
  `merge = auto-on-green` for one trusted agent.
- **Lives in the brain** (`fleet/config/policy.json` + per-agent records) — durable, visible,
  auditable, re-read on boot and on change. That config doc **is** the control plane; surface it in
  the web UI.

Not hand-wavy — it maps onto **real enforcement primitives**, so powers are enforced, not merely
prompted:

| Power | Enforced by |
|---|---|
| spawn / how many / which labels | **sprites-api token restrictions** (`max_sprites_total`, `name_prefix`, `label`) — the scoped token an agent is handed *is* its spawn power |
| which secrets / external APIs | **API Gateway connector access policies** (keyed on sprite **labels**) |
| which tools / shell commands | **Claude Code `--permission-mode` / `--settings` / `--allowedTools`** per agent |
| budget / spend | gateway spend cap (fleet) + per-agent sub-budget |

When home (or a human) spawns an agent, it materializes that agent's capability set into: a scoped
token, labels, a Claude settings/permission bundle, and which MCP tools get wired — recorded in the
brain.

**Critical guardrail:** `can-modify-policy` is **human-held by default.** Agents must not grant
themselves or each other more power (escalate merge/spawn/spend). Capability changes — like home
promotion — are control-plane (human) actions. This keeps "promotion is a human decision" true at
the *permission* level, not just the org chart.

**Policy schema (three-tier, most-specific wins):**
```jsonc
// fleet/config/policy.json   — HUMAN / control-plane writable ONLY (agents have no write cred here)
{
  "version": 1,
  "defaults": {                          // applies to every agent
    "spawn":  { "allowed": true, "max_total": 10, "name_prefix": "wk-" },
    "merge":  "human",                   // "human" | "auto-on-green" | "auto"
    "push_main": false,
    "spend":  { "daily_usd_cap": 50 },
    "secrets": ["github", "anthropic"],  // which gateway connectors
    "tools":  { "permission_mode": "acceptEdits", "deny": ["Bash(rm -rf *)"] },
    "modify_policy": false               // the guardrail — false everywhere by default
  },
  "roles": { "home": { "spawn": { "max_total": 50 } }, "worker": {} }
}
// fleet/<id>/policy.json     — per-agent override (written by the spawner)
{ "merge": "auto-on-green", "spend": { "daily_usd_cap": 10 } }
```
`effective = merge(defaults, roles[role], agent_override)`. Because `fleet/config/*` is writable
only by the control plane, the `modify_policy` guardrail is enforced at the **storage-permission
layer**, not by convention — an agent physically has no write credential for the policy prefix.

## 6.3 Identity & trust

**Lean on platform identity; don't build a PKI. The security model phases in with the topology.**

**Fleet identity (agent ↔ agent) — matters at Phase 2 (multi-agent):**
- **Brain integrity:** each agent gets **S3 credentials scoped to its own prefix** (`fleet/<id>/*`
  write, rest read). "Agent B wrote this" is then *guaranteed* — only B can write B's prefix. Same
  mechanism enforces the policy guardrail (no agent has write to `fleet/config/*`).
- **Peer calls:** intra-fleet HTTP is gated to the org by `private_access: members`; a per-agent
  token tells the callee *which* agent is calling. The spawner provisions scoped creds + token at
  spawn and records identity in the brain.
- **Blast radius:** a compromised worker can corrupt only its own prefix — not fleet policy, not
  peers' status, and it can't impersonate another agent.

**Human identity across transports — matters at Phase 3 (multi-transport/human):**
- A **human identity registry in the brain** maps *verified* transport identities
  (`org-member:cl@…`, `slack:U123`, `telegram:456`) → a canonical human + an **authority tier**:
  **observe** (watch) · **drive** (chat/steer) · **control-plane** (modify policy, promote home,
  grant powers). The bridge/session service resolves every inbound message to a tier before
  honoring it; control-plane actions require the top tier + a strong auth path.
- **Phase 1 is trivial:** one human (you), PWA only, org-member auth via `private_access`, full
  authority — no registry needed. The registry earns its keep only when multiple transports/humans
  appear.

## 7. Two seams to protect from day one
Cheap now, painful to retrofit later. Honor these even while building leader-only:
1. **Workers are real chat sessions, not dumb exec targets.** Every sprite runs the same session
   service and is addressable as a chat.
2. **The leader messages workers over the same session API a human uses.** Then "human inserts"
   reuses that exact path instead of a parallel one.

---

## 8. Build phasing

There is **no "leader" build target** — every phase builds and refines the *same one symmetric
agent* (§2.1). "Leader" is only ever a hat (wherever the human is). The dividing line between phases
is **not "can it spawn"** (spawning is one sprites-API/MCP call, available day one) but **"do
spawned agents coordinate."**

- **Phase 1 — The sprite-agent. ✅ BUILT.** The full symmetric artifact: Go session service (claude-hub
  kernel, CLI-driven Claude) + embedded web UI + GitHub + **spawn capability** (create + provision a
  worker that boots this artifact) + a **minimal brain** (self-registration, roster). Establishes the
  two seams. Not a "leader."
- **Phase 2 — Coordination. ✅ BUILT.** dispatch (via the brain — §0.5), durable shared memory,
  presence-routing, the fleet UI (sidebar, attach-to-worker), and the capability/policy control plane.
  Plus, beyond the original line: auto-reap, token-free brain via the s3 connector, and operational
  secrets in the brain (full symmetry).
- **Phase 3 — Insertion. ⛔ NOT STARTED.** Promote the worker view from observer to participant:
  take-the-wheel handoff + needs-human signals.

Each phase is independently useful; nothing in Phase 1 has to be undone to reach Phase 3.

---

## 9. Reused from v1 vs. dropped

**Reuse:** the web chat UI (streaming, image/voice, PWA) as the **first client**; session titles,
session-memories (promoted to shared fleet memory); the transcript-as-source-of-truth + co-presence
idea; **claude-hub's Go supervision kernel** (stream-json parsing, singleton, grace period, `.jsonl`
watcher, WS hub) as the **foundation of the Go backend** — not dropped.

**Drop / replace:**
- `claude-hub` *as a separate relay service* → its kernel is absorbed into the single Go
  `sprite-agent` backend; per-sprite isolation removes the multi-process juggling it was built for.
- Tailscale tower (tailscaled + tailnet-gate) → native `private_access: members`.
- `duration=3s` + `session-keepalive.sh` hacks → services subsystem (keep-alive while running,
  `--http-port` auto-start, `POST /services/{name}/restart`).
- UUID rename dance (`update-id`, file renames) → deterministic `--session-id`.
- Secrets copied onto every sprite → API Gateway connectors.
- S3 "Sprite Network" *as an ad-hoc discovery hack* → labels for membership + a deliberate,
  structured S3 brain for coordination/memory.

---

## 10. Decisions & open questions

### Decided
- **Task dispatch:** deliver via the **target agent's session API** (same path a human/bridge uses
  — seam #2); the session service *is* the durable queue (no separate S3-inbox transport). Mirror
  the assignment into S3 as **visible fleet state**, not as delivery. Materializes in the transcript.
  **(As built: delivery is via the brain — `fleet/tasks/<to>/…`, append-only — because private sprite
  URLs are Fly-OAuth-gated, so a direct cross-sprite session call isn't possible. The target polls its
  inbox and injects the task into its own local session, so it still materializes in that transcript
  (seam #2 preserved); only the delivery hop differs from the "no S3-inbox" intent. §0.5.)**
- **Home base:** one **durable home agent per workspace** — symmetric code, but pinned + stable URL
  + default landing. Changeable via human promotion (§4.2). Not a privileged coordinator.
- **GitHub:** **one worker = one branch/PR**, repo cloned per worker; coordinate via PRs, never a
  shared working tree; token via gateway connector. **Merge:** fleet default `merge = human`,
  **grantable per-agent** via the capability model (§6.2).
- **External-chat routing:** front door resolves a **new** thread (existing agent or spawn), writes
  `bindings/<transport>/<thread-id> → <agent-id>/<session-id>` to S3; established threads resolve
  the binding. Front-door + binding, not either/or.
- **Chat bridge:** thin **shared per-transport bridge** (forced by single-webhook ingress), hosted
  on the home agent; routes by the S3 binding; non-load-bearing.
- **Cold-start / recovery:** fleet reconstructible from the brain; bootstrap pointer is the only
  out-of-brain input; genesis via human CLI (optional seed watchdog) (§4.2).
- **Capabilities:** layered fleet-default + per-agent override in the brain; enforced by scoped
  tokens / gateway access policies / Claude permission-mode; `can-modify-policy` human-held (§6.2).

- **JS SDK:** verified `@fly/sprites` @ `216e4f1` (Apr 2) covers the v2 core (create+labels, exec,
  fs, proxy, checkpoints, services, control multiplexing). Only gap is **`private_access`** → set
  via raw `PUT /v1/sprites/:name`. Re-check npm for a newer published version at Phase 1.
- **Seed watchdog:** **deferred.** Genesis = human `bootstrap` CLI; home is pinned. Revisit when the
  fleet must self-recover unattended.
- **Policy schema:** drafted three-tier (defaults → role → per-agent) in §6.2; finalize field-by-field
  in Phase 2.
- **Identity & trust:** resolved in §6.3 — per-agent prefix-scoped S3 creds + platform auth (fleet,
  Phase 2); tiered human-identity registry (transports, Phase 3); Phase 1 rides on org auth.

### Still genuinely open
- Field-level finalization of the policy schema and the human-authority tiers — deferred to the
  phase that introduces them (2 and 3 respectively); nothing blocking Phase 1.

---

*Background: platform capabilities that changed since v1 froze (2026-03-11) are summarized in the
project memory (`reference_sprites_platform_changes.md`): API Gateway/connectors, remote MCP
(create/destroy sprite as tools), labels, `private_access`, services subsystem, checkpoints, and
Claude Code 2.1.185 flags (`--session-id`, `--include-partial-messages`, `--permission-mode`,
`--mcp-config`).*
