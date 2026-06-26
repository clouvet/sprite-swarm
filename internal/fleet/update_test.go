package fleet

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/clouvet/sprite-agent/internal/config"
)

func TestVerifyBinary(t *testing.T) {
	if err := verifyBinary(make([]byte, 100)); err == nil {
		t.Fatal("expected error for too-small binary")
	}
	junk := make([]byte, 1<<20+16) // big enough, but not an ELF
	if err := verifyBinary(junk); err == nil {
		t.Fatal("expected error for non-ELF data")
	}
	junk[0], junk[1], junk[2], junk[3] = 0x7f, 'E', 'L', 'F'
	if err := verifyBinary(junk); err != nil {
		t.Fatalf("expected a plausible ELF to pass, got %v", err)
	}
}

// Staging then preparing must no-op: the brain holds exactly our running binary,
// so the hash matches and we don't swap/re-exec (which would clobber the test bin).
func TestPrepareSelfUpdateNoopWhenCurrent(t *testing.T) {
	brain := newFakeBrain()
	svc := newService(brain, config.Config{AgentID: "a"})
	if err := svc.StageSelf(context.Background()); err != nil {
		t.Fatalf("stage: %v", err)
	}
	willUpdate, detail, err := svc.PrepareSelfUpdate(context.Background())
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if willUpdate {
		t.Fatalf("expected no-op when already current, got willUpdate=true (%s)", detail)
	}
}

// UpdateFleet stages the caller's binary, then POSTs each other agent's
// /api/fleet/update with the bearer token; it never targets itself.
func TestUpdateFleetFansOut(t *testing.T) {
	var sawAuth, sawMethod, sawPath string
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth, sawMethod, sawPath = r.Header.Get("Authorization"), r.Method, r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer peer.Close()

	brain := newFakeBrain()
	now := time.Unix(60_000_000, 0)
	worker := newService(brain, config.Config{AgentID: "wk-1", PublicURL: peer.URL})
	worker.now = func() time.Time { return now }
	worker.Register(context.Background())
	worker.PutSecret(context.Background(), SecretSpritesAPIToken, "org/oid/tid/secret")

	home := newService(brain, config.Config{AgentID: "home"})
	home.now = func() time.Time { return now }

	res, err := home.UpdateFleet(context.Background(), "all")
	if err != nil {
		t.Fatalf("UpdateFleet: %v", err)
	}
	// The staged binary is now in the brain (home read its own executable).
	if _, err := brain.Get(context.Background(), config.ArtifactKey); err != nil {
		t.Fatalf("expected binary staged to brain: %v", err)
	}
	targets := res.(map[string]interface{})["targets"].([]UpdateResult)
	if len(targets) != 1 || targets[0].ID != "wk-1" || !targets[0].OK {
		t.Fatalf("expected wk-1 updating, got %+v", targets)
	}
	if sawMethod != http.MethodPost || sawPath != "/api/fleet/update" {
		t.Fatalf("peer hit wrong: %s %s", sawMethod, sawPath)
	}
	if sawAuth != "Bearer org/oid/tid/secret" {
		t.Fatalf("update call lacked the bearer: %q", sawAuth)
	}
}
