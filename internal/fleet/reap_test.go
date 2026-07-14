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
		{Status: Status{ID: "home", Role: "home", Reapable: true}, LastSeen: now.Unix()},            // protected even if reapable
		{Status: Status{ID: "w-reapable", Role: "worker", Reapable: true}, LastSeen: now.Unix()},    // reap: self-declared done
		{Status: Status{ID: "w-alive", Role: "worker"}, LastSeen: now.Unix()},                       // keep: alive, not reapable
		{Status: Status{ID: "w-dead", Role: "worker"}, LastSeen: now.Add(-10 * time.Minute).Unix()}, // keep: stale != destroy (may be suspended)
	}
	got := ReapTargets(roster)
	if len(got) != 1 || got[0] != "w-reapable" {
		t.Fatalf("ReapTargets should destroy only explicitly-done workers, got %v", got)
	}
}

func TestStaleWorkers(t *testing.T) {
	now := time.Unix(10_000_000, 0)
	roster := []RosterEntry{
		{Status: Status{ID: "home", Role: "home"}, LastSeen: now.Add(-time.Hour).Unix()},                     // never (home)
		{Status: Status{ID: "w-stale", Role: "worker"}, LastSeen: now.Add(-10 * time.Minute).Unix()},         // stale candidate
		{Status: Status{ID: "w-recent", Role: "worker"}, LastSeen: now.Add(-1 * time.Minute).Unix()},         // fresh
		{Status: Status{ID: "w-done", Role: "worker", Reapable: true}, LastSeen: now.Add(-time.Hour).Unix()}, // handled by ReapTargets
	}
	got := StaleWorkers(roster, now, 5*time.Minute)
	if len(got) != 1 || got[0] != "w-stale" {
		t.Fatalf("StaleWorkers should be only w-stale, got %v", got)
	}
}

func TestComputeReapableExplicitOnly(t *testing.T) {
	svc := newService(newFakeBrain(), config.Config{AgentID: "w1"}) // role defaults to worker
	// No idle-based auto-reaping: reapable only once explicitly marked done.
	if svc.computeReapable() {
		t.Fatal("should not be reapable before being marked done")
	}
	if err := svc.MarkReapable(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !svc.computeReapable() {
		t.Fatal("should be reapable after MarkReapable")
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
