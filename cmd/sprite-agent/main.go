// Command sprite-agent is the single symmetric agent that runs on a sprite:
// a session service (web chat UI driving Claude Code), with GitHub, spawn, and
// minimal-fleet-brain capabilities. See README.md and docs/sprite-agent-V2-plan.md.
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/clouvet/sprite-agent/internal/config"
	"github.com/clouvet/sprite-agent/internal/fleet"
	"github.com/clouvet/sprite-agent/internal/gateway"
	"github.com/clouvet/sprite-agent/internal/hub"
	"github.com/clouvet/sprite-agent/internal/keepalive"
	"github.com/clouvet/sprite-agent/internal/memsync"
	"github.com/clouvet/sprite-agent/internal/reaper"
	"github.com/clouvet/sprite-agent/internal/server"
	"github.com/clouvet/sprite-agent/internal/spawn"
)

// setupGitHubAuth wires git + gh to use the brain-sourced GitHub token. It does
// NOT touch ~/.gitconfig (an earlier version did, and clobbered the host's own
// gh credential helper). Everything goes in the process env, inherited by the
// claude subprocess → git/gh:
//   - GH_TOKEN / GITHUB_TOKEN for `gh`.
//   - a github.com-scoped git credential helper injected via GIT_CONFIG_* (layers
//     on top of existing config, doesn't overwrite it). The helper is conditional
//     — it emits nothing when GH_TOKEN is unset, so git falls through to any other
//     helper instead of failing with an empty password.
//
// The token lives only in process env (sourced from the brain), never on disk.
func setupGitHubAuth(token string) {
	os.Setenv("GH_TOKEN", token)
	os.Setenv("GITHUB_TOKEN", token)
	helper := `!f() { test -n "$GH_TOKEN" && printf 'username=x-access-token\npassword=%s\n' "$GH_TOKEN"; }; f`
	os.Setenv("GIT_CONFIG_COUNT", "1")
	os.Setenv("GIT_CONFIG_KEY_0", "credential.https://github.com.helper")
	os.Setenv("GIT_CONFIG_VALUE_0", helper)
}

// setupFlyAuth wires flyctl to the brain-sourced Fly token and ensures the CLI is
// available, so any sprite can run `fly`/`flyctl` non-interactively. The token lives
// only in process env (FLY_API_TOKEN), inherited by the claude subprocess. flyctl is
// installed to ~/.fly/bin in the background if missing (workers boot from the bare
// artifact); ~/.fly/bin is put on PATH immediately so it's usable once present.
func setupFlyAuth(token string) {
	os.Setenv("FLY_API_TOKEN", token)
	os.Setenv("FLY_ACCESS_TOKEN", token)
	home := os.Getenv("HOME")
	if home == "" {
		home = "/home/sprite"
	}
	flyBin := filepath.Join(home, ".fly", "bin")
	if p := os.Getenv("PATH"); !strings.Contains(p, flyBin) {
		os.Setenv("PATH", flyBin+":"+p)
	}
	if _, err := os.Stat(filepath.Join(flyBin, "flyctl")); err != nil {
		go func() {
			cmd := exec.Command("sh", "-c", "curl -fsSL https://fly.io/install.sh | sh")
			cmd.Env = os.Environ()
			if out, err := cmd.CombinedOutput(); err != nil {
				log.Printf("fly: install failed: %v (%s)", err, strings.TrimSpace(string(out)))
			} else {
				log.Printf("fly: flyctl installed to %s", flyBin)
			}
		}()
	}
}

// hasClaudeOAuth reports whether this sprite has a Claude OAuth login, so we don't
// override it with the Anthropic connector.
func hasClaudeOAuth() bool {
	home := os.Getenv("HOME")
	if home == "" {
		home = "/home/sprite"
	}
	_, err := os.Stat(filepath.Join(home, ".claude", ".credentials.json"))
	return err == nil
}

// fleetMemoryDir is the local markdown fleet-memory directory (synced to the brain).
func fleetMemoryDir() string {
	home := os.Getenv("HOME")
	if home == "" {
		home = "/home/sprite"
	}
	return filepath.Join(home, ".sprite-agent", "memory")
}

// taskSnippet makes a short single-line label from a dispatched task.
func taskSnippet(task string) string {
	s := strings.TrimSpace(strings.ReplaceAll(task, "\n", " "))
	if len(s) > 50 {
		s = s[:50] + "…"
	}
	return s
}

// fleetAffordance is the baked-in "you are a fleet peer" system prompt (DESIGN
// §5): a sprite won't spawn workers if nothing tells it that's an option. It is
// appended to Claude's system prompt so the agent knows it can spin up isolated
// worker sprites instead of doing all work on its own filesystem.
func fleetAffordance(cfg config.Config, spawnAvailable bool) string {
	b := &strings.Builder{}
	fmt.Fprintf(b, "You are sprite-agent %q, one peer in a symmetric fleet of identical agents — "+
		"not a standalone assistant. For parallel or isolated work, prefer spinning up a worker "+
		"sprite (its own microVM, filesystem, and git checkout) over doing everything here. ", cfg.AgentID)
	if cfg.Brain.Enabled() {
		b.WriteString("The live fleet roster is available at GET /api/fleet on this service. " +
			"Keep your own status current so peers can see how you're doing without interrupting you: " +
			"POST /api/fleet/phase {\"phase\":\"<one line: what you're doing now, or 'done: <result>'>\"}. " +
			"This is especially important while working a dispatched task — update it at each milestone and " +
			"when you finish, so when a human asks another agent \"how is <you> progressing?\" they get a real " +
			"answer from your latest phase (peers can't read your transcript; this note is the channel). ")
	}
	if spawnAvailable {
		b.WriteString("To create a worker, POST /api/fleet/spawn (or use the sprites API); the new " +
			"sprite boots this same artifact and registers into the shared brain automatically. " +
			"DELEGATING WORK follows ONE fixed protocol — use it exactly, never improvise a way to get " +
			"results: " +
			"(1) ASSIGN — POST /api/fleet/dispatch {\"target\":\"<id>\",\"task\":\"…\"}; the response has a " +
			"\"session_id\" — REMEMBER it, that's where the worker does the work in its own session. " +
			"(2) PROGRESS — GET /api/fleet/status?target=<id> returns the peer's latest phase + LIVE state " +
			"(generating now? sessions? awake?), so you can answer \"how is <worker> doing?\" without interrupting it. " +
			"(3) RESULT — GET /api/fleet/result?target=<id>&session=<session_id> returns the worker's final " +
			"answer (\"ready\":true once done); poll status until done, then PULL the result and relay it to the human " +
			"in this chat. NEVER ask a worker to dispatch/send/curl its result back to you — a result delivered " +
			"through dispatch is misread as a NEW task and executed, spawning runaway sessions. You PULL; the worker never pushes. " +
			"To send an informational FYI rather than work, add \"kind\":\"note\" to a dispatch — the recipient is told not to execute it. " +
			"To tear a worker down, POST /api/fleet/destroy {\"target\":\"<id>\"} — this destroys its VM " +
			"and removes its brain entry. It refuses with HTTP 409 if a human is attached to that worker " +
			"(the roster's present/👤 = the DEFER signal, §2.4); only after the human confirms, re-POST " +
			"with {\"target\":\"<id>\",\"force\":true}. Do NOT hand-roll teardown via the host socket or " +
			"guess routes — this endpoint is the mechanism. ")
	} else {
		b.WriteString("Spawning is not yet wired on this sprite (no sprites API token), so for now " +
			"do the work here and note when a worker sprite would have been the better tool. ")
	}
	b.WriteString("You have GitHub access (git + gh are authenticated) — clone repos, branch, commit, " +
		"and open PRs directly. ")
	mem := fleetMemoryDir()
	own := filepath.Join(mem, cfg.AgentID)
	fmt.Fprintf(b, "Shared fleet memory is a local folder, %s — treat it exactly like your own memory. "+
		"At the START of a task read %s/MEMORY.md (entries grouped by topic across the whole fleet) so you "+
		"inherit what's known; when working on a repo, read everything under the 'repos' group for it. "+
		"Record durable learnings as you go and before you finish — write concise markdown under %s/ using "+
		"this light structure: repos/<repo-name>.md (what a repo is like: architecture, conventions, gotchas, "+
		"how a feature was built), decisions/<slug>.md (a choice + why), how-to/<slug>.md (a reusable "+
		"procedure); anything else can be a top-level .md. It syncs fleet-wide automatically, so the next "+
		"worker starts already knowing. Make writing memory as second-nature as committing code.",
		mem, mem, own)
	return b.String()
}

func main() {
	// `sprite-agent init` stands up a brand-new fleet (prime the brain + ignite home)
	// rather than running the agent. See launch-fleet.sh.
	if len(os.Args) > 1 && os.Args[1] == "init" {
		runInit(os.Args[2:])
		return
	}

	cfg := config.FromEnv()
	log.Printf("sprite-agent starting: id=%s addr=%s workdir=%s projects=%s",
		cfg.AgentID, cfg.Addr, cfg.WorkDir, cfg.ClaudeProjectsDir)

	// Scope the agent's tools/shell via a settings allow-list (DESIGN §6.2)
	// rather than a blanket skip — materialize the embedded default unless the
	// operator pointed SPRITE_AGENT_SETTINGS elsewhere. This is what lets the
	// agent's Claude run git/gh non-interactively (M3) while staying scoped.
	if path, err := config.ResolveSettingsPath(cfg.WorkDir, cfg.SettingsPath); err != nil {
		log.Printf("settings: failed to resolve (%v); continuing without --settings", err)
	} else {
		cfg.SettingsPath = path
		log.Printf("settings: using %s", path)
	}

	// Prefer the token-free brain: if no explicit brain gateway is set, discover
	// the org's s3_object_store connector and route the brain through it (authed by
	// this sprite's identity — no S3 keys needed). This is what lets a fresh sprite
	// reach the brain with nothing but its identity, and keeps every sprite
	// symmetric. Direct S3 keys remain a fallback when no connector is available.
	if cfg.Brain.GatewayURL == "" {
		dctx, dcancel := context.WithTimeout(context.Background(), 15*time.Second)
		if conns, err := gateway.Discover(dctx); err == nil {
			if c, ok := conns["s3_object_store"]; ok && c.GatewayBase != "" {
				cfg.Brain.GatewayURL = c.GatewayBase
				log.Printf("brain: using s3 connector (token-free): %s", c.GatewayBase)
			}
		}
		dcancel()
	}

	// Claude auth: a freshly-ignited home (and any worker) has no OAuth credential,
	// so route Claude through the Anthropic connector by self-discovering it — the
	// same identity-authed path as the brain. Skipped when ANTHROPIC_BASE_URL is
	// already set or this sprite has an OAuth login (don't override it).
	if os.Getenv("ANTHROPIC_BASE_URL") == "" && !hasClaudeOAuth() {
		dctx, dcancel := context.WithTimeout(context.Background(), 15*time.Second)
		if base := gateway.AnthropicBaseURL(dctx); base != "" {
			os.Setenv("ANTHROPIC_BASE_URL", base)
			log.Printf("claude: using anthropic connector (token-free): %s", base)
		}
		dcancel()
	}

	// Fleet brain: create it first so operational secrets + policy rehydrate from
	// it before anything that depends on them. nil when no brain (agent runs solo).
	var fleetSvc *fleet.Service
	if cfg.Brain.Enabled() {
		fs, err := fleet.New(cfg)
		if err != nil {
			log.Printf("fleet: disabled (init failed: %v)", err)
		} else {
			fleetSvc = fs
		}
	} else {
		log.Printf("fleet: disabled (no brain configured)")
	}

	// Rehydrate operational secrets from the brain (symmetry, DESIGN §2.1/§4.2):
	// every sprite reads the same capabilities, so any worker is as capable as
	// home. Env values still win if explicitly set.
	if fleetSvc != nil {
		sctx, scancel := context.WithTimeout(context.Background(), 15*time.Second)
		if cfg.SpriteAPIToken == "" {
			if tok := fleetSvc.GetSecret(sctx, fleet.SecretSpritesAPIToken); tok != "" {
				cfg.SpriteAPIToken = tok
				log.Printf("secrets: loaded sprites-api-token from brain (spawn/reap enabled)")
			}
		}
		if gh := fleetSvc.GetSecret(sctx, fleet.SecretGitHubToken); gh != "" {
			setupGitHubAuth(gh) // GH_TOKEN for gh + a git credential helper (no token on disk)
			log.Printf("secrets: loaded github token from brain (git/gh enabled)")
		}
		if fly := fleetSvc.GetSecret(sctx, fleet.SecretFlyToken); fly != "" {
			setupFlyAuth(fly) // FLY_API_TOKEN + ensure flyctl installed/on PATH
			log.Printf("secrets: loaded fly token from brain (flyctl enabled)")
		}
		scancel()
	}

	// Spawn capability (M4): live when a sprites token is available (env or brain).
	spawner := spawn.New(cfg)
	log.Printf("spawn: available=%v", spawner.Available())

	// Effective capability policy (P2.5): tools.permission_mode enforces which
	// permission mode the agent's Claude runs in. Fall back to config otherwise.
	permissionMode := cfg.PermissionMode
	if fleetSvc != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		if eff := fleetSvc.EffectivePolicy(ctx); eff.PermissionMode != "" {
			permissionMode = eff.PermissionMode
			log.Printf("policy: permission_mode=%s merge=%s spawn_max=%d", eff.PermissionMode, eff.Merge, eff.SpawnMaxTotal)
		}
		cancel()
	}

	log.Printf("permissions: dangerous-skip=%v (fleet-wide; scoped mode=%s when off)", cfg.DangerousSkip, permissionMode)
	h := hub.NewHub(hub.Config{
		WorkDir:        cfg.WorkDir,
		ProjectsDir:    cfg.ClaudeProjectsDir,
		UploadsDir:     filepath.Join(cfg.WorkDir, ".sprite-agent", "uploads"),
		DangerousSkip:  cfg.DangerousSkip,
		PermissionMode: permissionMode,
		SettingsPath:   cfg.SettingsPath,
		MCPConfigPath:  cfg.MCPConfigPath,
		AppendSystem:   fleetAffordance(cfg, spawner.Available()),
	})
	go h.Run()

	// Keep the sprite awake while it has active work (Claude generating or a client
	// attached), so autonomous tasks don't get suspended mid-run. Releases when idle
	// so an idle sprite still pauses. Local Tasks API — every sprite holds itself.
	go keepalive.Run(context.Background(), func() bool { return !h.IsIdle() })

	// Pass a nil Fleet when there's no brain so /api/fleet reports unavailable
	// (avoids a typed-nil interface).
	var roster server.Fleet
	if fleetSvc != nil {
		roster = fleetSvc
	}
	srv := server.New(cfg, h, roster, spawner)

	if fleetSvc != nil {
		// Idle-based self-reaping (DESIGN §2.3, disabled by default).
		fleetSvc.SetIdleReaping(h.IsIdle, cfg.IdleReapAfter)
		// Presence (P2.3): advertise human attachment so other surfaces defer (§2.4).
		fleetSvc.SetAttendanceProbe(h.Attendance)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		if err := fleetSvc.Register(ctx); err != nil {
			log.Printf("fleet: registration failed: %v", err)
		} else {
			log.Printf("fleet: registered %s into brain s3://%s (idle-reap=%s)", cfg.AgentID, cfg.Brain.Bucket, cfg.IdleReapAfter)
		}
		cancel()
		fleetSvc.StartHeartbeat(context.Background())

		// Frictionless shared memory: sync a local markdown dir with the brain so
		// recording a learning is a plain file write and every sprite boots knowing
		// what the fleet has learned (memsync pulls on boot, pushes on local change).
		go memsync.Run(context.Background(), fleetSvc.Brain(), fleetMemoryDir(), cfg.AgentID)

		// Dispatch (P2.1): poll this agent's task inbox and inject each task into a
		// local session so it materializes in the transcript (seam #2). Label the
		// session so the dispatched work shows up in the UI list (visible + attachable).
		inject := func(sessionID, task, kind string) error {
			if kind == fleet.KindNote {
				// A note is informational — deliver it but make clear it is NOT work,
				// so a relayed report/FYI is never executed as a task.
				srv.RegisterSession(sessionID, "note: "+taskSnippet(task))
				framed := "[Informational note from a fleet peer — NOT a task] Shared for your awareness " +
					"only. Do NOT execute it, act on it, or treat it as work — just take it in. No response " +
					"or action is needed unless it changes something you're already doing.\n\n" + task
				return h.InjectMessage(sessionID, framed)
			}
			srv.RegisterSession(sessionID, "task: "+taskSnippet(task))
			// Frame dispatched work, and pin down the RESULT contract: the peer pulls
			// the result from this session — the worker must NOT push it back (a pushed
			// result is misread as a new task, the bug this protocol exists to prevent).
			framed := "[Dispatched task from a fleet peer] Work this to completion in this session. " +
				"Keep your fleet phase current (POST /api/fleet/phase {\"phase\":\"…\"}) at each milestone and a " +
				"final \"done: <result>\". Put your final answer/report as your LAST message in this session — the " +
				"peer RETRIEVES it by pulling this session. Do NOT dispatch, curl, or otherwise send your result " +
				"back to anyone; that would be misread as a new task. Just finish here.\n\n" + task
			return h.InjectMessage(sessionID, framed)
		}
		fleetSvc.StartTaskPolling(context.Background(), inject)

		// Reaper: on token-bearing agents, destroy reapable/dead workers and
		// clean their brain entries. Home is never reaped (fleet.ReapTargets).
		go reaper.New(fleetSvc, spawner, cfg.ReapInterval, cfg.DeadReapAfter).Run(context.Background())
	}

	httpServer := &http.Server{Addr: cfg.Addr, Handler: srv.Handler()}

	go func() {
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
		sig := <-sigChan
		log.Printf("received %v, shutting down", sig)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(ctx); err != nil {
			log.Printf("shutdown error: %v", err)
		}
		os.Exit(0)
	}()

	log.Printf("listening on %s", cfg.Addr)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
