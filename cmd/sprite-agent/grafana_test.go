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

func TestParseGrafanaSecret(t *testing.T) {
	// url + canonical token, and the `token` alias; trailing slash trimmed.
	for _, s := range []string{
		`{"url":"https://g.example.net","service_account_token":"glsa_x"}`,
		`{"url":"https://g.example.net/","token":"glsa_x"}`,
	} {
		url, tok, err := parseGrafanaSecret(s)
		if err != nil {
			t.Fatalf("parse %s: %v", s, err)
		}
		if url != "https://g.example.net" || tok != "glsa_x" {
			t.Errorf("parsed url=%q token=%q from %s", url, tok, s)
		}
	}

	// url is required; token is now OPTIONAL (connector mode carries no token).
	if url, tok, err := parseGrafanaSecret(`{"url":"https://g.example.net"}`); err != nil || url == "" || tok != "" {
		t.Errorf("url-only should parse token-free: url=%q tok=%q err=%v", url, tok, err)
	}
	for _, bad := range []string{`{"service_account_token":"glsa_x"}`, `{}`, `not json`, ``} {
		if _, _, err := parseGrafanaSecret(bad); err == nil {
			t.Errorf("expected error for %q", bad)
		}
	}
}

func TestGrafanaServerEntry(t *testing.T) {
	// Connector mode: gateway base as URL, and NO token env on the sprite.
	conn := grafanaServerEntry("/x/mcp-grafana", "https://api.sprites.dev/v1/gateway/custom_api/abc", "")
	env := conn["env"].(map[string]any)
	if env["GRAFANA_URL"] != "https://api.sprites.dev/v1/gateway/custom_api/abc" {
		t.Errorf("connector URL = %v", env["GRAFANA_URL"])
	}
	if _, ok := env["GRAFANA_SERVICE_ACCOUNT_TOKEN_FILE"]; ok {
		t.Error("connector mode must not set a token env")
	}

	// Fallback mode: direct URL + token file.
	fb := grafanaServerEntry("/x/mcp-grafana", "https://g.example.net", "/d/grafana-token")
	env = fb["env"].(map[string]any)
	if env["GRAFANA_URL"] != "https://g.example.net" || env["GRAFANA_SERVICE_ACCOUNT_TOKEN_FILE"] != "/d/grafana-token" {
		t.Errorf("fallback env = %v", env)
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
