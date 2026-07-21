package spawn

import (
	"context"
	"errors"
	"testing"

	"github.com/clouvet/sprite-swarm/internal/config"
)

func testConfig() config.Config {
	return config.Config{
		AgentID:     "agent-a",
		ArtifactRef: "github.com/clouvet/sprite-swarm@main",
		Brain: config.BrainConfig{
			Bucket: "sprite-agent", Region: "auto",
			Endpoint: "https://fly.storage.tigris.dev",
			AccessKey: "AK", SecretKey: "SK",
		},
	}
}

func TestNewReturnsStubWithoutToken(t *testing.T) {
	s := New(testConfig()) // no SpriteAPIToken
	if s.Available() {
		t.Fatal("expected stub spawner (Available=false) without a token")
	}
	_, err := s.Spawn(context.Background(), Request{})
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("expected ErrNotConfigured, got %v", err)
	}
}

func TestNewReturnsLiveWithToken(t *testing.T) {
	cfg := testConfig()
	cfg.SpriteAPIToken = "myorg/org_123/tok_456/secretvalue"
	s := New(cfg)
	if !s.Available() {
		t.Fatal("expected live spawner with a valid token")
	}
}

func TestBootstrapEnvHandsBrainPointer(t *testing.T) {
	env := BootstrapEnv(testConfig(), "wk-1")
	want := map[string]string{
		"SPRITE_AGENT_ID":       "wk-1",
		"SPRITE_AGENT_ARTIFACT": "github.com/clouvet/sprite-swarm@main",
		"S3_BUCKET":             "sprite-agent",
		"S3_ENDPOINT":           "https://fly.storage.tigris.dev",
		"S3_ACCESS_KEY":         "AK",
		"S3_SECRET_KEY":         "SK",
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("BootstrapEnv[%q] = %q, want %q", k, env[k], v)
		}
	}
}

func TestBootstrapEnvNoBrain(t *testing.T) {
	cfg := testConfig()
	cfg.Brain = config.BrainConfig{}
	env := BootstrapEnv(cfg, "wk-1")
	if _, ok := env["S3_BUCKET"]; ok {
		t.Error("expected no S3 keys when brain disabled")
	}
	if env["SPRITE_AGENT_ID"] != "wk-1" {
		t.Error("agent id should still be set")
	}
}

func TestBootstrapEnvGatewayHidesKeys(t *testing.T) {
	cfg := testConfig()
	cfg.Brain.GatewayURL = "https://api.sprites.dev/v1/gateway/s3_object_store/abc"
	env := BootstrapEnv(cfg, "wk-1")
	if env["SPRITE_AGENT_BRAIN_GATEWAY"] != cfg.Brain.GatewayURL {
		t.Errorf("gateway not passed: %q", env["SPRITE_AGENT_BRAIN_GATEWAY"])
	}
	for _, k := range []string{"S3_ACCESS_KEY", "S3_SECRET_KEY", "S3_BUCKET"} {
		if _, ok := env[k]; ok {
			t.Errorf("gateway mode must not copy %s", k)
		}
	}
}

func TestBootstrapEnvBootstrapGatewayOverridesKeys(t *testing.T) {
	// init's case: this host has raw keys (to prime the brain), but the fleet it
	// ignites should run token-free — BootstrapGateway wins, keys are not copied.
	cfg := testConfig() // has AccessKey/SecretKey set
	cfg.Brain.BootstrapGateway = "https://api.sprites.dev/v1/gateway/s3_object_store/xyz"
	env := BootstrapEnv(cfg, "home")
	if env["SPRITE_AGENT_BRAIN_GATEWAY"] != cfg.Brain.BootstrapGateway {
		t.Errorf("bootstrap gateway not passed: %q", env["SPRITE_AGENT_BRAIN_GATEWAY"])
	}
	if _, ok := env["S3_ACCESS_KEY"]; ok {
		t.Error("BootstrapGateway must suppress copying S3 keys onto the sprite")
	}
}

func TestParseToken(t *testing.T) {
	tp, err := parseToken("myorg/org_123/tok_456/secretvalue")
	if err != nil {
		t.Fatal(err)
	}
	if tp.OrgSlug != "myorg" || tp.OrgID != "org_123" || tp.TokenID != "tok_456" || tp.TokenValue != "secretvalue" {
		t.Fatalf("bad parse: %+v", tp)
	}
	for _, bad := range []string{"", "a/b/c", "a//c/d", "no-slashes", "a/b/c/"} {
		if _, err := parseToken(bad); err == nil {
			t.Errorf("expected error for %q", bad)
		}
	}
}

func TestBuildCreateRequest(t *testing.T) {
	cfg := testConfig()
	cfg.SpriteAPIToken = "myorg/org_123/tok_456/secretvalue"
	a := newAPISpawner(cfg).(*apiSpawner)
	a.newID = func() string { return "abcd1234" } // deterministic

	cr := a.buildCreateRequest(Request{NamePrefix: "wk-"})
	// name carries the restricted-token prefix + synthesized id.
	if cr.Name != "wk-abcd1234" {
		t.Errorf("Name = %q, want wk-abcd1234", cr.Name)
	}
	if cr.Labels["fleet"] != "sprite-agent" {
		t.Errorf("labels = %v", cr.Labels)
	}
	if _, ok := cr.Labels["role"]; ok {
		t.Errorf("spawned sprites must not carry a role label: %v", cr.Labels)
	}
	if cr.Env["S3_BUCKET"] != "sprite-agent" || cr.Env["SPRITE_AGENT_ARTIFACT"] == "" {
		t.Errorf("bootstrap env missing brain/artifact: %v", cr.Env)
	}
	// the new sprite registers under its own name as the agent id.
	if cr.Env["SPRITE_AGENT_ID"] != "wk-abcd1234" {
		t.Errorf("SPRITE_AGENT_ID = %q, want wk-abcd1234", cr.Env["SPRITE_AGENT_ID"])
	}
}

func TestSpriteNameExplicitWins(t *testing.T) {
	if got := spriteName(Request{Name: "fixed", NamePrefix: "wk-"}, "rand"); got != "fixed" {
		t.Errorf("spriteName = %q, want fixed", got)
	}
	if got := spriteName(Request{NamePrefix: "wk-"}, "rand"); got != "wk-rand" {
		t.Errorf("spriteName = %q, want wk-rand", got)
	}
}
