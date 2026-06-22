package spawn

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/clouvet/sprite-agent/internal/config"
)

// apiSpawner creates sprites via the sprites REST API.
//
// NOTE (build brief / DESIGN §10): the token + request assembly below are real
// and unit-tested. The live HTTP call is the seam that could not be verified at
// build time (no SPRITE_API_TOKEN was present), so the exact endpoint/payload
// may need confirming against the sprites API at first use. The base URL is
// overridable via SPRITE_API_BASE.
type apiSpawner struct {
	cfg    config.Config
	token  tokenParts
	base   string
	client *http.Client
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
	}
}

func (a *apiSpawner) Available() bool { return true }

// createSpriteRequest is the JSON body assembled for the sprites API. Kept as a
// named type so it is constructed/serialized identically in tests.
type createSpriteRequest struct {
	Name       string            `json:"name,omitempty"`
	NamePrefix string            `json:"name_prefix,omitempty"`
	OrgID      string            `json:"org_id"`
	Labels     map[string]string `json:"labels,omitempty"`
	Env        map[string]string `json:"env"`
}

// buildCreateRequest assembles the create-sprite payload, including the
// bootstrap env that points the new sprite at the same brain + artifact.
func (a *apiSpawner) buildCreateRequest(req Request) createSpriteRequest {
	role := req.Role
	if role == "" {
		role = "worker"
	}
	labels := map[string]string{"fleet": "sprite-agent", "role": role}
	for k, v := range req.Labels {
		labels[k] = v
	}
	newID := req.Name
	if newID == "" {
		newID = req.NamePrefix + "spawned"
	}
	return createSpriteRequest{
		Name:       req.Name,
		NamePrefix: req.NamePrefix,
		OrgID:      a.token.OrgID,
		Labels:     labels,
		Env:        BootstrapEnv(a.cfg, newID, role),
	}
}

func (a *apiSpawner) Spawn(ctx context.Context, req Request) (Result, error) {
	body, err := json.Marshal(a.buildCreateRequest(req))
	if err != nil {
		return Result{}, err
	}
	url := fmt.Sprintf("%s/v1/orgs/%s/sprites", a.base, a.token.OrgID)
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
