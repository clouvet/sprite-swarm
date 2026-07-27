package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteGrafanaToken(t *testing.T) {
	dir := t.TempDir()
	p, err := writeGrafanaToken(dir, "glsa_secret")
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("token perms = %v, want 0600", fi.Mode().Perm())
	}
	b, _ := os.ReadFile(p)
	if string(b) != "glsa_secret" {
		t.Errorf("token content = %q", b)
	}
}

func TestGrafanaConfigParse(t *testing.T) {
	// Both the canonical key and the `token` alias parse.
	for _, s := range []string{
		`{"url":"https://g.example.net","service_account_token":"glsa_x"}`,
		`{"url":"https://g.example.net/","token":"glsa_x"}`,
	} {
		var c grafanaConfig
		if err := json.Unmarshal([]byte(s), &c); err != nil {
			t.Fatalf("parse %s: %v", s, err)
		}
		tok := c.Token
		if tok == "" {
			tok = c.Alt
		}
		if c.URL == "" || tok != "glsa_x" {
			t.Errorf("parsed url=%q token=%q from %s", c.URL, tok, s)
		}
	}
}

func TestSetupGrafanaMCPRejectsIncomplete(t *testing.T) {
	dir := t.TempDir()
	// Missing url or token errors BEFORE any binary download (no network here).
	for _, bad := range []string{
		`{"url":"https://g.example.net"}`,           // no token
		`{"service_account_token":"glsa_x"}`,        // no url
		`{}`, `not json`, ``,
	} {
		if _, _, err := setupGrafanaMCP(dir, bad); err == nil {
			t.Errorf("expected error for %q", bad)
		}
	}
}

// The fleet-wide scope must stay metrics + dashboards — never silently widen to
// alerting/incident/admin write surfaces on a shared instance.
func TestGrafanaEnabledToolsScope(t *testing.T) {
	on := strings.Split(grafanaEnabledTools, ",")
	has := func(c string) bool {
		for _, x := range on {
			if x == c {
				return true
			}
		}
		return false
	}
	for _, want := range []string{"dashboard", "folder", "prometheus", "loki", "datasource"} {
		if !has(want) {
			t.Errorf("expected %q enabled for metrics+dashboards", want)
		}
	}
	for _, no := range []string{"alerting", "incident", "oncall", "admin", "provisioning"} {
		if has(no) {
			t.Errorf("category %q should NOT be enabled fleet-wide", no)
		}
	}
}

func TestWriteMCPConfig(t *testing.T) {
	dir := t.TempDir()
	servers := map[string]any{
		"discourse": map[string]any{"command": "npx"},
		"grafana":   map[string]any{"command": "/x/mcp-grafana"},
	}
	p, err := writeMCPConfig(dir, servers)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if p != filepath.Join(dir, "mcp.json") {
		t.Errorf("path = %q", p)
	}
	var cfg struct {
		MCPServers map[string]map[string]any `json:"mcpServers"`
	}
	b, _ := os.ReadFile(p)
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, ok := cfg.MCPServers["discourse"]; !ok {
		t.Errorf("discourse missing: %s", b)
	}
	if _, ok := cfg.MCPServers["grafana"]; !ok {
		t.Errorf("grafana missing: %s", b)
	}
}
