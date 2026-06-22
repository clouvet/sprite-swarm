// Package config loads sprite-agent configuration from the environment.
//
// Everything is env-driven so the same binary runs unchanged on any sprite; a
// spawner hands a new sprite its config (notably the brain pointer) at spawn
// time. Nothing here is secret-bearing beyond what the platform injects.
package config

import (
	"os"
	"path/filepath"
	"strings"
)

// Config is the resolved runtime configuration for one sprite-agent instance.
type Config struct {
	// HTTP listen address for the session service + web UI.
	Addr string

	// AgentID is this sprite's stable identity in the fleet. Defaults to the
	// hostname; a spawner may override it.
	AgentID string

	// WorkDir is the working directory the Claude process runs in. Its Claude
	// "projects" transcript directory is derived from it.
	WorkDir string

	// ClaudeProjectsDir is where Claude writes <session-id>.jsonl transcripts,
	// derived from WorkDir (Claude slugifies the cwd path).
	ClaudeProjectsDir string

	// Claude CLI driving options (DESIGN §3.1): scoped permissions, not a
	// blanket skip. PermissionMode maps to --permission-mode; SettingsPath and
	// MCPConfigPath map to --settings / --mcp-config when set.
	PermissionMode string
	SettingsPath   string
	MCPConfigPath  string

	// Brain (S3/Tigris) pointer. When BrainBucket is empty the brain is
	// disabled and the agent runs solo (still fully functional for chat).
	Brain BrainConfig

	// Spawn (sprites API) token. When empty, spawning is stubbed.
	SpriteAPIToken string

	// ArtifactRef is the bootstrap pointer handed to spawned sprites so they
	// run this same artifact (e.g. a git ref or image). Informational in
	// Phase 1; recorded into spawn requests.
	ArtifactRef string
}

// BrainConfig points at the shared fleet brain (S3-compatible, e.g. Tigris).
type BrainConfig struct {
	Bucket    string
	Region    string
	Endpoint  string
	AccessKey string
	SecretKey string
}

// Enabled reports whether a brain is configured.
func (b BrainConfig) Enabled() bool { return b.Bucket != "" }

// FromEnv builds a Config from environment variables, applying documented
// defaults (DECISIONS.md) for anything unset.
func FromEnv() Config {
	workDir := getenv("SPRITE_AGENT_WORKDIR", "/home/sprite")

	c := Config{
		Addr:           getenv("SPRITE_AGENT_ADDR", ":8080"),
		AgentID:        getenv("SPRITE_AGENT_ID", hostname()),
		WorkDir:        workDir,
		PermissionMode: getenv("SPRITE_AGENT_PERMISSION_MODE", "acceptEdits"),
		SettingsPath:   os.Getenv("SPRITE_AGENT_SETTINGS"),
		MCPConfigPath:  os.Getenv("SPRITE_AGENT_MCP_CONFIG"),
		SpriteAPIToken: os.Getenv("SPRITE_API_TOKEN"),
		ArtifactRef:    getenv("SPRITE_AGENT_ARTIFACT", "github.com/clouvet/sprite-agent@main"),
		Brain: BrainConfig{
			Bucket:    os.Getenv("S3_BUCKET"),
			Region:    getenv("S3_REGION", "auto"),
			Endpoint:  os.Getenv("S3_ENDPOINT"),
			AccessKey: os.Getenv("S3_ACCESS_KEY"),
			SecretKey: os.Getenv("S3_SECRET_KEY"),
		},
	}
	c.ClaudeProjectsDir = deriveProjectsDir(workDir)
	return c
}

// deriveProjectsDir mirrors how Claude Code names its per-project transcript
// directory: the absolute cwd with path separators replaced by dashes, under
// ~/.claude/projects. e.g. /home/sprite -> -home-sprite.
func deriveProjectsDir(workDir string) string {
	home := os.Getenv("HOME")
	if home == "" {
		home = "/home/sprite"
	}
	abs := workDir
	if !filepath.IsAbs(abs) {
		if a, err := filepath.Abs(abs); err == nil {
			abs = a
		}
	}
	slug := strings.ReplaceAll(abs, "/", "-")
	return filepath.Join(home, ".claude", "projects", slug)
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func hostname() string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "sprite-agent"
}
