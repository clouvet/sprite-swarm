// Package gateway discovers Sprite API Gateway connectors. Sprites never hold
// provider tokens; they route through https://api.sprites.dev/v1/gateway/<provider>/<id>/...
// and the gateway attaches the stored credential, authenticating the caller by
// its Fly sprite identity. See https://docs.sprites.dev/concepts/connectors/.
package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"
)

// DefaultBase is the gateway/API host; override with SPRITE_API_BASE.
func apiBase() string {
	if b := os.Getenv("SPRITE_API_BASE"); b != "" {
		return b
	}
	return "https://api.sprites.dev"
}

// Connection is one configured connector.
type Connection struct {
	Provider    string `json:"provider"`
	ID          string `json:"id"`
	GatewayBase string `json:"gateway_base_url"`
}

type listResponse struct {
	Connections []Connection `json:"connections"`
}

// list fetches the raw connector list (auth is by sprite identity, so this only
// works when called from a sprite).
func list(ctx context.Context) ([]Connection, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBase()+"/v1/gateway/list", nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gateway list: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("gateway list: status %d", resp.StatusCode)
	}
	var lr listResponse
	if err := json.NewDecoder(resp.Body).Decode(&lr); err != nil {
		return nil, err
	}
	return lr.Connections, nil
}

// Discover lists connectors available to this sprite. Returns provider -> connection.
func Discover(ctx context.Context) (map[string]Connection, error) {
	conns, err := list(ctx)
	if err != nil {
		return nil, err
	}
	byProvider := make(map[string]Connection, len(conns))
	for _, c := range conns {
		// First wins; the gateway lists one connection per provider here.
		if _, ok := byProvider[c.Provider]; !ok {
			byProvider[c.Provider] = c
		}
	}
	return byProvider, nil
}

// AnthropicBaseURL returns the Anthropic connector's gateway base URL (the value
// for ANTHROPIC_BASE_URL), or "" if no Anthropic connector is available.
func AnthropicBaseURL(ctx context.Context) string {
	conns, err := Discover(ctx)
	if err != nil {
		return ""
	}
	return conns["anthropic"].GatewayBase
}

// SpritesAPIBase returns the gateway base URL of a custom_api connector fronting
// the Sprites API: the one whose id == pinID if pinID is set, else the first
// custom_api connector. "" if none. This lets a sprite route spawn/reap through
// the gateway (identity-authed) instead of holding the sprites token. custom_api
// is generic, so pinning by id avoids grabbing an unrelated custom_api connector.
func SpritesAPIBase(ctx context.Context, pinID string) string {
	conns, err := list(ctx)
	if err != nil {
		return ""
	}
	var first string
	for _, c := range conns {
		if c.Provider != "custom_api" {
			continue
		}
		if pinID != "" {
			if c.ID == pinID {
				return c.GatewayBase
			}
			continue
		}
		if first == "" {
			first = c.GatewayBase
		}
	}
	return first
}
