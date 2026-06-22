# sprite-agent — Phase 1 Build Brief (autonomous run)

> Execution brief for an autonomous Claude Code agent. The **design + rationale** live in the
> companion design doc (`sprite-agent-v2-plan.md`, delivered alongside this — referred to below as
> **DESIGN**). This file is *what to build and how to know you're done.* Read DESIGN first, then
> execute this.

## Mission
Build **Phase 1** of `sprite-agent` (DESIGN §8): the single symmetric Go agent that runs on a
sprite — a Go session service (built on claude-hub's kernel) that serves a web chat UI, drives
Claude Code, can use GitHub, can spawn another instance of itself, and registers into a minimal
shared roster. Ship it to a GitHub repo with a PR.

## How to work (autonomy rules — read carefully)
You are running **unattended**. The human is gone and **cannot answer questions.**
1. **Never block on a question.** Where DESIGN leaves something ambiguous, pick the **documented
   default**, write one line in `DECISIONS.md` (`<decision> — <why>`), and continue.
2. **Commit and push frequently** (after every green milestone, and at least every ~15 min of work).
   Partial progress must survive you crashing or being suspended. Work on a branch; never force-push.
3. **Build the smoke test early and keep it green.** Don't write the whole thing then test — get the
   thinnest end-to-end slice working, prove it, then expand.
4. **If a credential/input is missing** (see Inputs), do **not** halt: build that capability behind a
   clean interface, unit-test the logic, **stub the live call**, note it in `BUILD_REPORT.md`, and
   move on. Token-independent milestones (M1–M3) must still fully complete.
5. **Stay in scope.** Build Phase 1 only. See Non-goals — do not start them.
6. **Prefer boring, working code.** Match claude-hub's style where you lift from it.

## Hard scope
**In scope (Phase 1):** M1–M4 below.
**Non-goals (do NOT build — these are Phase 2/3):** task dispatch/assignment between agents;
durable shared *memory* (only the minimal roster is in scope); presence-routing; the fleet UI
(sidebar/attach-to-worker); the capability/policy control plane; chat bridges (Slack/Telegram);
insertion / take-the-wheel / needs-human. If you finish M1–M4 with time left, **harden and test**,
don't start Phase 2.

## Inputs & environment
Check each at startup; record what's present/absent in `BUILD_REPORT.md`.
- **Claude Code token** — required (M2+). The `claude` CLI must be installed and authenticated.
- **GitHub token** (`gh`) — required (M1, M3). Needs repo-create + push scope.
- **Go toolchain** — required. If absent, install it.
- **claude-hub source** — clone `https://github.com/clouvet/claude-hub` to lift the kernel.
- **Sprites API token** — needed for **M4 live spawn**. Created under the dashboard's **Access
  Tokens** tab (NOT Connectors — it drives Sprites itself, it's not an external service). Provide it
  to the agent as an env var (`SPRITE_API_TOKEN`, format `org-slug/org-id/token-id/token-value`).
  **Scope it (restricted token):** set `name_prefix` (e.g. `wk-`) + `max_sprites_total` cap —
  least privilege for an unattended agent (DESIGN §6.2). If absent → stub M4's spawn, still build it.
- **Tigris/S3 creds + bucket** — needed for **M4 brain**. Provision an **editor** key (NOT admin)
  **scoped to a dedicated brain bucket** — least privilege for an unattended YOLO agent (admin could
  delete buckets / mint keys). Per-prefix scoping + `fleet/config/*` write-protection is a Phase 2
  refinement (DESIGN §6.3), not enforced with a single bucket-scoped key. If absent → stub the brain
  client, still build the interface and unit-test the roster logic.
- **Target repo** — `https://github.com/clouvet/sprite-agent` **already exists and is empty** (brand
  new). Push into it; do NOT create it.

## Milestones (ordered; each ends in a commit + push)

### M1 — Repo bootstrap
- Clone the **existing empty** repo `https://github.com/clouvet/sprite-agent` (don't create it).
- Go module, sane layout (`cmd/`, `internal/`, `web/`), `.gitignore`, `README`.
- Commit + push `main`; open a working branch.
- **Acceptance:** `go build ./...` passes; initial commit pushed to `clouvet/sprite-agent`.

### M2 — Session service + web UI + Claude chat loop  *(the riskiest slice — do it first after M1)*
- Lift from claude-hub: stream-json parsing (`pkg/claude`), process supervision
  (`internal/process`), `.jsonl` watcher (`internal/watcher`), WS hub fan-out (`internal/hub`).
- Drive `claude` per DESIGN §3.1: `--print --output-format stream-json --input-format stream-json
  --include-partial-messages`, deterministic `--session-id`, scoped `--permission-mode`/`--settings`
  (not blanket skip-permissions). Serve the web UI embedded via `go:embed` (lift/trim v1's chat UI).
- **Acceptance:** start the service; open the web UI; send a message; see Claude's reply **stream
  token-by-token**; a `claude --resume <id>` terminal session and the web UI show the **same
  conversation** (co-presence). A scripted smoke test exercises send→stream→done.

### M3 — GitHub capability
- Ensure the agent's Claude can clone/branch/commit/PR using the gh token (gateway-injected per
  DESIGN, or env for Phase 1 — record which in DECISIONS).
- **Acceptance:** via the web UI, instruct the agent to make a trivial change in a scratch repo and
  open a PR; the PR appears on GitHub.

### M4 — Spawn capability + minimal brain  *(gated on Sprites token + S3 creds)*
- **Spawn:** the agent can create another sprite running this same artifact (sprites API/MCP or
  `sprites-go`), handing it the bootstrap pointer (DESIGN §4.2).
- **Minimal brain:** bootstrap pointer → self-registration → roster. On boot, read brain location
  from input, write `fleet/<id>/status.json`, and list the roster. Per-writer keys only; roster =
  list-the-prefix (DESIGN §4.1). No dispatch, no durable memory (those are Phase 2).
- **Acceptance (creds present):** from agent A, spawn agent B; B boots, registers; both appear in
  the roster read by either. **(creds absent):** interface built, roster/merge logic unit-tested,
  live calls stubbed, gap documented.

## Definition of done
- M1–M3 acceptance criteria **pass**, committed and pushed, with an open **PR**.
- M4 done to the extent creds allow; otherwise built-behind-interface + stubbed + documented.
- The two seams hold (DESIGN §7): workers are real sessions; the agent talks to a spawned agent over
  the same session API a human uses.
- `DECISIONS.md` and `BUILD_REPORT.md` written. Repo builds clean from a fresh clone.

## Reporting (write `BUILD_REPORT.md` before finishing)
- What's built and **verified** (with the command/observation that proves it).
- What's **stubbed/untested** and why (missing creds, etc.) and exactly how to finish it.
- Assumptions made (mirror of `DECISIONS.md`).
- How to run it from scratch (commands).
- Open PR link.
