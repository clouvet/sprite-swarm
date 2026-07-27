package main

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
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

// grafanaConfig is the brain secret shape ({url, service_account_token}). token
// is accepted as an alias so either key works.
type grafanaConfig struct {
	URL   string `json:"url"`
	Token string `json:"service_account_token"`
	Alt   string `json:"token"`
}

// setupGrafanaMCP materializes the optional Grafana MCP server from a brain
// secret. It writes the service-account token to a 0600 file (so it stays out of
// the process args AND out of the 0644 mcp.json), ensures the mcp-grafana binary
// is present, and returns the server name + mcp.json entry to compose. Optional
// by construction: only fleets with a `grafana` secret get the server.
func setupGrafanaMCP(baseDir, secret string) (string, map[string]any, error) {
	var cfg grafanaConfig
	if err := json.Unmarshal([]byte(strings.TrimSpace(secret)), &cfg); err != nil {
		return "", nil, fmt.Errorf("parse grafana secret: %w", err)
	}
	url := strings.TrimRight(strings.TrimSpace(cfg.URL), "/")
	token := strings.TrimSpace(cfg.Token)
	if token == "" {
		token = strings.TrimSpace(cfg.Alt)
	}
	if url == "" || token == "" {
		return "", nil, fmt.Errorf("grafana secret needs both url and service_account_token")
	}

	tokenPath, err := writeGrafanaToken(baseDir, token)
	if err != nil {
		return "", nil, err
	}

	binPath, err := ensureMCPGrafanaBinary(baseDir)
	if err != nil {
		return "", nil, fmt.Errorf("provision mcp-grafana: %w", err)
	}

	// Token via _FILE (not the value) keeps it in the 0600 file only; mcp.json and
	// the process argv carry just the URL, the file path, and the tool allowlist.
	entry := map[string]any{
		"command": binPath,
		"args":    []string{"-enabled-tools", grafanaEnabledTools},
		"env": map[string]any{
			"GRAFANA_URL":                        url,
			"GRAFANA_SERVICE_ACCOUNT_TOKEN_FILE": tokenPath,
		},
	}
	log.Printf("secrets: loaded grafana config from brain (mcp %s, metrics+dashboards, %s)", grafanaMCPVersion, url)
	return "grafana", entry, nil
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
