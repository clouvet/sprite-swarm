package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
)

// sentryConfig is the brain secret shape ({host, access_token}); `token` is
// accepted as an alias for the access token.
type sentryConfig struct {
	Host  string `json:"host"`
	Token string `json:"access_token"`
	Alt   string `json:"token"`
}

// setupSentryMCP wires the optional Sentry MCP server from a brain secret. It
// runs the official @sentry/mcp-server over stdio (npx, no build — like the
// Discourse server) against the configured host, and injects the user auth token
// into the PROCESS environment (SENTRY_ACCESS_TOKEN) rather than mcp.json, so the
// token stays out of the 0644 config file (the MCP subprocess inherits it).
// Sentry has no connector path (its server takes a bare --host and can't be aimed
// at the gateway), so the token lives on the sprite. Read-only is enforced by the
// token's scopes. Returns the server name + mcp.json entry to compose.
func setupSentryMCP(secret string) (string, map[string]any, error) {
	host, token, err := parseSentrySecret(secret)
	if err != nil {
		return "", nil, err
	}
	os.Setenv("SENTRY_ACCESS_TOKEN", token) // inherited by the npx MCP subprocess
	entry := map[string]any{
		"command": "npx",
		"args":    []string{"-y", "@sentry/mcp-server@latest", "--host=" + host},
	}
	log.Printf("secrets: loaded sentry config from brain (mcp stdio, host %s)", host)
	return "sentry", entry, nil
}

// parseSentrySecret pulls host + access token from the brain secret. Split out so
// boot and reload-secrets share it (and it's unit-testable).
func parseSentrySecret(secret string) (host, token string, err error) {
	var cfg sentryConfig
	if err := json.Unmarshal([]byte(strings.TrimSpace(secret)), &cfg); err != nil {
		return "", "", fmt.Errorf("parse sentry secret: %w", err)
	}
	host = strings.TrimSpace(cfg.Host)
	token = strings.TrimSpace(cfg.Token)
	if token == "" {
		token = strings.TrimSpace(cfg.Alt)
	}
	if host == "" || token == "" {
		return "", "", fmt.Errorf("sentry secret needs both host and access_token")
	}
	return host, token, nil
}
