package fleet

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/clouvet/sprite-swarm/internal/config"
)

func TestWriteMemoryIndexAndGet(t *testing.T) {
	brain := newFakeBrain()
	now := time.Unix(50_000_000, 0)
	a := newService(brain, config.Config{AgentID: "agent-a"})
	a.now = func() time.Time { now = now.Add(time.Second); return now }

	e1, err := a.WriteMemory(context.Background(), "auth pattern", "use OAuth via gateway", []string{"auth"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.WriteMemory(context.Background(), "build note", "go build ./...", nil); err != nil {
		t.Fatal(err)
	}

	// A different agent reads the shared index (memory is fleet-wide, not per-agent).
	b := newService(brain, config.Config{AgentID: "agent-b"})
	idx, err := b.MemoryIndex(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(idx) != 2 {
		t.Fatalf("expected 2 memory headers, got %d", len(idx))
	}
	// Newest first.
	if idx[0].Title != "build note" {
		t.Fatalf("expected newest 'build note' first, got %q", idx[0].Title)
	}
	// Headers carry no body.
	for _, h := range idx {
		if h.Author != "agent-a" {
			t.Errorf("unexpected author %q", h.Author)
		}
	}

	// On-demand body retrieval.
	full, err := b.GetMemory(context.Background(), "agent-a", e1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if full.Text != "use OAuth via gateway" {
		t.Fatalf("body mismatch: %q", full.Text)
	}
}

func TestMemoryContextRendersIndexOnly(t *testing.T) {
	brain := newFakeBrain()
	now := time.Unix(60_000_000, 0)
	a := newService(brain, config.Config{AgentID: "agent-a"})
	a.now = func() time.Time { return now }
	a.WriteMemory(context.Background(), "secret-titled note", "BODY_SHOULD_NOT_APPEAR", []string{"x"})

	ctxText, err := a.MemoryContext(context.Background(), 50)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ctxText, "secret-titled note") {
		t.Fatalf("context should list the title: %q", ctxText)
	}
	if strings.Contains(ctxText, "BODY_SHOULD_NOT_APPEAR") {
		t.Fatal("context must NOT include memory bodies (index only)")
	}
}

func TestMemorySurvivesRemoveAgent(t *testing.T) {
	brain := newFakeBrain()
	now := time.Unix(70_000_000, 0)
	a := newService(brain, config.Config{AgentID: "wk-1"})
	a.now = func() time.Time { return now }
	a.Register(context.Background())
	a.WriteMemory(context.Background(), "learning", "durable knowledge", nil)

	// Reap the worker — coordination entry gone, memory MUST remain.
	if err := a.RemoveAgent(context.Background(), "wk-1"); err != nil {
		t.Fatal(err)
	}
	idx, _ := a.MemoryIndex(context.Background())
	if len(idx) != 1 {
		t.Fatalf("memory must outlive the reaped worker, got %d entries", len(idx))
	}
	roster, _ := a.roster(context.Background())
	if len(roster) != 0 {
		t.Fatalf("coordination entry should be gone after reap, got %+v", roster)
	}
}
