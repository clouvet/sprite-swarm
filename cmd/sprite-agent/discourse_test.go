package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupDiscourseMCP(t *testing.T) {
	dir := t.TempDir()

	// A full profile object with two sites (private + public).
	secret := `{"auth_pairs":[
		{"site":"https://community.fly.io","api_key":"AAA","api_username":"system"},
		{"site":"https://private.example.com","api_key":"BBB","api_username":"reader"}
	]}`
	mcpPath, err := setupDiscourseMCP(dir, secret)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	if mcpPath != filepath.Join(dir, "mcp.json") {
		t.Errorf("mcp path = %q", mcpPath)
	}

	// Profile file is 0600 and holds both sites' keys.
	profilePath := filepath.Join(dir, "discourse-profile.json")
	fi, err := os.Stat(profilePath)
	if err != nil {
		t.Fatalf("stat profile: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("profile perms = %v, want 0600", fi.Mode().Perm())
	}
	pb, _ := os.ReadFile(profilePath)
	if !strings.Contains(string(pb), "AAA") || !strings.Contains(string(pb), "BBB") {
		t.Errorf("profile missing keys: %s", pb)
	}

	// mcp.json launches @discourse/mcp against the profile (read-only: no writes flag).
	mb, _ := os.ReadFile(mcpPath)
	var cfg struct {
		MCPServers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(mb, &cfg); err != nil {
		t.Fatalf("parse mcp.json: %v", err)
	}
	d, ok := cfg.MCPServers["discourse"]
	if !ok {
		t.Fatalf("no discourse server in mcp.json: %s", mb)
	}
	if d.Command != "npx" {
		t.Errorf("command = %q", d.Command)
	}
	joined := strings.Join(d.Args, " ")
	if !strings.Contains(joined, "@discourse/mcp") || !strings.Contains(joined, "--profile") {
		t.Errorf("args missing server/profile: %v", d.Args)
	}
	if strings.Contains(joined, "allow_writes") || strings.Contains(joined, "read_only=false") {
		t.Errorf("expected read-only, got write-enabled args: %v", d.Args)
	}
}

func TestSetupDiscourseMCPBareArray(t *testing.T) {
	dir := t.TempDir()
	// A bare auth_pairs array is accepted and wrapped into a profile.
	if _, err := setupDiscourseMCP(dir, `[{"site":"https://x.io","api_key":"K","api_username":"u"}]`); err != nil {
		t.Fatalf("bare array: %v", err)
	}
	pb, _ := os.ReadFile(filepath.Join(dir, "discourse-profile.json"))
	if !strings.Contains(string(pb), "auth_pairs") {
		t.Errorf("bare array not wrapped: %s", pb)
	}
}

func TestSetupDiscourseMCPRejectsEmpty(t *testing.T) {
	dir := t.TempDir()
	for _, bad := range []string{"", "[]", "{}", "not json"} {
		if _, err := setupDiscourseMCP(dir, bad); err == nil {
			t.Errorf("expected error for %q", bad)
		}
	}
}
