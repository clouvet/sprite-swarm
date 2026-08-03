package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
)

// honeycombConfig is the brain secret shape ({url, key}). url is the hosted MCP
// endpoint (US: https://mcp.honeycomb.io/mcp, EU: https://mcp.eu1.honeycomb.io/mcp);
// key is the Management API key in `KEY_ID:SECRET` form.
type honeycombConfig struct {
	URL string `json:"url"`
	Key string `json:"key"`
}

// setupHoneycombMCP wires the optional Honeycomb MCP from a brain secret. Honeycomb
// is a REMOTE, streaming MCP server, so it can't be fronted by a gateway connector
// (the proxy strips MCP session headers) — we reach it with the `mcp-remote` stdio
// bridge (npx, no build). The Management key is injected into the process env as
// HONEYCOMB_KEY and referenced as ${HONEYCOMB_KEY} in the --header arg, so the key
// is in NEITHER the 0644 mcp.json NOR the process argv (mcp-remote expands it).
// Returns the server name + mcp.json entry to compose.
func setupHoneycombMCP(secret string) (string, map[string]any, error) {
	url, key, err := parseHoneycombSecret(secret)
	if err != nil {
		return "", nil, err
	}
	os.Setenv("HONEYCOMB_KEY", key) // expanded by mcp-remote; never in argv/mcp.json
	entry := map[string]any{
		"command": "npx",
		"args":    []string{"-y", "mcp-remote", url, "--header", "Authorization: Bearer ${HONEYCOMB_KEY}"},
	}
	log.Printf("secrets: loaded honeycomb config from brain (hosted mcp via mcp-remote, %s)", url)
	return "honeycomb", entry, nil
}

// parseHoneycombSecret pulls the hosted MCP url + key. Shared by boot and
// reload-secrets, and unit-testable.
func parseHoneycombSecret(secret string) (url, key string, err error) {
	var cfg honeycombConfig
	if err := json.Unmarshal([]byte(strings.TrimSpace(secret)), &cfg); err != nil {
		return "", "", fmt.Errorf("parse honeycomb secret: %w", err)
	}
	url = strings.TrimSpace(cfg.URL)
	key = strings.TrimSpace(cfg.Key)
	if url == "" || key == "" {
		return "", "", fmt.Errorf("honeycomb secret needs both url and key")
	}
	return url, key, nil
}
