// Command sprite-agent is the single symmetric agent that runs on a sprite:
// a session service (web chat UI driving Claude Code), with GitHub, spawn, and
// minimal-fleet-brain capabilities. See README.md.
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

	"github.com/clouvet/sprite-swarm/internal/config"
	"github.com/clouvet/sprite-swarm/internal/fleet"
	"github.com/clouvet/sprite-swarm/internal/gateway"
	"github.com/clouvet/sprite-swarm/internal/hub"
	"github.com/clouvet/sprite-swarm/internal/keepalive"
	"github.com/clouvet/sprite-swarm/internal/memsync"
	"github.com/clouvet/sprite-swarm/internal/secret"
	"github.com/clouvet/sprite-swarm/internal/server"
	"github.com/clouvet/sprite-swarm/internal/spawn"
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

// claudeAuthPlan is the pure decision of how Claude should authenticate.
type claudeAuthPlan struct {
	subscription bool // use CLAUDE_CODE_OAUTH_TOKEN; clear the connector env (it outranks the token)
	tryConnector bool // discover + set the Anthropic connector env
	// (neither set ⇒ leave the environment as-is: an explicit key or a /login credential)
}

// decideClaudeAuth resolves auth from the observable facts. Subscription is the
// default when a token is present; the connector is the fallback; and
// SPRITE_AGENT_CLAUDE_AUTH=connector forces the connector even when a token exists.
func decideClaudeAuth(hasToken, forceConnector, hasBaseURL, hasLogin bool) claudeAuthPlan {
	if hasToken && !forceConnector {
		return claudeAuthPlan{subscription: true}
	}
	if !forceConnector && (hasBaseURL || hasLogin) {
		return claudeAuthPlan{} // respect an explicit base URL / interactive login
	}
	return claudeAuthPlan{tryConnector: true}
}

// setupClaudeAuth applies the decision to the environment: the subscription
// (CLAUDE_CODE_OAUTH_TOKEN, billed to the plan) by default, else the Anthropic
// API-Gateway connector (billed per token, authed by sprite identity).
func setupClaudeAuth() {
	forceConnector := strings.EqualFold(os.Getenv("SPRITE_AGENT_CLAUDE_AUTH"), "connector")
	plan := decideClaudeAuth(
		os.Getenv("CLAUDE_CODE_OAUTH_TOKEN") != "",
		forceConnector,
		os.Getenv("ANTHROPIC_BASE_URL") != "",
		hasClaudeOAuth(),
	)
	if plan.subscription {
		// The token is outranked by ANTHROPIC_API_KEY in Claude Code's precedence,
		// so clear any connector env (e.g. one baked into a worker at spawn).
		os.Unsetenv("ANTHROPIC_API_KEY")
		os.Unsetenv("ANTHROPIC_BASE_URL")
		log.Printf("claude: subscription auth (CLAUDE_CODE_OAUTH_TOKEN)")
		return
	}
	if !plan.tryConnector {
		return // leave as-is
	}
	dctx, dcancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer dcancel()
	if base := gateway.AnthropicBaseURL(dctx); base != "" {
		os.Setenv("ANTHROPIC_BASE_URL", base)
		// Claude Code needs a key set when using a base URL without OAuth; the
		// gateway injects the real one by sprite identity, so this is a placeholder.
		if os.Getenv("ANTHROPIC_API_KEY") == "" {
			os.Setenv("ANTHROPIC_API_KEY", "sprite-gateway")
		}
		log.Printf("claude: connector auth (token-free): %s", base)
	} else if forceConnector {
		log.Printf("claude: SPRITE_AGENT_CLAUDE_AUTH=connector set but no Anthropic connector found")
	}
}

// checkClaudeAuth runs one tiny Claude call at boot to verify auth works, and logs
// a clear verdict. A misconfigured connector-only fleet otherwise fails invisibly
// (a 502 in the UI on the first chat/title); this turns that into an obvious
// startup line so the operator knows immediately what's wrong.
func checkClaudeAuth() {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	// Cheap model, trivial prompt — just enough to exercise the auth path.
	cmd := exec.CommandContext(ctx, "claude", "--model", "claude-haiku-4-5-20251001", "-p", "Reply with: ok")
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	if err != nil {
		hint := "set the Anthropic connector (ANTHROPIC_BASE_URL + ANTHROPIC_API_KEY) or a Claude OAuth login"
		snippet := strings.TrimSpace(string(out))
		if len(snippet) > 300 {
			snippet = snippet[:300]
		}
		log.Printf("claude: AUTH CHECK FAILED — chat & titles will 502. %s. (%v) %s", hint, err, snippet)
		return
	}
	log.Printf("claude: auth check OK — chat will work")
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
func fleetAffordance(cfg config.Config, spawnAvailable, githubAvailable bool) string {
	apiBase := cfg.Addr
	if strings.HasPrefix(apiBase, ":") {
		apiBase = "localhost" + apiBase
	}
	apiBase = "http://" + apiBase

	b := &strings.Builder{}
	fmt.Fprintf(b, "You are sprite-agent %q, one peer in a symmetric fleet of identical agents — "+
		"not a standalone assistant. For parallel or isolated work, prefer spinning up a worker "+
		"sprite (its own microVM, filesystem, and git checkout) over doing everything here. ", cfg.AgentID)
	if cfg.Brain.Enabled() {
		fmt.Fprintf(b, "Your fleet API lives on your OWN service at %s — this is a given: never discover, "+
			"announce, restate, or verify the API location, and don't pre-check the roster or remark on who "+
			"else is in the fleet just to act. When asked to spawn a worker, dispatch, etc., DO it (call the "+
			"endpoint) and report the outcome — skip the narration about plumbing. The live roster is "+
			"GET /api/fleet. ", apiBase)
		b.WriteString("Keep your own status current so peers can see how you're doing without interrupting you: " +
			"POST /api/fleet/phase {\"phase\":\"<one line: what you're doing now, or 'done: <result>'>\"}. " +
			"This is especially important while working a dispatched task — update it at each milestone and " +
			"when you finish, so when a human asks another agent \"how is <you> progressing?\" they get a real " +
			"answer from your latest phase (peers can't read your transcript; this note is the channel). ")
	}
	if spawnAvailable {
		b.WriteString("Do ALL fleet operations through these /api/fleet endpoints — do NOT shell out to the " +
			"`sprite` CLI or curl api.sprites.dev directly: that auth path may not exist on this fleet (the " +
			"endpoints route through the gateway connector, which works token-free), so the CLI will just fail. " +
			"To create a worker, POST /api/fleet/spawn — include a short \"label\" summarizing its task " +
			"(e.g. {\"label\":\"posthog integration\"}) so it is named wk-posthog-integration; omit the label " +
			"for a generic worker (a random wk-… id); pass \"name\":\"<name>\" for an exact name. The new " +
			"sprite boots this same artifact and registers into the shared brain automatically. " +
			"To pin a sprite so it won't auto-adopt the fleet's staged binary when it next wakes, spawn it " +
			"with \"env\":{\"SPRITE_AGENT_BOOT_UPDATE\":\"0\"}. " +
			"DELEGATING WORK follows ONE fixed protocol — use it exactly, never improvise a way to get " +
			"results: " +
			"(1) ASSIGN — POST /api/fleet/dispatch {\"target\":\"<id>\",\"task\":\"…\"}; the response has a " +
			"\"session_id\" — REMEMBER it, that's where the worker does the work in its own session. " +
			"(2) PROGRESS — GET /api/fleet/status?target=<id> returns the peer's latest phase + LIVE state " +
			"(generating now? sessions? awake?), so you can answer \"how is <worker> doing?\" without interrupting it. " +
			"(3) RESULT — GET /api/fleet/result?target=<id>&session=<session_id> returns the worker's final " +
			"answer (\"ready\":true once done), which you PULL and relay to the human in this chat. " +
			"After assigning, just tell the human it's dispatched and stop — do NOT narrate a plan, set up a " +
			"polling loop, or spawn background watchers. Pull status/result ON DEMAND: when the human asks, or " +
			"once when you'd reasonably expect it done; if it's not ready, say so — never sit in a loop. " +
			"NEVER ask a worker to dispatch/send/curl its result back to you — a result delivered " +
			"through dispatch is misread as a NEW task and executed, spawning runaway sessions. You PULL; the worker never pushes. " +
			"To send an informational FYI rather than work, add \"kind\":\"note\" to a dispatch — the recipient is told not to execute it. " +
			"To reap/tear a sprite down BY NAME, POST /api/fleet/destroy {\"target\":\"<name>\"} — this destroys its VM " +
			"and removes its brain entry. It refuses with HTTP 409 if a human is attached to that sprite " +
			"(the roster's present/👤 = the DEFER signal, §2.4); only after the human confirms, re-POST " +
			"with {\"target\":\"<id>\",\"force\":true}. Do NOT hand-roll teardown via the host socket or " +
			"guess routes — this endpoint is the mechanism. " +
			"To roll out a new build after this binary is updated: POST /api/fleet/update {\"target\":\"<id>\"|\"all\"} " +
			"stages your current binary and tells that worker (or every other agent) to self-update in place — they " +
			"re-exec, keeping their VM disk (repo/branch/uncommitted work). POST /api/fleet/update with no body updates " +
			"only this node. The roster's \"build\" hash shows who's stale (marked in the fleet context). " +
			"To HOST A WEB APP on its own public URL, do NOT try to serve it on an agent sprite — the agent " +
			"already owns the http port (you'll hit a 409). Instead: build the app, tar its FILES at the archive " +
			"root (e.g. `tar czf app.tgz -C <appdir> .` — entry point like index.html at the top, not nested in a " +
			"wrapper dir), stage the tarball to the brain (PUT it via the s3 connector), then POST /api/fleet/deploy-app " +
			"{\"artifact_url\":\"<brain url of the tarball>\",\"run\":\"<start command>\",\"http_port\":<port the app listens on>}. " +
			"To give the app a specific sprite name (e.g. \"host this as foo\"), add \"name\":\"foo\"; omit it for an " +
			"auto-generated app-<id> name (or pass \"name_prefix\":\"myprefix-\" to control just the prefix). " +
			"That creates a dedicated BARE sprite (no agent) which fetches + runs your app, so the app owns that " +
			"sprite's URL (served behind org login). The response returns the app's URL. " +
			"To CHANGE a deployed app in place (new build), stage the new tarball and POST /api/fleet/update-app " +
			"{\"name\":\"<app sprite name>\",\"artifact_url\":..,\"run\":..,\"http_port\":..} — it reinstalls onto the " +
			"SAME sprite, so the URL is unchanged; do NOT deploy a fresh sprite just to change an app. " +
			"To TEAR DOWN an app sprite, POST /api/fleet/destroy-app {\"name\":\"<app sprite name>\"}. Bare app " +
			"sprites are NOT in the fleet roster, so /api/fleet/destroy won't touch them — destroy-app is their teardown. ")
	} else {
		b.WriteString("Spawning is not yet wired on this sprite (no sprites API token), so for now " +
			"do the work here and note when a worker sprite would have been the better tool. ")
	}
	b.WriteString("To start a FRESH CONVERSATION on yourself — a new chat on THIS SAME sprite, e.g. to " +
		"hand a long thread off into a clean window, or split work off without flooding the current " +
		"context — POST /api/sessions {\"name\":\"<short title>\",\"message\":\"<the seed / handoff text>\"} " +
		"(to localhost:8080). It creates a new chat here, seeded with your message, that the human can open " +
		"from the chat list; for a large payload write the JSON to a file and `curl -sX POST " +
		"localhost:8080/api/sessions -d @<file>`. This is a NEW CHAT, not a new worker — never spawn a " +
		"worker just to get a fresh conversation. ")
	b.WriteString("To GIVE THE HUMAN A FILE you produced — a report, export, generated document, etc. — " +
		"WRITE IT to your working directory (cwd, a plain filename with no path). Loose files in your cwd " +
		"appear in the chat's context panel under \"Created\" as downloads. Do NOT paste a long file's " +
		"contents into the chat for the human to copy — write the file so they can download it. ")
	if githubAvailable {
		b.WriteString("You have GitHub access (git + gh are authenticated) — clone repos, branch, commit, " +
			"and open PRs directly. When asked to clone a repo, ALWAYS clone it into your current working " +
			"directory — a plain `git clone <url>` with NO target path. Never clone into /tmp, a subdirectory " +
			"(repos/, work/, etc.), or anywhere else. Your cwd is this chat's own workspace, and the UI's " +
			"cloned-repos view mirrors exactly the repos sitting directly in it — cloning elsewhere hides them " +
			"from the human and breaks that view. ")
	} else {
		b.WriteString("This fleet has NO GitHub access (no token configured) — git push / gh / opening PRs " +
			"will fail, so don't attempt them; work on the local filesystem and tell the human if a task " +
			"genuinely needs GitHub. ")
	}
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

// shouldBootSelfUpdate decides whether this process adopts the staged binary on
// boot (so a suspended sprite converges on wake). SPRITE_AGENT_BOOT_UPDATE=0
// opts out — it pins a sprite to its current binary so it won't downgrade to an
// older staged artifact (e.g. the one originating builds).
func shouldBootSelfUpdate(disable string) bool {
	if disable == "0" || strings.EqualFold(disable, "false") {
		return false
	}
	return true
}

// maybeBootSelfUpdate converges a worker to the fleet's staged binary on boot
// without a push: compare its build to the staged one and, if they differ, swap +
// re-exec (identical to POST /api/fleet/update). Best-effort — any error just
// continues booting on the current binary. Loop-safe: after re-exec the running
// binary IS the staged one, so PrepareSelfUpdate no-ops.
func maybeBootSelfUpdate(fleetSvc *fleet.Service) {
	if !shouldBootSelfUpdate(os.Getenv("SPRITE_AGENT_BOOT_UPDATE")) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	willUpdate, detail, err := fleetSvc.PrepareSelfUpdate(ctx)
	if err != nil {
		log.Printf("boot: self-update check skipped: %v (continuing on current build)", err)
		return
	}
	if !willUpdate {
		return
	}
	log.Printf("boot: adopting staged build (%s) — re-exec", detail)
	if err := fleetSvc.Reexec(); err != nil {
		log.Printf("boot: re-exec failed: %v", err)
	}
}

func main() {
	// `sprite-agent init` stands up a brand-new fleet (prime the brain + ignite home)
	// rather than running the agent. See launch-fleet.sh.
	if len(os.Args) > 1 && os.Args[1] == "init" {
		runInit(os.Args[2:])
		return
	}
	// `put-secret` writes one operational secret into the brain (via the token-free
	// s3 connector on a sprite, or S3 keys off it) — e.g. rotating the Claude token.
	if len(os.Args) > 1 && os.Args[1] == "put-secret" {
		runPutSecret(os.Args[2:])
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

	// Claude auth (subscription vs connector) is resolved after the brain's secrets
	// rehydrate below — a Claude subscription token stored in the brain should win
	// over the API connector, so the decision has to wait until it's loaded.

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
		if os.Getenv("CLAUDE_CODE_OAUTH_TOKEN") == "" {
			if tok := fleetSvc.GetSecret(sctx, fleet.SecretClaudeOAuthToken); tok != "" {
				os.Setenv("CLAUDE_CODE_OAUTH_TOKEN", tok)
				log.Printf("secrets: loaded claude oauth token from brain (subscription auth)")
			}
		}
		// Discourse MCP (optional): if forum creds are in the brain and the operator
		// hasn't supplied their own MCP config, generate the @discourse/mcp server so
		// pasting a Discourse link lets Claude pull the thread in. One server serves
		// every site in the profile (e.g. a private + a public forum).
		if cfg.MCPConfigPath == "" {
			if prof := fleetSvc.GetSecret(sctx, fleet.SecretDiscourse); prof != "" {
				if p, err := setupDiscourseMCP(filepath.Join(cfg.WorkDir, ".sprite-agent"), prof); err != nil {
					log.Printf("secrets: discourse mcp setup failed: %v", err)
				} else {
					cfg.MCPConfigPath = p
				}
			}
		}
		scancel()
	}

	// Boot-time self-update (workers only): adopt the fleet's staged binary on wake,
	// so a suspended/idle worker converges to the latest build WITHOUT a push (which
	// races the sprite's wake and often can't land). Re-execs if a different build is
	// staged; home is excluded — it originates builds and must not downgrade to an
	// older staged artifact. Same swap+re-exec as POST /api/fleet/update.
	if fleetSvc != nil {
		maybeBootSelfUpdate(fleetSvc)
	}

	// Resolve Claude auth now that any brain secrets are loaded: prefer the
	// subscription (CLAUDE_CODE_OAUTH_TOKEN) over the API connector; then a
	// boot-time self-check so a misconfigured fleet says so loudly in the logs.
	setupClaudeAuth()
	go checkClaudeAuth()

	// No sprites token (env or brain)? Fall back to a custom_api connector fronting
	// the Sprites API — spawn/reap then route through the gateway, authed by sprite
	// identity, with no token on the sprite or in the brain.
	if cfg.SpriteAPIToken == "" && cfg.SpriteAPIGateway == "" {
		dctx, dcancel := context.WithTimeout(context.Background(), 15*time.Second)
		if base := gateway.SpritesAPIBase(dctx, cfg.SpriteAPIConnectorID); base != "" {
			cfg.SpriteAPIGateway = base
			log.Printf("spawn: using custom_api connector for the Sprites API (token-free): %s", base)
		}
		dcancel()
	}

	// Spawn capability (M4): live when a sprites token (env or brain) or a Sprites
	// API gateway connector is available.
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
	// Worker-scoped env secrets: in-memory only (never disk/brain), injected into
	// each Claude process and shared by the hub (inject) and the server (manage).
	secrets := secret.NewStore()
	h := hub.NewHub(hub.Config{
		WorkDir:        cfg.WorkDir,
		ProjectsDir:    cfg.ClaudeProjectsDir,
		UploadsDir:     filepath.Join(cfg.WorkDir, ".sprite-agent", "uploads"),
		DangerousSkip:  cfg.DangerousSkip,
		PermissionMode: permissionMode,
		SettingsPath:   cfg.SettingsPath,
		MCPConfigPath:  cfg.MCPConfigPath,
		AppendSystem:   fleetAffordance(cfg, spawner.Available(), os.Getenv("GH_TOKEN") != ""),
		Secrets:        secrets,
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
	srv := server.New(cfg, h, roster, spawner, secrets)

	if fleetSvc != nil {
		// Presence (P2.3): advertise human attachment so other surfaces defer (§2.4).
		fleetSvc.SetAttendanceProbe(h.Attendance)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		if err := fleetSvc.Register(ctx); err != nil {
			log.Printf("fleet: registration failed: %v", err)
		} else {
			log.Printf("fleet: registered %s into brain s3://%s", cfg.AgentID, cfg.Brain.Bucket)
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
		fleetSvc.StartTaskPolling(context.Background(), inject, h.Generating)
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
