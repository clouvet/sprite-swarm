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

// Discover lists connectors available to this sprite (auth is by sprite identity,
// so this only works when called from a sprite). Returns provider -> connection.
func Discover(ctx context.Context) (map[string]Connection, error) {
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
	byProvider := make(map[string]Connection, len(lr.Connections))
	for _, c := range lr.Connections {
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
