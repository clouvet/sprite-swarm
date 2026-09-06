package fleet

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/clouvet/sprite-swarm/internal/config"
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

func TestSelectUpdateTargets(t *testing.T) {
	roster := []RosterEntry{
		{Status: Status{ID: "self"}},
		{Status: Status{ID: "main-a"}},
		{Status: Status{ID: "main-b"}},
		{Status: Status{ID: "exp-pinned", Pinned: true}},
	}
	ids := func(es []RosterEntry) []string {
		out := []string{}
		for _, e := range es {
			out = append(out, e.ID)
		}
		return out
	}

	// Bulk "all": self excluded, pinned skipped (reported), rest targeted.
	tg, sk := selectUpdateTargets(roster, "self", "all")
	if got := ids(tg); len(got) != 2 || got[0] != "main-a" || got[1] != "main-b" {
		t.Fatalf("bulk targets = %v, want [main-a main-b]", got)
	}
	if len(sk) != 1 || sk[0].ID != "exp-pinned" || sk[0].OK {
		t.Fatalf("bulk skipped = %+v, want exp-pinned not-ok", sk)
	}

	// Bulk "" behaves the same as "all".
	tg2, sk2 := selectUpdateTargets(roster, "self", "")
	if len(tg2) != 2 || len(sk2) != 1 {
		t.Fatalf(`empty target: targets=%v skipped=%d`, ids(tg2), len(sk2))
	}

	// Explicit single target STILL updates a pinned sprite (operator asked by name).
	tg3, sk3 := selectUpdateTargets(roster, "self", "exp-pinned")
	if len(tg3) != 1 || tg3[0].ID != "exp-pinned" || len(sk3) != 0 {
		t.Fatalf("explicit pinned target: targets=%v skipped=%v", ids(tg3), sk3)
	}

	// Self is never a target, even if named.
	tg4, _ := selectUpdateTargets(roster, "self", "self")
	if len(tg4) != 0 {
		t.Fatalf("self should never be targeted, got %v", ids(tg4))
	}
}
