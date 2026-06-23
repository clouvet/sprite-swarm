package fleet

import (
	"context"
	"testing"
	"time"

	"github.com/clouvet/sprite-agent/internal/config"
)

func TestReapTargets(t *testing.T) {
	now := time.Unix(10_000_000, 0)
	roster := []RosterEntry{
		{Status: Status{ID: "home", Role: "home", Reapable: true}, LastSeen: now.Unix()},                  // protected even if reapable
		{Status: Status{ID: "w-reapable", Role: "worker", Reapable: true}, LastSeen: now.Unix()},          // reap: self-declared
		{Status: Status{ID: "w-alive", Role: "worker"}, LastSeen: now.Unix()},                             // keep: alive, not reapable
		{Status: Status{ID: "w-dead", Role: "worker"}, LastSeen: now.Add(-10 * time.Minute).Unix()},       // reap: long dead
		{Status: Status{ID: "w-recent-dead", Role: "worker"}, LastSeen: now.Add(-2 * time.Minute).Unix()}, // keep: not past dead TTL
	}
	got := ReapTargets(roster, now, 5*time.Minute)
	want := map[string]bool{"w-reapable": true, "w-dead": true}
	if len(got) != len(want) {
		t.Fatalf("got %v, want keys %v", got, want)
	}
	for _, id := range got {
		if !want[id] {
			t.Errorf("unexpected reap target %q", id)
		}
	}
}

func TestComputeReapableIdleTransition(t *testing.T) {
	now := time.Unix(20_000_000, 0)
	svc := newService(newFakeBrain(), config.Config{AgentID: "w1"}) // role defaults to worker
	idle := true
	svc.SetIdleReaping(func() bool { return idle }, 60*time.Second)

	// Idle for less than the threshold → not yet reapable.
	if svc.computeReapable(now) {
		t.Fatal("should not be reapable at the start of an idle stretch")
	}
	if svc.computeReapable(now.Add(30 * time.Second)) {
		t.Fatal("should not be reapable before the idle threshold")
	}
	// Idle past the threshold → reapable.
	if !svc.computeReapable(now.Add(61 * time.Second)) {
		t.Fatal("should be reapable after idle exceeds threshold")
	}
	// Becomes busy → resets, not reapable.
	idle = false
	if svc.computeReapable(now.Add(120 * time.Second)) {
		t.Fatal("should not be reapable once busy again")
	}
}

func TestComputeReapableHomeNeverIdleReaps(t *testing.T) {
	now := time.Unix(20_000_000, 0)
	svc := newService(newFakeBrain(), config.Config{AgentID: "home"})
	svc.role = "home"
	svc.SetIdleReaping(func() bool { return true }, time.Nanosecond)
	if svc.computeReapable(now.Add(time.Hour)) {
		t.Fatal("home must never self-declare reapable on idle")
	}
}

func TestMarkReapableAndRemoveAgent(t *testing.T) {
	brain := newFakeBrain()
	now := time.Unix(30_000_000, 0)
	svc := newService(brain, config.Config{AgentID: "w1"})
	svc.now = func() time.Time { return now }

	if err := svc.Register(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := svc.MarkReapable(context.Background()); err != nil {
		t.Fatal(err)
	}
	roster, _ := svc.roster(context.Background())
	if len(roster) != 1 || !roster[0].Reapable || roster[0].Phase != "done" {
		t.Fatalf("expected reapable done entry, got %+v", roster)
	}

	if err := svc.RemoveAgent(context.Background(), "w1"); err != nil {
		t.Fatal(err)
	}
	roster, _ = svc.roster(context.Background())
	if len(roster) != 0 {
		t.Fatalf("expected empty roster after RemoveAgent, got %+v", roster)
	}
}
