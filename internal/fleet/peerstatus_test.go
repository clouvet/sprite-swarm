package fleet

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/clouvet/sprite-agent/internal/config"
)

func TestPeerStatusMergesLiveHealth(t *testing.T) {
	// Stand in for the worker's /health, asserting the call carries the bearer.
	var sawAuth string
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		sawAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok","generating":1,"active_sessions":2}`))
	}))
	defer peer.Close()

	brain := newFakeBrain()
	now := time.Unix(50_000_000, 0)

	worker := newService(brain, config.Config{AgentID: "wk-1", PublicURL: peer.URL})
	worker.now = func() time.Time { return now }
	if err := worker.Register(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := worker.UpdatePhase(context.Background(), "running tests"); err != nil {
		t.Fatal(err)
	}
	if err := worker.PutSecret(context.Background(), SecretSpritesAPIToken, "org/oid/tid/secret"); err != nil {
		t.Fatal(err)
	}

	home := newService(brain, config.Config{AgentID: "home"})
	home.now = func() time.Time { return now }

	res, err := home.PeerStatus(context.Background(), "wk-1")
	if err != nil {
		t.Fatalf("PeerStatus: %v", err)
	}
	m := res.(map[string]interface{})
	if m["phase"] != "running tests" {
		t.Fatalf("phase from roster missing: %+v", m)
	}
	if _, ok := m["live_error"]; ok {
		t.Fatalf("unexpected live_error: %+v", m)
	}
	live, ok := m["live"].(map[string]interface{})
	if !ok || live["generating"] != float64(1) || live["active_sessions"] != float64(2) {
		t.Fatalf("live health not merged: %+v", m)
	}
	if sawAuth != "Bearer org/oid/tid/secret" {
		t.Fatalf("health call lacked the bearer token, got %q", sawAuth)
	}
}

func TestPeerResultPullsSessionResult(t *testing.T) {
	var gotPath, gotAuth string
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"session":"sess-1","ready":true,"result":"the findings"}`))
	}))
	defer peer.Close()

	brain := newFakeBrain()
	now := time.Unix(52_000_000, 0)
	worker := newService(brain, config.Config{AgentID: "wk-1", PublicURL: peer.URL})
	worker.now = func() time.Time { return now }
	worker.Register(context.Background())
	worker.PutSecret(context.Background(), SecretSpritesAPIToken, "org/oid/tid/secret")

	home := newService(brain, config.Config{AgentID: "home"})
	home.now = func() time.Time { return now }

	res, err := home.PeerResult(context.Background(), "wk-1", "sess-1")
	if err != nil {
		t.Fatalf("PeerResult: %v", err)
	}
	m := res.(map[string]interface{})
	if m["ready"] != true || m["result"] != "the findings" {
		t.Fatalf("result not pulled through: %+v", m)
	}
	if gotPath != "/api/sessions/sess-1/result" {
		t.Fatalf("wrong path pulled: %q", gotPath)
	}
	if gotAuth != "Bearer org/oid/tid/secret" {
		t.Fatalf("result pull lacked the bearer: %q", gotAuth)
	}
	// A missing session id is rejected without a network call.
	if _, err := home.PeerResult(context.Background(), "wk-1", ""); err == nil {
		t.Fatal("expected error for empty session")
	}
}

// A reachable-but-down peer (no live endpoint) still answers from the roster, with
// the failure surfaced rather than fatal.
func TestPeerStatusFallsBackWhenUnreachable(t *testing.T) {
	brain := newFakeBrain()
	now := time.Unix(51_000_000, 0)
	worker := newService(brain, config.Config{AgentID: "wk-9", PublicURL: "http://127.0.0.1:0"})
	worker.now = func() time.Time { return now }
	worker.Register(context.Background())
	worker.PutSecret(context.Background(), SecretSpritesAPIToken, "org/oid/tid/secret")

	home := newService(brain, config.Config{AgentID: "home"})
	home.now = func() time.Time { return now }
	res, err := home.PeerStatus(context.Background(), "wk-9")
	if err != nil {
		t.Fatalf("PeerStatus should not fail when the peer is unreachable: %v", err)
	}
	m := res.(map[string]interface{})
	if _, ok := m["live_error"]; !ok {
		t.Fatalf("expected live_error for unreachable peer: %+v", m)
	}
	if m["id"] != "wk-9" {
		t.Fatalf("roster fields should still be present: %+v", m)
	}
}
