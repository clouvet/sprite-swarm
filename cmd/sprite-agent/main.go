// Command sprite-agent is the single symmetric agent that runs on a sprite:
// a session service (web chat UI driving Claude Code), with GitHub, spawn, and
// minimal-fleet-brain capabilities. See README.md and docs/sprite-agent-V2-plan.md.
package main

import (
	"log"

	"github.com/clouvet/sprite-agent/internal/config"
)

func main() {
	cfg := config.FromEnv()
	log.Printf("sprite-agent starting: id=%s addr=%s workdir=%s", cfg.AgentID, cfg.Addr, cfg.WorkDir)
	// The session service is wired up in M2 (internal/server). M1 bootstraps
	// the module + layout so `go build ./...` is green from the first commit.
}
