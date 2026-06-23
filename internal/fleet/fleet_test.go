package fleet

import (
	"context"
	"testing"
	"time"

	"github.com/clouvet/sprite-agent/internal/config"
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
