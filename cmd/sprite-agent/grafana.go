package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/clouvet/sprite-swarm/internal/gateway"
)

// grafanaMCPVersion pins the mcp-grafana release the fleet runs. Bump it to adopt
// a newer server; the binary is fetched once per sprite disk and cached.
const grafanaMCPVersion = "v0.17.2"

// grafanaEnabledTools scopes the fleet to metrics + dashboards: query datasources
// (Prometheus/Loki), search, and build/edit dashboards (folder + dashboard, write
// ON). The heavier write surfaces on a shared instance — alerting, incident,
// oncall, admin, provisioning — are intentionally left off. Widen this list to
// grant more; it's an allowlist over mcp-grafana's tool categories.
const grafanaEnabledTools = "search,datasource,prometheus,loki,dashboard,folder,navigation,annotations,rendering"

// grafanaConfig is the brain secret shape. url is required; the token is OPTIONAL
// — preferred path is a custom_api gateway connector fronting url (token lives in
// the connector, never on the sprite). service_account_token / token remain as a
// fallback for fleets without a connector.
type grafanaConfig struct {
	URL   string `json:"url"`
	Token string `json:"service_account_token"`
	Alt   string `json:"token"`
}

// parseGrafanaSecret pulls the (required) url and (optional) token from the brain
// secret. Split out from setupGrafanaMCP so it's unit-testable without network.
func parseGrafanaSecret(secret string) (url, token string, err error) {
	var cfg grafanaConfig
	if err := json.Unmarshal([]byte(strings.TrimSpace(secret)), &cfg); err != nil {
		return "", "", fmt.Errorf("parse grafana secret: %w", err)
	}
	url = strings.TrimRight(strings.TrimSpace(cfg.URL), "/")
	token = strings.TrimSpace(cfg.Token)
	if token == "" {
		token = strings.TrimSpace(cfg.Alt)
	}
	if url == "" {
		return "", "", fmt.Errorf("grafana secret needs a url")
	}
	return url, token, nil
}

// grafanaServerEntry builds the mcp.json entry. In connector mode grafanaURL is
// the gateway base and tokenPath is "" — no credential rides on the sprite at all
// (the gateway injects it by identity). In fallback mode grafanaURL is the direct
// Grafana URL and tokenPath points at the 0600 token file
// (GRAFANA_SERVICE_ACCOUNT_TOKEN_FILE keeps the value out of argv and mcp.json).
func grafanaServerEntry(binPath, grafanaURL, tokenPath string) map[string]any {
	env := map[string]any{"GRAFANA_URL": grafanaURL}
	if tokenPath != "" {
		env["GRAFANA_SERVICE_ACCOUNT_TOKEN_FILE"] = tokenPath
	}
	return map[string]any{
		"command": binPath,
		"args":    []string{"-enabled-tools", grafanaEnabledTools},
		"env":     env,
	}
}

// setupGrafanaMCP wires the optional Grafana MCP server from a brain secret.
// Preferred: a custom_api gateway connector fronting the secret's url — the fleet
// reaches Grafana by sprite identity and NO token touches the sprite. Fallback:
// if no connector matches but the secret carries a token, it's written to a 0600
// file and passed via GRAFANA_SERVICE_ACCOUNT_TOKEN_FILE. Either way it ensures
// the mcp-grafana binary and returns the server name + mcp.json entry to compose.
func setupGrafanaMCP(ctx context.Context, baseDir, secret string) (string, map[string]any, error) {
	url, token, err := parseGrafanaSecret(secret)
	if err != nil {
		return "", nil, err
	}

	binPath, err := ensureMCPGrafanaBinary(baseDir)
	if err != nil {
		return "", nil, fmt.Errorf("provision mcp-grafana: %w", err)
	}

	// Prefer the gateway connector (token-free by sprite identity).
	if base := gateway.CustomAPIBaseFor(ctx, url); base != "" {
		// Drop any token a prior secret-mode boot left on disk — connector mode
		// keeps no credential on the sprite.
		_ = os.Remove(filepath.Join(baseDir, "grafana-token"))
		log.Printf("secrets: grafana via gateway connector (token-free, mcp %s, metrics+dashboards, %s)", grafanaMCPVersion, url)
		return "grafana", grafanaServerEntry(binPath, base, ""), nil
	}

	// Fallback: token on the sprite (0600 file). Requires a token in the secret.
	if token == "" {
		return "", nil, fmt.Errorf("grafana: no custom_api connector fronting %s and no token in the secret", url)
	}
	tokenPath, err := writeGrafanaToken(baseDir, token)
	if err != nil {
		return "", nil, err
	}
	log.Printf("secrets: grafana via on-disk token (no connector found; mcp %s, metrics+dashboards, %s)", grafanaMCPVersion, url)
	return "grafana", grafanaServerEntry(binPath, url, tokenPath), nil
}

// writeGrafanaToken persists the service-account token to a 0600 file that
// mcp-grafana reads via GRAFANA_SERVICE_ACCOUNT_TOKEN_FILE. Also used by
// reload-secrets to refresh a rotated token for subsequent server launches.
func writeGrafanaToken(baseDir, token string) (string, error) {
	tokenPath := filepath.Join(baseDir, "grafana-token")
	if err := os.WriteFile(tokenPath, []byte(token), 0o600); err != nil {
		return "", fmt.Errorf("write grafana token: %w", err)
	}
	return tokenPath, nil
}

// ensureMCPGrafanaBinary returns the path to the pinned mcp-grafana binary,
// downloading + extracting the release tarball once per sprite disk if absent.
// The binary is cached under <baseDir>/bin, so warm boots (suspend/resume) and
// re-execs skip the fetch; only a cold sprite pays it, once.
func ensureMCPGrafanaBinary(baseDir string) (string, error) {
	binDir := filepath.Join(baseDir, "bin")
	binPath := filepath.Join(binDir, "mcp-grafana")
	if fi, err := os.Stat(binPath); err == nil && fi.Mode()&0o111 != 0 {
		return binPath, nil // already installed
	}
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return "", err
	}

	url := fmt.Sprintf("https://github.com/grafana/mcp-grafana/releases/download/%s/mcp-grafana_Linux_x86_64.tar.gz", grafanaMCPVersion)
	log.Printf("grafana: fetching mcp-grafana %s …", grafanaMCPVersion)
	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download %s: http %d", url, resp.StatusCode)
	}
	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return "", err
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return "", fmt.Errorf("mcp-grafana binary not found in release tarball")
		}
		if err != nil {
			return "", err
		}
		if filepath.Base(hdr.Name) != "mcp-grafana" || hdr.Typeflag != tar.TypeReg {
			continue
		}
		// Write to a temp path then rename in, so a partial download never leaves a
		// broken binary that a later boot would treat as "installed".
		tmp := binPath + ".tmp"
		f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
		if err != nil {
			return "", err
		}
		if _, err := io.Copy(f, tr); err != nil {
			f.Close()
			os.Remove(tmp)
			return "", err
		}
		f.Close()
		if err := os.Rename(tmp, binPath); err != nil {
			os.Remove(tmp)
			return "", err
		}
		log.Printf("grafana: installed mcp-grafana %s at %s", grafanaMCPVersion, binPath)
		return binPath, nil
	}
}
