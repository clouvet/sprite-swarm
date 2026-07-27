package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// discourseAuthPair is one entry in the @discourse/mcp profile's auth_pairs: a
// site plus its credentials. api_key/api_username is an admin key; user_api_key
// is the per-user variant. We only read Site for logging — the rest round-trips
// verbatim into the profile file.
type discourseAuthPair struct {
	Site        string `json:"site"`
	APIKey      string `json:"api_key,omitempty"`
	APIUsername string `json:"api_username,omitempty"`
	UserAPIKey  string `json:"user_api_key,omitempty"`
}

// setupDiscourseMCP materializes the optional Discourse MCP server from a brain
// secret. The secret is the @discourse/mcp profile — either a full object
// ({"auth_pairs":[...]}) or a bare auth_pairs array. It writes a 0600 profile
// (holding the keys) and returns the server's name + mcp.json entry for the
// caller to compose into the shared config (see writeMCPConfig).
//
// Optional by construction: only fleets that store a `discourse` secret get the
// server. Two instances (e.g. a private + a public forum) are just two entries
// in auth_pairs — one server routes each request to the matching site.
func setupDiscourseMCP(baseDir, secret string) (string, map[string]any, error) {
	profile := map[string]any{}
	var pairs []discourseAuthPair
	trimmed := strings.TrimSpace(secret)
	if strings.HasPrefix(trimmed, "[") {
		if err := json.Unmarshal([]byte(trimmed), &pairs); err != nil {
			return "", nil, fmt.Errorf("parse auth_pairs array: %w", err)
		}
		profile["auth_pairs"] = pairs
	} else {
		if err := json.Unmarshal([]byte(trimmed), &profile); err != nil {
			return "", nil, fmt.Errorf("parse profile: %w", err)
		}
		raw, _ := json.Marshal(profile["auth_pairs"])
		_ = json.Unmarshal(raw, &pairs) // best-effort, for the site count/log only
	}
	if len(pairs) == 0 {
		return "", nil, fmt.Errorf("no auth_pairs found in discourse secret")
	}

	profileJSON, err := json.Marshal(profile)
	if err != nil {
		return "", nil, err
	}
	profilePath := filepath.Join(baseDir, "discourse-profile.json")
	if err := os.WriteFile(profilePath, profileJSON, 0o600); err != nil {
		return "", nil, fmt.Errorf("write profile: %w", err)
	}

	// Read-only by default (no --allow_writes / --read_only=false): the fleet only
	// pulls forum context in, never posts. The key stays in the 0600 profile file,
	// not in the process args (which --auth_pairs on the command line would leak).
	entry := map[string]any{
		"command": "npx",
		"args":    []string{"-y", "@discourse/mcp@latest", "--profile", profilePath},
	}

	sites := make([]string, 0, len(pairs))
	for _, p := range pairs {
		sites = append(sites, p.Site)
	}
	log.Printf("secrets: loaded discourse creds from brain (mcp read-only, %d site(s): %s)",
		len(pairs), strings.Join(sites, ", "))
	return "discourse", entry, nil
}
