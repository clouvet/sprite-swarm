package main

import (
	"context"
	"log"
	"os"

	"github.com/clouvet/sprite-swarm/internal/gateway"
)

// setupSlackMCP enables the in-house Slack MCP when a `slack` gateway connector is
// present for this org (discovered by sprite identity). Unlike the other MCP
// servers there's NO brain secret and NO token: the mcp.json entry points at this
// same binary's `slack-mcp` subcommand, which reaches Slack through the gateway
// token-free. Returns ("", nil, nil) when no connector is configured (absent, not
// an error).
func setupSlackMCP(ctx context.Context) (string, map[string]any, error) {
	if gateway.SlackBase(ctx) == "" {
		return "", nil, nil
	}
	self, err := os.Executable()
	if err != nil {
		return "", nil, err
	}
	entry := map[string]any{"command": self, "args": []string{"slack-mcp"}}
	log.Printf("secrets: slack mcp enabled (gateway connector, token-free)")
	return "slack", entry, nil
}
