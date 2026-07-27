package main

import (
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
	name, entry, err := setupDiscourseMCP(dir, secret)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	if name != "discourse" {
		t.Errorf("name = %q", name)
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

	// The entry launches @discourse/mcp against the profile (read-only: no writes flag).
	if entry["command"] != "npx" {
		t.Errorf("command = %v", entry["command"])
	}
	args, _ := entry["args"].([]string)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "@discourse/mcp") || !strings.Contains(joined, "--profile") {
		t.Errorf("args missing server/profile: %v", args)
	}
	if strings.Contains(joined, "allow_writes") || strings.Contains(joined, "read_only=false") {
		t.Errorf("expected read-only, got write-enabled args: %v", args)
	}
}

func TestSetupDiscourseMCPBareArray(t *testing.T) {
	dir := t.TempDir()
	// A bare auth_pairs array is accepted and wrapped into a profile.
	if _, _, err := setupDiscourseMCP(dir, `[{"site":"https://x.io","api_key":"K","api_username":"u"}]`); err != nil {
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
		if _, _, err := setupDiscourseMCP(dir, bad); err == nil {
			t.Errorf("expected error for %q", bad)
		}
	}
}
