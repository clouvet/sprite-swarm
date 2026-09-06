// Package pi wires the experimental Pi (pi.dev) multi-provider runtime: provider
// auth resolution (a brain-uploaded key wins, else a gateway connector), the
// ~/.pi/agent/models.json it needs, and the RPC↔Claude protocol translation used to
// keep the rest of sprite-agent (hub, UI, transcript) unchanged.
//
// This whole package lives ONLY on the experimental/pi-runtime branch. On main the
// runtime is always Claude Code; nothing here is reachable unless SPRITE_AGENT_RUNTIME=pi.
package pi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Provider describes a supported LLM provider and how Pi authenticates to it.
type Provider struct {
	Name       string // pi provider id ("openai", "anthropic", "google")
	EnvVar     string // env var Pi reads for the API key ("OPENAI_API_KEY")
	SecretName string // brain secret holding an uploaded key (fleet.GetSecret name)
	Default    string // a sensible default model id (overridable via SPRITE_AGENT_MODEL)
}

// Providers is the supported set. The env var names are the ones pi-ai reads for
// each provider's built-in definition; a brain-uploaded key is exported under that
// name so Pi's built-in provider picks it up with no models.json entry needed.
var Providers = []Provider{
	// Defaults are known-good ids for each provider's DEFAULT api type
	// (openai-completions / anthropic-messages / google-generative-ai). Override with
	// SPRITE_AGENT_MODEL. Note: some OpenAI models (e.g. gpt-5-codex) need the
	// Responses/Codex api type and won't work under plain openai-completions — pick a
	// chat-completions model here for a working default.
	{Name: "openai", EnvVar: "OPENAI_API_KEY", SecretName: "openai-api-key", Default: "gpt-6-astra"},
	{Name: "anthropic", EnvVar: "ANTHROPIC_API_KEY", SecretName: "anthropic-api-key", Default: "claude-opus-4-8"},
	{Name: "google", EnvVar: "GEMINI_API_KEY", SecretName: "google-api-key", Default: "gemini-2.5-pro"},
}

// ProviderByName returns the Provider with the given name, or false.
func ProviderByName(name string) (Provider, bool) {
	for _, p := range Providers {
		if p.Name == name {
			return p, true
		}
	}
	return Provider{}, false
}

// Auth is the resolved authentication for a provider.
type Auth struct {
	Provider  string
	Available bool
	ViaKey    bool              // true: a brain key (wins); false: a gateway connector
	Env       map[string]string // env vars to export for the pi subprocess (the key)
	BaseURL   string            // gateway base URL when ViaKey is false
}

// Resolve decides how a provider authenticates, mirroring the Claude posture: a
// brain-uploaded key WINS; otherwise fall back to a gateway connector routed by
// sprite identity (token-free); otherwise the provider is unavailable.
//
// getSecret reads a brain secret by name (fleet.Service.GetSecret); connectorBase
// returns a provider's gateway base URL (gateway.ConnectorBase) or "".
func Resolve(ctx context.Context, p Provider, getSecret func(context.Context, string) string, connectorBase func(context.Context, string) string) Auth {
	if key := strings.TrimSpace(getSecret(ctx, p.SecretName)); key != "" {
		return Auth{Provider: p.Name, Available: true, ViaKey: true, Env: map[string]string{p.EnvVar: key}}
	}
	if base := strings.TrimSpace(connectorBase(ctx, p.Name)); base != "" {
		return Auth{Provider: p.Name, Available: true, ViaKey: false, BaseURL: base}
	}
	return Auth{Provider: p.Name, Available: false}
}

// WriteModelsJSON writes ~/.pi/agent/models.json under homeDir for the connector
// auths (a provider override with baseUrl and NO apiKey — Pi preserves the built-in
// models and the gateway auths by identity). Key-authed providers need no entry (the
// exported env var suffices for Pi's built-in provider). Writes nothing / removes the
// file when there are no connector overrides. Returns the path written ("" if none).
func WriteModelsJSON(homeDir string, auths []Auth) (string, error) {
	overrides := map[string]map[string]string{}
	for _, a := range auths {
		if a.Available && !a.ViaKey && a.BaseURL != "" {
			overrides[a.Provider] = map[string]string{"baseUrl": strings.TrimRight(a.BaseURL, "/")}
		}
	}
	dir := filepath.Join(homeDir, ".pi", "agent")
	path := filepath.Join(dir, "models.json")
	if len(overrides) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return "", err
		}
		return "", nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	// Deterministic key order for stable output / tests.
	providers := map[string]map[string]string{}
	keys := make([]string, 0, len(overrides))
	for k := range overrides {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		providers[k] = overrides[k]
	}
	data, err := json.MarshalIndent(map[string]any{"providers": providers}, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return "", err
	}
	return path, nil
}
