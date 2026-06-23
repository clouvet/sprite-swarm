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
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/clouvet/sprite-agent/internal/config"
	"github.com/clouvet/sprite-agent/internal/fleet"
	"github.com/clouvet/sprite-agent/internal/hub"
	"github.com/clouvet/sprite-agent/internal/reaper"
	"github.com/clouvet/sprite-agent/internal/server"
	"github.com/clouvet/sprite-agent/internal/spawn"
)

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
		b.WriteString("The live fleet roster is available at GET /api/fleet on this service. ")
	}
	if spawnAvailable {
		b.WriteString("To create a worker, POST /api/fleet/spawn (or use the sprites API); the new " +
			"sprite boots this same artifact and registers into the shared brain automatically. " +
			"To assign work to a peer, POST /api/fleet/dispatch {\"target\":\"<id>\",\"task\":\"…\"} — " +
			"it lands in that worker's own session (attach to watch). ")
	} else {
		b.WriteString("Spawning is not yet wired on this sprite (no sprites API token), so for now " +
			"do the work here and note when a worker sprite would have been the better tool. ")
	}
	b.WriteString("Shared fleet memory is available — record durable learnings so peers and future " +
		"sprites inherit them.")
	return b.String()
}

func main() {
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

	// Spawn capability (M4): live when SPRITE_API_TOKEN is set, otherwise a stub.
	spawner := spawn.New(cfg)
	log.Printf("spawn: available=%v", spawner.Available())

	h := hub.NewHub(hub.Config{
		WorkDir:        cfg.WorkDir,
		ProjectsDir:    cfg.ClaudeProjectsDir,
		PermissionMode: cfg.PermissionMode,
		SettingsPath:   cfg.SettingsPath,
		MCPConfigPath:  cfg.MCPConfigPath,
		AppendSystem:   fleetAffordance(cfg, spawner.Available()),
	})
	go h.Run()

	// Fleet brain (M4): register this agent and expose the roster. When no
	// brain is configured the agent runs solo; pass a nil RosterProvider so the
	// /api/fleet endpoint reports unavailable (avoids a typed-nil interface).
	var roster server.RosterProvider
	if cfg.Brain.Enabled() {
		fleetSvc, err := fleet.New(cfg)
		if err != nil {
			log.Printf("fleet: disabled (init failed: %v)", err)
		} else {
			roster = fleetSvc
			// Idle-based self-reaping: a worker that sits idle long enough marks
			// itself reapable (DESIGN §2.3). Disabled by default (0).
			fleetSvc.SetIdleReaping(h.IsIdle, cfg.IdleReapAfter)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			if err := fleetSvc.Register(ctx); err != nil {
				log.Printf("fleet: registration failed: %v", err)
			} else {
				log.Printf("fleet: registered %s into brain s3://%s (idle-reap=%s)", cfg.AgentID, cfg.Brain.Bucket, cfg.IdleReapAfter)
			}
			cancel()
			fleetSvc.StartHeartbeat(context.Background())

			// Dispatch (P2.1): poll this agent's task inbox and inject each task
			// into a local session so it materializes in the transcript (seam #2).
			fleetSvc.StartTaskPolling(context.Background(), h.InjectMessage)

			// Reaper: on token-bearing agents, destroy reapable/dead workers and
			// clean their brain entries. Home is never reaped (fleet.ReapTargets).
			go reaper.New(fleetSvc, spawner, cfg.ReapInterval, cfg.DeadReapAfter).Run(context.Background())
		}
	} else {
		log.Printf("fleet: disabled (no brain configured)")
	}

	srv := server.New(cfg, h, roster, spawner)
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
