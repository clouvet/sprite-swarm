package fleet

import (
	"context"
	"testing"
	"time"

	"github.com/clouvet/sprite-agent/internal/config"
)

func TestDispatchThenInbox(t *testing.T) {
	brain := newFakeBrain()
	now := time.Unix(40_000_000, 0)
	home := newService(brain, config.Config{AgentID: "home"})
	home.now = func() time.Time { return now }

	task, err := home.dispatch(context.Background(), "wk-1", "build the thing")
	if err != nil {
		t.Fatal(err)
	}
	if task.From != "home" || task.To != "wk-1" || task.SessionID == "" {
		t.Fatalf("bad task: %+v", task)
	}

	// The target sees it in its inbox.
	worker := newService(brain, config.Config{AgentID: "wk-1"})
	inbox, err := worker.Inbox(context.Background(), "wk-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox) != 1 || inbox[0].Task != "build the thing" || inbox[0].SessionID != task.SessionID {
		t.Fatalf("inbox mismatch: %+v", inbox)
	}

	// A different agent's inbox is empty (addressing works).
	if other, _ := worker.Inbox(context.Background(), "wk-2"); len(other) != 0 {
		t.Fatalf("expected empty inbox for wk-2, got %+v", other)
	}
}

func TestTaskPollingInjectsOnceAndDedups(t *testing.T) {
	brain := newFakeBrain()
	now := time.Unix(41_000_000, 0)
	home := newService(brain, config.Config{AgentID: "home"})
	home.now = func() time.Time { return now }
	task, _ := home.dispatch(context.Background(), "wk-1", "do work")

	worker := newService(brain, config.Config{AgentID: "wk-1"})

	// Simulate two poll passes: inject unseen, persist seen, re-load, no re-inject.
	var injected []string
	seen := worker.loadSeen(context.Background())
	pass := func() {
		inbox, _ := worker.Inbox(context.Background(), "wk-1")
		changed := false
		for _, tk := range inbox {
			if seen[tk.ID] {
				continue
			}
			injected = append(injected, tk.SessionID+"|"+tk.Task)
			seen[tk.ID] = true
			changed = true
		}
		if changed {
			worker.saveSeen(context.Background(), seen)
		}
	}
	pass()
	// Reload seen from the brain (as a restart would) and poll again.
	seen = worker.loadSeen(context.Background())
	pass()

	if len(injected) != 1 || injected[0] != task.SessionID+"|do work" {
		t.Fatalf("expected exactly one injection, got %v", injected)
	}
}
