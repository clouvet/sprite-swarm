// Package config loads sprite-agent configuration from the environment.
//
// Everything is env-driven so the same binary runs unchanged on any sprite; a
// spawner hands a new sprite its config (notably the brain pointer) at spawn
// time. Nothing here is secret-bearing beyond what the platform injects.
package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ArtifactKey is the brain key where a sprite stages its own binary so peers can
// fetch it — at spawn time (a new sprite downloads it) and for self-update (a
// running sprite re-fetches it). Arch-tagged: the staged binary must match the
// target platform (same as the stager's in practice).
const ArtifactKey = "fleet/artifacts/sprite-agent-linux-amd64"

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

	// Claude CLI driving options. The fleet runs with --dangerously-skip-permissions
	// by default (DangerousSkip): every sprite is an identical isolated microVM
	// doing autonomous work and shouldn't stall on permission prompts. Set
	// SPRITE_AGENT_DANGEROUS_SKIP=0 to opt into the scoped PermissionMode instead.
	DangerousSkip  bool
	PermissionMode string
	SettingsPath   string
	MCPConfigPath  string

	// Brain (S3/Tigris) pointer. When BrainBucket is empty the brain is
	// disabled and the agent runs solo (still fully functional for chat).
	Brain BrainConfig

	// Spawn (sprites API) token. When empty, spawning falls back to the gateway
	// connector (SpriteAPIGateway) if available, else is stubbed.
	SpriteAPIToken string

	// SpriteAPIGateway is the gateway base URL of a custom_api connector fronting
	// the Sprites API. When set (and no token), spawn/reap route through it with
	// no token — the gateway injects the credential by sprite identity.
	SpriteAPIGateway string

	// SpriteAPIConnectorID optionally pins which custom_api connector to use for
	// the Sprites API (since custom_api is generic); empty = first one discovered.
	SpriteAPIConnectorID string

	// ArtifactRef is the bootstrap pointer handed to spawned sprites so they
	// run this same artifact (e.g. a git ref or image). Informational in
	// Phase 1; recorded into spawn requests.
	ArtifactRef string

	// PublicURL is this agent's externally reachable session-service URL,
	// advertised in the roster so a human can attach to it (P2.4). The spawner
	// passes it to workers (from the create response); home sets it via env.
	PublicURL string

	// Reaper cadence (on token-bearing agents). Workers are torn down only on
	// explicit teardown (POST /api/fleet/destroy, or a worker's own POST
	// /api/fleet/done); the reaper scans every ReapInterval and, separately, cleans
	// the brain entry of any worker whose heartbeat has been stale beyond
	// DeadReapAfter AND whose sprite is actually gone. There is no idle-based
	// auto-reaping — a suspended/idle worker is left alone.
	ReapInterval  time.Duration
	DeadReapAfter time.Duration
}

// BrainConfig points at the shared fleet brain (S3-compatible, e.g. Tigris).
type BrainConfig struct {
	Bucket    string
	Region    string
	Endpoint  string
	AccessKey string
	SecretKey string

	// GatewayURL, when set, reaches the brain through the Sprite API Gateway
	// s3_object_store connector (authed by sprite identity — no S3 keys). This is
	// the token-free path; it takes precedence over direct keys. May be discovered
	// at runtime, so it isn't always known at config time.
	GatewayURL string
}

// Enabled reports whether a brain is reachable — either via the gateway connector
// (no keys) or direct S3 credentials.
func (b BrainConfig) Enabled() bool { return b.GatewayURL != "" || b.Bucket != "" }

// UsesGateway reports whether the connector path is configured (preferred).
func (b BrainConfig) UsesGateway() bool { return b.GatewayURL != "" }

// FromEnv builds a Config from environment variables, applying documented
// defaults for anything unset.
func FromEnv() Config {
	workDir := getenv("SPRITE_AGENT_WORKDIR", "/home/sprite")

	c := Config{
		Addr:                getenv("SPRITE_AGENT_ADDR", ":8080"),
		AgentID:             getenv("SPRITE_AGENT_ID", hostname()),
		WorkDir:             workDir,
		DangerousSkip:       boolEnv("SPRITE_AGENT_DANGEROUS_SKIP", true),
		PermissionMode:      getenv("SPRITE_AGENT_PERMISSION_MODE", "acceptEdits"),
		SettingsPath:        os.Getenv("SPRITE_AGENT_SETTINGS"),
		MCPConfigPath:       os.Getenv("SPRITE_AGENT_MCP_CONFIG"),
		SpriteAPIToken:       os.Getenv("SPRITE_API_TOKEN"),
		SpriteAPIGateway:     os.Getenv("SPRITE_API_GATEWAY"),
		SpriteAPIConnectorID: os.Getenv("SPRITE_API_CONNECTOR_ID"),
		ArtifactRef:         getenv("SPRITE_AGENT_ARTIFACT", "github.com/clouvet/sprite-swarm@main"),
		PublicURL:           os.Getenv("SPRITE_AGENT_URL"),
		ReapInterval:        secondsEnv("SPRITE_AGENT_REAP_INTERVAL_SECONDS", 60),
		DeadReapAfter:       minutesEnv("SPRITE_AGENT_DEAD_REAP_MINUTES", 5),
		Brain: BrainConfig{
			Bucket:     os.Getenv("S3_BUCKET"),
			Region:     getenv("S3_REGION", "auto"),
			Endpoint:   os.Getenv("S3_ENDPOINT"),
			AccessKey:  os.Getenv("S3_ACCESS_KEY"),
			SecretKey:  os.Getenv("S3_SECRET_KEY"),
			GatewayURL: os.Getenv("SPRITE_AGENT_BRAIN_GATEWAY"),
		},
	}
	c.ClaudeProjectsDir = deriveProjectsDir(workDir)
	return c
}

// deriveProjectsDir mirrors how Claude Code names its per-project transcript
// directory: the absolute cwd slugified under ~/.claude/projects. Claude replaces
// every character that is not [a-zA-Z0-9-] with a dash — NOT just path separators.
// e.g. /home/sprite -> -home-sprite ; /home/sprite/.sa-home -> -home-sprite--sa-home
// (the leading "/." becomes "--"). Getting this wrong points history replay at an
// empty directory, so transcripts (and thus chat history on refresh) are lost.
// ProjectsDirFor returns the ~/.claude/projects transcript directory Claude uses
// for a given working directory. Exposed so per-session working dirs can locate
// their own transcripts for history replay.
func ProjectsDirFor(cwd string) string { return deriveProjectsDir(cwd) }

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
	return filepath.Join(home, ".claude", "projects", slugifyCwd(abs))
}

// slugifyCwd replaces every rune that is not alphanumeric or '-' with '-',
// matching Claude Code's transcript-directory naming.
func slugifyCwd(abs string) string {
	var b strings.Builder
	b.Grow(len(abs))
	for _, r := range abs {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// minutesEnv reads an integer-minutes env var, defaulting to def minutes.
func minutesEnv(key string, def int) time.Duration {
	return time.Duration(intEnv(key, def)) * time.Minute
}

// secondsEnv reads an integer-seconds env var, defaulting to def seconds.
func secondsEnv(key string, def int) time.Duration {
	return time.Duration(intEnv(key, def)) * time.Second
}

// boolEnv reads a boolean env var (1/true/yes = true, 0/false/no = false),
// defaulting to def when unset/unrecognized.
func boolEnv(key string, def bool) bool {
	switch strings.ToLower(os.Getenv(key)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return def
	}
}

func intEnv(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func hostname() string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "sprite-agent"
}
