package config

import (
	_ "embed"
	"os"
	"path/filepath"
)

// defaultClaudeSettings is the baked-in scoped Claude settings: an allow-list
// covering the GitHub workflow (git, gh) plus common dev/file tools, and a
// deny-list for catastrophic commands. This is how the agent's tool/shell
// powers are scoped (DESIGN §6.2) without resorting to a blanket
// --dangerously-skip-permissions: in headless --print mode, only allow-listed
// Bash commands run without an interactive approver.
//
//go:embed default-claude-settings.json
var defaultClaudeSettings []byte

// ResolveSettingsPath returns the path to pass to claude's --settings.
//
// If SPRITE_AGENT_SETTINGS is set, it wins (operator override). Otherwise the
// embedded default is materialized to <workDir>/.sprite-agent/claude-settings.json
// so the GitHub capability is on by default and deploy-layout-independent.
func ResolveSettingsPath(workDir, override string) (string, error) {
	if override != "" {
		return override, nil
	}
	dir := filepath.Join(workDir, ".sprite-agent")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "claude-settings.json")
	if err := os.WriteFile(path, defaultClaudeSettings, 0o644); err != nil {
		return "", err
	}
	return path, nil
}
