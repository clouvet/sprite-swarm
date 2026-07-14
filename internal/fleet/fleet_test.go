package fleet

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/clouvet/sprite-swarm/internal/config"
)

func TestRegisterThenRoster(t *testing.T) {
	brain := newFakeBrain()
	now := time.Unix(5_000_000, 0)
	svc := newService(brain, config.Config{AgentID: "agent-a", ArtifactRef: "art@main"})
	svc.now = func() time.Time { return now }

	if err := svc.Register(context.Background()); err != nil {
		t.Fatalf("register: %v", err)
	}

	// Registration must write only this agent's own keys (DESIGN §4.1 pattern 1).
	if _, err := brain.Get(context.Background(), "fleet/agent-a/status.json"); err != nil {
		t.Fatalf("status not written: %v", err)
	}
	if _, err := brain.Get(context.Background(), "fleet/agent-a/heartbeat.json"); err != nil {
		t.Fatalf("heartbeat not written: %v", err)
	}

	roster, err := svc.roster(context.Background())
	if err != nil {
		t.Fatalf("roster: %v", err)
	}
	if len(roster) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(roster))
	}
	if roster[0].ID != "agent-a" || !roster[0].Alive || roster[0].Phase != "idle" {
		t.Fatalf("unexpected entry: %+v", roster[0])
	}
}

// A worker's published phase is visible to a peer reading the roster + fleet
// context — the channel that lets home answer "how's wk-3 doing?" without
// reading the worker's transcript.
func TestUpdatePhaseVisibleToPeer(t *testing.T) {
	brain := newFakeBrain()
	now := time.Unix(5_000_000, 0)

	worker := newService(brain, config.Config{AgentID: "wk-3"})
	worker.now = func() time.Time { return now }
	if err := worker.Register(context.Background()); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := worker.UpdatePhase(context.Background(), "running the test suite"); err != nil {
		t.Fatalf("update phase: %v", err)
	}

	home := newService(brain, config.Config{AgentID: "home"})
	home.now = func() time.Time { return now }

	roster, err := home.roster(context.Background())
	if err != nil {
		t.Fatalf("roster: %v", err)
	}
	var wk *RosterEntry
	for i := range roster {
		if roster[i].ID == "wk-3" {
			wk = &roster[i]
		}
	}
	if wk == nil || wk.Phase != "running the test suite" {
		t.Fatalf("peer should see worker's published phase, got %+v", wk)
	}

	ctx, err := home.FleetContext(context.Background(), 0)
	if err != nil {
		t.Fatalf("fleet context: %v", err)
	}
	if !strings.Contains(ctx, "running the test suite") {
		t.Fatalf("fleet context should surface the worker phase:\n%s", ctx)
	}
}

func TestRosterSeesMultipleAgents(t *testing.T) {
	brain := newFakeBrain()
	now := time.Unix(6_000_000, 0)

	// Two agents register into the same brain (each writes only its own keys).
	for _, id := range []string{"home-1", "wk-2"} {
		svc := newService(brain, config.Config{AgentID: id})
		svc.now = func() time.Time { return now }
		if err := svc.Register(context.Background()); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
	}

	// Either agent reading the brain sees the whole roster.
	reader := newService(brain, config.Config{AgentID: "home-1"})
	reader.now = func() time.Time { return now }
	roster, err := reader.roster(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(roster) != 2 {
		t.Fatalf("expected 2 agents, got %d: %+v", len(roster), roster)
	}
	if roster[0].ID != "home-1" || roster[1].ID != "wk-2" {
		t.Fatalf("roster ids/order wrong: %+v", roster)
	}
}
