package fleet

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/clouvet/sprite-agent/internal/config"
)

func TestFleetContextRendersPresenceAndMemory(t *testing.T) {
	brain := newFakeBrain()
	now := time.Unix(80_000_000, 0)

	// home (the reader), a worker with a human attached, and a memory entry.
	home := newService(brain, config.Config{AgentID: "home"})
	home.role = "home"
	home.now = func() time.Time { return now }
	home.Register(context.Background())
	home.WriteMemory(context.Background(), "deploy steps", "go build then run", []string{"ops"})

	worker := newService(brain, config.Config{AgentID: "wk-1"})
	worker.now = func() time.Time { return now }
	worker.SetAttendanceProbe(func() (bool, string) { return true, "sess-123" })
	worker.Register(context.Background())

	ctxText, err := home.FleetContext(context.Background(), 50)
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
