package main

import (
	"context"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/clouvet/sprite-swarm/internal/pi"
)

// setupPiMCPBridge gives the Pi runtime the fleet's MCP servers. Pi ships no built-in MCP,
// so we materialize an in-house bridge extension (internal/pi/bridge) that reads the same
// composed mcp.json Claude uses and registers each server's tools into Pi. It also ensures
// the extension's MCP client SDK is installed. No-op when there's no composed config.
func setupPiMCPBridge(ctx context.Context, mcpConfigPath string) {
	if mcpConfigPath == "" {
		return // no MCP servers composed → nothing to bridge
	}
	home := os.Getenv("HOME")
	if home == "" {
		home = "/home/sprite"
	}
	dir := filepath.Join(home, ".pi", "agent", "extensions", "mcp-bridge")
	pkgChanged, err := pi.MaterializeBridge(dir)
	if err != nil {
		log.Printf("pi: mcp bridge: materialize failed: %v (MCP tools unavailable under Pi)", err)
		return
	}
	// The extension reads this at load to find the composed server set.
	_ = os.Setenv("SPRITE_AGENT_MCP_BRIDGE_CONFIG", mcpConfigPath)

	// Install the MCP client SDK next to the extension on first boot or when deps change.
	_, sdkErr := os.Stat(filepath.Join(dir, "node_modules", "@modelcontextprotocol", "sdk"))
	if !pkgChanged && sdkErr == nil {
		log.Printf("pi: mcp bridge ready (%s)", mcpConfigPath)
		return
	}
	npm, err := exec.LookPath("npm")
	if err != nil {
		log.Printf("pi: mcp bridge: npm not found — cannot install MCP SDK; MCP tools unavailable under Pi")
		return
	}
	log.Printf("pi: mcp bridge: installing MCP client SDK (first boot; ~10-20s)…")
	ic, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	// Local (non-global) install, so the nvm npm shim's npm_config_prefix guard doesn't
	// bite; strip any inherited prefix to be safe. --omit=dev keeps it lean.
	cmd := exec.CommandContext(ic, npm, "install", "--omit=dev")
	cmd.Dir = dir
	cmd.Env = stripEnv(os.Environ(), "npm_config_prefix")
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Printf("pi: mcp bridge: SDK install failed: %v: %s", err, tailBytes(out, 500))
		return
	}
	log.Printf("pi: mcp bridge ready (extension %s, config %s)", dir, mcpConfigPath)
}
