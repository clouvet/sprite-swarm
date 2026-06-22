// Command sprite-agent is the single symmetric agent that runs on a sprite:
// a session service (web chat UI driving Claude Code), with GitHub, spawn, and
// minimal-fleet-brain capabilities. See README.md and docs/sprite-agent-V2-plan.md.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/clouvet/sprite-agent/internal/config"
	"github.com/clouvet/sprite-agent/internal/fleet"
	"github.com/clouvet/sprite-agent/internal/hub"
	"github.com/clouvet/sprite-agent/internal/server"
)

func main() {
	cfg := config.FromEnv()
	log.Printf("sprite-agent starting: id=%s addr=%s workdir=%s projects=%s",
		cfg.AgentID, cfg.Addr, cfg.WorkDir, cfg.ClaudeProjectsDir)

	h := hub.NewHub(hub.Config{
		WorkDir:        cfg.WorkDir,
		ProjectsDir:    cfg.ClaudeProjectsDir,
		PermissionMode: cfg.PermissionMode,
		SettingsPath:   cfg.SettingsPath,
		MCPConfigPath:  cfg.MCPConfigPath,
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
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			if err := fleetSvc.Register(ctx); err != nil {
				log.Printf("fleet: registration failed: %v", err)
			} else {
				log.Printf("fleet: registered %s into brain s3://%s", cfg.AgentID, cfg.Brain.Bucket)
			}
			cancel()
			fleetSvc.StartHeartbeat(context.Background())
		}
	} else {
		log.Printf("fleet: disabled (no brain configured)")
	}

	srv := server.New(cfg, h, roster)
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
