package spawn

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/clouvet/sprite-agent/internal/config"
)

// apiSpawner creates sprites via the sprites REST API (POST /v1/sprites, Bearer
// auth with the full token). The endpoint, auth, and create body (name required;
// env + labels accepted) were verified against the live cl-sprites API. The base
// URL is overridable via SPRITE_API_BASE.
//
// What this does NOT do: provision the sprite-agent artifact onto the new sprite.
// A bare create yields a base-environment sprite; making it boot sprite-agent and
// register into the brain needs a follow-up provisioning step (push/build the
// binary + run it as a service) — see BUILD_REPORT.
type apiSpawner struct {
	cfg    config.Config
	token  tokenParts
	base   string
	client *http.Client
	newID  func() string // short unique suffix for synthesized sprite names
}

// tokenParts is the SPRITE_API_TOKEN shape: org-slug/org-id/token-id/token-value.
type tokenParts struct {
	OrgSlug    string
	OrgID      string
	TokenID    string
	TokenValue string
}

func parseToken(raw string) (tokenParts, error) {
	parts := strings.Split(strings.TrimSpace(raw), "/")
	if len(parts) != 4 || parts[0] == "" || parts[1] == "" || parts[2] == "" || parts[3] == "" {
		return tokenParts{}, fmt.Errorf("spawn: malformed SPRITE_API_TOKEN (want org-slug/org-id/token-id/token-value)")
	}
	return tokenParts{OrgSlug: parts[0], OrgID: parts[1], TokenID: parts[2], TokenValue: parts[3]}, nil
}

func newAPISpawner(cfg config.Config) Spawner {
	base := os.Getenv("SPRITE_API_BASE")
	if base == "" {
		base = "https://api.sprites.dev"
	}
	tp, err := parseToken(cfg.SpriteAPIToken)
	if err != nil {
		// A malformed token means we cannot spawn; degrade to the stub so the
		// failure is explicit rather than a confusing runtime panic.
		return notConfigured{}
	}
	return &apiSpawner{
		cfg:    cfg,
		token:  tp,
		base:   strings.TrimRight(base, "/"),
		client: &http.Client{Timeout: 60 * time.Second},
		newID:  randomSuffix,
	}
}

// randomSuffix returns 8 hex chars for synthesizing unique sprite names.
func randomSuffix() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "00000000"
	}
	return hex.EncodeToString(b[:])
}

func (a *apiSpawner) Available() bool { return true }

// createSpriteRequest is the JSON body for POST /v1/sprites. Verified against
// the live API (cl-sprites): name is required; env + labels are accepted. The
// sprite name carries the restricted-token prefix, so name_prefix/org_id are not
// part of the body.
type createSpriteRequest struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels,omitempty"`
	Env    map[string]string `json:"env,omitempty"`
}

// spriteName resolves the explicit name, or synthesizes <prefix><id> using the
// new agent id so the restricted-token name_prefix cap is honored.
func spriteName(req Request, newID string) string {
	if req.Name != "" {
		return req.Name
	}
	return req.NamePrefix + newID
}

// buildCreateRequest assembles the create-sprite payload, including the
// bootstrap env that points the new sprite at the same brain + artifact. The
// sprite's name is also its SPRITE_AGENT_ID so it registers under that id.
func (a *apiSpawner) buildCreateRequest(req Request) createSpriteRequest {
	role := req.Role
	if role == "" {
		role = "worker"
	}
	labels := map[string]string{"fleet": "sprite-agent", "role": role}
	for k, v := range req.Labels {
		labels[k] = v
	}
	name := spriteName(req, a.newID())
	return createSpriteRequest{
		Name:   name,
		Labels: labels,
		Env:    BootstrapEnv(a.cfg, name, role),
	}
}

func (a *apiSpawner) Spawn(ctx context.Context, req Request) (Result, error) {
	body, err := json.Marshal(a.buildCreateRequest(req))
	if err != nil {
		return Result{}, err
	}
	url := a.base + "/v1/sprites"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return Result{}, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+a.cfg.SpriteAPIToken)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return Result{}, fmt.Errorf("spawn: create sprite: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return Result{}, fmt.Errorf("spawn: sprites API %d: %s", resp.StatusCode, string(data))
	}
	var out Result
	if err := json.Unmarshal(data, &out); err != nil {
		return Result{}, fmt.Errorf("spawn: decode response: %w", err)
	}
	return out, nil
}
