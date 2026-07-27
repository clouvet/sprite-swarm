package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

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
