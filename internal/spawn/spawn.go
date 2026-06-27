// Package spawn is the sprite-management capability: an agent can create another
// sprite running this same artifact, handing it the bootstrap pointer so the new
// agent registers into the same fleet brain (DESIGN §4.2, §8 Phase 1).
//
// Spawning is one sprites-API call; it does not require coordination (that's
// Phase 2). When SPRITE_API_TOKEN is absent the capability is built behind this
// interface but the live call is stubbed (NotConfigured), per the build brief —
// the request/bootstrap assembly below stays exercised by unit tests.
package spawn

import (
	"context"
	"errors"
	"fmt"

	"github.com/clouvet/sprite-agent/internal/config"
)

// ErrNotConfigured means no sprites API token is available to spawn with.
var ErrNotConfigured = errors.New("spawn: sprites API token not configured (set SPRITE_API_TOKEN)")

// Request describes a sprite to create.
type Request struct {
	Name       string            // explicit name; if empty the API assigns one under NamePrefix
	NamePrefix string            // restricted-token prefix (e.g. "wk-")
	Role       string            // role the new agent advertises ("worker" | "home")
	Labels     map[string]string // sprites-api labels (authoritative membership)
}

// Result identifies a spawned sprite.
type Result struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url"`
}

// Spawner creates and destroys sprites. Available reports whether live spawning
// is wired (a sprites API token is present).
type Spawner interface {
	Available() bool
	Spawn(ctx context.Context, req Request) (Result, error)
	Destroy(ctx context.Context, name string) error
	Exists(ctx context.Context, name string) (bool, error)
	DeployApp(ctx context.Context, req DeployRequest) (Result, error)
}

// DeployRequest hosts a user app on a dedicated BARE sprite (no agent), so the
// app owns the sprite's http port / public URL (an agent sprite already owns it).
// The new sprite fetches the app tarball from ArtifactURL (a brain URL the worker
// staged, reachable token-free by sprite identity) and runs Run on HTTPPort.
type DeployRequest struct {
	NamePrefix  string
	ArtifactURL string
	Run         string
	HTTPPort    int
}

// New returns a live spawner when a sprites token is set OR a gateway connector
// fronting the Sprites API is configured; otherwise a stub that returns
// ErrNotConfigured (the capability is present, the live call is not).
func New(cfg config.Config) Spawner {
	if cfg.SpriteAPIToken == "" && cfg.SpriteAPIGateway == "" {
		return notConfigured{}
	}
	return newAPISpawner(cfg)
}

// LaunchHome stands up a brand-new fleet's home sprite (used by `sprite-agent
// init`): create the sprite, stage the given linux/amd64 artifact in the brain,
// and provision the sprite-agent service to run as home. cfg carries the Sprites
// token + the brain pointer (direct S3 keys when igniting from off-account). The
// home self-discovers the Anthropic + s3 connectors at runtime. Returns the
// created sprite (name + URL).
func LaunchHome(ctx context.Context, cfg config.Config, artifactPath, name string) (Result, error) {
	sp, ok := newAPISpawner(cfg).(*apiSpawner)
	if !ok {
		return Result{}, errors.New("spawn: a valid Sprites API token is required to launch a fleet")
	}
	res, err := sp.createSprite(ctx, sp.buildCreateRequest(Request{Name: name, Role: "home"}))
	if err != nil {
		return Result{}, err
	}
	url, err := stageFile(ctx, cfg.Brain, artifactPath, artifactKey, artifactTTL)
	if err != nil {
		return res, fmt.Errorf("spawn: stage artifact: %w", err)
	}
	env := map[string]string{"SPRITE_AGENT_ROLE": "home"}
	if res.URL != "" {
		env["SPRITE_AGENT_URL"] = res.URL
	}
	if err := sp.provisionAgent(ctx, res.Name, env, url); err != nil {
		return res, fmt.Errorf("spawn: provision home: %w", err)
	}
	return res, nil
}

// BootstrapEnv is the environment a spawned sprite is handed so it can rehydrate
// itself from the brain on boot (DESIGN §4.2: the brain pointer is the only
// out-of-brain input; everything else rehydrates). Pure and unit-tested.
func BootstrapEnv(cfg config.Config, newID, role string) map[string]string {
	env := map[string]string{
		"SPRITE_AGENT_ID":       newID,
		"SPRITE_AGENT_ROLE":     role,
		"SPRITE_AGENT_ARTIFACT": cfg.ArtifactRef,
	}
	// Brain pointer. Prefer the gateway connector — the worker reaches the brain
	// by its own sprite identity, so NO S3 keys are copied onto it (token-free,
	// symmetric). Only fall back to copying keys if there's no connector.
	switch {
	case cfg.Brain.UsesGateway():
		env["SPRITE_AGENT_BRAIN_GATEWAY"] = cfg.Brain.GatewayURL
	case cfg.Brain.Enabled():
		env["S3_BUCKET"] = cfg.Brain.Bucket
		env["S3_REGION"] = cfg.Brain.Region
		env["S3_ENDPOINT"] = cfg.Brain.Endpoint
		env["S3_ACCESS_KEY"] = cfg.Brain.AccessKey
		env["S3_SECRET_KEY"] = cfg.Brain.SecretKey
	}
	return env
}
