package pi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func secrets(m map[string]string) func(context.Context, string) string {
	return func(_ context.Context, name string) string { return m[name] }
}
func connectors(m map[string]string) func(context.Context, string) string {
	return func(_ context.Context, name string) string { return m[name] }
}

func TestResolveKeyWins(t *testing.T) {
	p, _ := ProviderByName("openai")
	// Both a brain key AND a connector exist → the key must win.
	a := Resolve(context.Background(), p,
		secrets(map[string]string{"openai-api-key": "sk-abc"}),
		connectors(map[string]string{"openai": "https://gw/openai"}))
	if !a.Available || !a.ViaKey {
		t.Fatalf("expected key auth, got %+v", a)
	}
	if a.Env["OPENAI_API_KEY"] != "sk-abc" {
		t.Errorf("expected key exported as OPENAI_API_KEY, got %v", a.Env)
	}
	if a.BaseURL != "" {
		t.Errorf("key auth should not set a baseURL, got %q", a.BaseURL)
	}
}

func TestResolveConnectorFallback(t *testing.T) {
	p, _ := ProviderByName("openai")
	a := Resolve(context.Background(), p,
		secrets(map[string]string{}),
		connectors(map[string]string{"openai": "https://gw/openai"}))
	if !a.Available || a.ViaKey {
		t.Fatalf("expected connector auth, got %+v", a)
	}
	if a.BaseURL != "https://gw/openai" {
		t.Errorf("expected connector baseURL, got %q", a.BaseURL)
	}
	if len(a.Env) != 0 {
		t.Errorf("connector auth should export no key env, got %v", a.Env)
	}
}

func TestResolveUnavailable(t *testing.T) {
	p, _ := ProviderByName("openai")
	a := Resolve(context.Background(), p, secrets(nil), connectors(nil))
	if a.Available {
		t.Fatalf("expected unavailable, got %+v", a)
	}
}

func TestWriteModelsJSON(t *testing.T) {
	home := t.TempDir()
	// One connector provider, one key provider — only the connector gets a models.json entry.
	auths := []Auth{
		{Provider: "openai", Available: true, ViaKey: true, Env: map[string]string{"OPENAI_API_KEY": "k"}},
		{Provider: "anthropic", Available: true, ViaKey: false, BaseURL: "https://gw/anthropic/"},
	}
	path, err := WriteModelsJSON(home, auths)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Providers map[string]map[string]string `json:"providers"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if _, ok := parsed.Providers["openai"]; ok {
		t.Errorf("key-authed provider must NOT appear in models.json: %s", data)
	}
	an, ok := parsed.Providers["anthropic"]
	if !ok || an["baseUrl"] != "https://gw/anthropic" {
		t.Errorf("connector provider should have a trimmed baseUrl, got %v", parsed.Providers)
	}
	if strings.Contains(string(data), "apiKey") {
		t.Errorf("connector override must not carry an apiKey (gateway auths by identity): %s", data)
	}
}

func TestWriteModelsJSONNoConnectorsRemovesFile(t *testing.T) {
	home := t.TempDir()
	// Pre-create a stale file.
	dir := filepath.Join(home, ".pi", "agent")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "models.json"), []byte("{}"), 0o644)

	path, err := WriteModelsJSON(home, []Auth{{Provider: "openai", Available: true, ViaKey: true}})
	if err != nil {
		t.Fatal(err)
	}
	if path != "" {
		t.Errorf("expected no file written for key-only auth, got %q", path)
	}
	if _, err := os.Stat(filepath.Join(dir, "models.json")); !os.IsNotExist(err) {
		t.Errorf("stale models.json should have been removed")
	}
}
