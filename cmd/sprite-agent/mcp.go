package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/clouvet/sprite-swarm/internal/fleet"
)

// composeMCP builds the sprite's mcp.json by merging the built-in integrations
// (each gated on its own brain secret or gateway connector) with the user-added
// servers from the brain registry (fleet/config/mcp — see fleet.MCPRegistry),
// writes it, and returns its path. Runs at boot AND on any /api/mcp change
// (regenerate + RestartActiveSessions is the apply path, since a live Claude
// won't hot-reload --mcp-config). operatorPath, when set (SPRITE_AGENT_MCP_CONFIG),
// wins — we don't overwrite an operator-supplied config. Returns "" when there are
// no servers at all.
func composeMCP(ctx context.Context, fleetSvc *fleet.Service, workDir, operatorPath string) (string, error) {
	if operatorPath != "" {
		return operatorPath, nil
	}
	baseDir := filepath.Join(workDir, ".sprite-agent")
	servers := map[string]any{}

	// Built-in integrations (special provisioning: creds, binaries, connectors).
	if prof := fleetSvc.GetSecret(ctx, fleet.SecretDiscourse); prof != "" {
		if name, entry, err := setupDiscourseMCP(baseDir, prof); err != nil {
			log.Printf("mcp: discourse setup failed: %v", err)
		} else {
			servers[name] = entry
		}
	}
	if gsec := fleetSvc.GetSecret(ctx, fleet.SecretGrafana); gsec != "" {
		if name, entry, err := setupGrafanaMCP(ctx, baseDir, gsec); err != nil {
			log.Printf("mcp: grafana setup failed: %v", err)
		} else {
			servers[name] = entry
		}
	}
	if ssec := fleetSvc.GetSecret(ctx, fleet.SecretSentry); ssec != "" {
		if name, entry, err := setupSentryMCP(ssec); err != nil {
			log.Printf("mcp: sentry setup failed: %v", err)
		} else {
			servers[name] = entry
		}
	}
	if hsec := fleetSvc.GetSecret(ctx, fleet.SecretHoneycomb); hsec != "" {
		if name, entry, err := setupHoneycombMCP(hsec); err != nil {
			log.Printf("mcp: honeycomb setup failed: %v", err)
		} else {
			servers[name] = entry
		}
	}
	if name, entry, err := setupSlackMCP(ctx); err != nil {
		log.Printf("mcp: slack setup failed: %v", err)
	} else if entry != nil {
		servers[name] = entry
	}

	// User-added servers from the brain registry — added after the built-ins so a
	// registry entry can extend (or, by same name, override) them.
	if reg, err := fleetSvc.MCPRegistry(ctx); err != nil {
		log.Printf("mcp: registry read failed: %v", err)
	} else {
		for name, raw := range reg {
			var entry map[string]any
			if err := json.Unmarshal(raw, &entry); err != nil {
				log.Printf("mcp: skipping malformed registry entry %q: %v", name, err)
				continue
			}
			servers[name] = entry
		}
	}

	if len(servers) == 0 {
		return "", nil
	}
	return writeMCPConfig(baseDir, servers)
}

// writeMCPConfig writes the composed mcp.json from a set of server entries keyed
// by server name, and returns its path for --mcp-config. Each integration
// (Discourse, Grafana, …) materializes its own creds and contributes one entry,
// so the fleet can run several MCP servers at once instead of one clobbering the
// file. Written 0644 — it carries no secrets itself (tokens live in 0600 files
// the entries point at), only URLs and paths.
func writeMCPConfig(baseDir string, servers map[string]any) (string, error) {
	cfg := map[string]any{"mcpServers": servers}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return "", err
	}
	path := filepath.Join(baseDir, "mcp.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", fmt.Errorf("write mcp config: %w", err)
	}
	return path, nil
}
