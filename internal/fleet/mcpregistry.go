package fleet

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// mcpRegistryKey holds the user-added MCP servers as one JSON object {name: entry},
// where each entry is a standard MCP server config ({"command","args","env"} for
// stdio, or a url-based remote server). Brain-stored ⇒ fleet-wide + symmetric, the
// same posture as the discourse/grafana/... secrets. One aggregate blob (rather
// than a key per name) keeps add/remove a simple read-modify-write with no
// key-listing dependency.
const mcpRegistryKey = "fleet/config/mcp-servers"

// MCPRegistry returns the user-added MCP servers (name -> raw config), or an empty
// map when none are configured.
func (s *Service) MCPRegistry(ctx context.Context) (map[string]json.RawMessage, error) {
	data, err := s.brain.Get(ctx, mcpRegistryKey)
	if err != nil {
		return map[string]json.RawMessage{}, nil // absent = empty registry
	}
	reg := map[string]json.RawMessage{}
	if len(strings.TrimSpace(string(data))) == 0 {
		return reg, nil
	}
	if err := json.Unmarshal(data, &reg); err != nil {
		return nil, fmt.Errorf("parse mcp registry: %w", err)
	}
	return reg, nil
}

// SetMCPServer adds or replaces one server in the registry. The config must be a
// JSON object with a "command" (stdio) or "url" (remote) — enough to be a real
// MCP server entry — so a paste-o typo doesn't poison every sprite's mcp.json.
func (s *Service) SetMCPServer(ctx context.Context, name string, config json.RawMessage) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("mcp server name is required")
	}
	var probe map[string]any
	if err := json.Unmarshal(config, &probe); err != nil {
		return fmt.Errorf("config must be a JSON object: %w", err)
	}
	if _, hasCmd := probe["command"]; !hasCmd {
		if _, hasURL := probe["url"]; !hasURL {
			return fmt.Errorf(`config needs a "command" (stdio) or "url" (remote)`)
		}
	}
	reg, err := s.MCPRegistry(ctx)
	if err != nil {
		return err
	}
	reg[name] = config
	return s.writeMCPRegistry(ctx, reg)
}

// DeleteMCPServer removes a server from the registry (no error if absent).
func (s *Service) DeleteMCPServer(ctx context.Context, name string) error {
	reg, err := s.MCPRegistry(ctx)
	if err != nil {
		return err
	}
	delete(reg, strings.TrimSpace(name))
	return s.writeMCPRegistry(ctx, reg)
}

func (s *Service) writeMCPRegistry(ctx context.Context, reg map[string]json.RawMessage) error {
	// Marshal with sorted keys for stable brain writes.
	names := make([]string, 0, len(reg))
	for n := range reg {
		names = append(names, n)
	}
	sort.Strings(names)
	ordered := make(map[string]json.RawMessage, len(reg))
	for _, n := range names {
		ordered[n] = reg[n]
	}
	data, err := json.MarshalIndent(ordered, "", "  ")
	if err != nil {
		return err
	}
	return s.brain.Put(ctx, mcpRegistryKey, data)
}
