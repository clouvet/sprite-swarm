package fleet

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/clouvet/sprite-swarm/internal/config"
)

func TestFleetContextRendersPresenceAndMemory(t *testing.T) {
	brain := newFakeBrain()
	now := time.Unix(80_000_000, 0)

	// home (the reader), a worker with a human attached, and a memory entry.
	home := newService(brain, config.Config{AgentID: "home"})
	home.now = func() time.Time { return now }
	home.Register(context.Background())
	home.WriteMemory(context.Background(), "deploy steps", "go build then run", []string{"ops"})

	worker := newService(brain, config.Config{AgentID: "wk-1"})
	worker.now = func() time.Time { return now }
	worker.SetAttendanceProbe(func() (bool, string) { return true, "sess-123" })
	worker.Register(context.Background())

	ctxText, err := home.FleetContext(context.Background(), 50, "")
	if err != nil {
		t.Fatal(err)
	}
	// Roster lists both agents.
	if !strings.Contains(ctxText, "home") || !strings.Contains(ctxText, "wk-1") {
		t.Fatalf("context missing agents:\n%s", ctxText)
	}
	// Presence-routing: the attended worker is flagged DEFER.
	if !strings.Contains(ctxText, "wk-1") || !strings.Contains(ctxText, "DEFER") {
		t.Fatalf("context should flag attended worker to defer:\n%s", ctxText)
	}
	if !strings.Contains(ctxText, "human is steering") {
		t.Fatalf("context should summarize who is steered:\n%s", ctxText)
	}
	// Memory index appears (title), bodies do not.
	if !strings.Contains(ctxText, "deploy steps") {
		t.Fatalf("context should include memory index:\n%s", ctxText)
	}
	if strings.Contains(ctxText, "go build then run") {
		t.Fatalf("context must not include memory bodies:\n%s", ctxText)
	}
}

// The roster+memory body is cached: a peer that joins after the cache is warm isn't
// reflected until the TTL lapses and a background refresh runs — so a turn never blocks
// on the brain for it.
func TestFleetContextBodyCachedUntilStale(t *testing.T) {
	brain := newFakeBrain()
	cur := time.Unix(80_000_000, 0)
	home := newService(brain, config.Config{AgentID: "home"})
	home.now = func() time.Time { return cur }
	home.Register(context.Background())

	c1, _ := home.FleetContext(context.Background(), 50, "") // cold-builds + caches
	if !strings.Contains(c1, "home") || strings.Contains(c1, "wk-1") {
		t.Fatalf("first render should have home only:\n%s", c1)
	}

	worker := newService(brain, config.Config{AgentID: "wk-1"})
	worker.now = func() time.Time { return cur }
	worker.Register(context.Background()) // joins AFTER the cache warmed

	c2, _ := home.FleetContext(context.Background(), 50, "") // within TTL → cached
	if strings.Contains(c2, "wk-1") {
		t.Fatalf("within TTL the body should be cached, not reflect wk-1:\n%s", c2)
	}

	cur = cur.Add(fleetBodyTTL + time.Second)              // now stale
	_, _ = home.FleetContext(context.Background(), 50, "") // returns stale, kicks bg refresh
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if c, _ := home.FleetContext(context.Background(), 50, ""); strings.Contains(c, "wk-1") {
			return // refresh reflected the new peer
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("after TTL, a background refresh should reflect the new peer")
}
