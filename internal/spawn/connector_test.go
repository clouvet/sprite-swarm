package spawn

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/clouvet/sprite-agent/internal/config"
)

// Spawn is live with a token OR a gateway connector; a stub with neither.
func TestNewSpawnerModes(t *testing.T) {
	if New(config.Config{}).Available() {
		t.Fatal("no token + no gateway should be a stub")
	}
	if !New(config.Config{SpriteAPIToken: "org/oid/tok/secret"}).Available() {
		t.Fatal("token mode should be available")
	}
	if !New(config.Config{SpriteAPIGateway: "https://gw.example/x"}).Available() {
		t.Fatal("connector mode should be available")
	}
}

// Connector mode routes through the gateway base and sends NO Authorization
// header (the gateway injects the credential by sprite identity).
func TestConnectorModeOmitsAuthHeader(t *testing.T) {
	var gotAuth string
	var hit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, hit = r.Header.Get("Authorization"), true
		w.WriteHeader(http.StatusNotFound) // Exists treats 404 as "absent"
	}))
	defer srv.Close()

	sp := newAPISpawner(config.Config{SpriteAPIGateway: srv.URL}).(*apiSpawner)
	if _, err := sp.Exists(context.Background(), "wk-x"); err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if !hit {
		t.Fatal("request never reached the gateway")
	}
	if gotAuth != "" {
		t.Fatalf("connector mode must send no Authorization header, got %q", gotAuth)
	}
}

// Token mode attaches the Bearer (pointed at a test server via SPRITE_API_BASE).
func TestTokenModeSendsAuthHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	t.Setenv("SPRITE_API_BASE", srv.URL)

	sp := newAPISpawner(config.Config{SpriteAPIToken: "org/oid/tok/secret"}).(*apiSpawner)
	if _, err := sp.Exists(context.Background(), "wk-x"); err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if gotAuth != "Bearer org/oid/tok/secret" {
		t.Fatalf("token mode should send the bearer, got %q", gotAuth)
	}
}
