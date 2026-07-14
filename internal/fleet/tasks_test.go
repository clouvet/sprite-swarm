package fleet

import (
	"context"
	"testing"
	"time"

	"github.com/clouvet/sprite-swarm/internal/config"
)

func TestDispatchThenInbox(t *testing.T) {
	brain := newFakeBrain()
	now := time.Unix(40_000_000, 0)
	home := newService(brain, config.Config{AgentID: "home"})
	home.now = func() time.Time { return now }

	task, err := home.dispatch(context.Background(), "wk-1", "build the thing", KindTask)
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

func TestDrainInboxInjectsOnceAndDedups(t *testing.T) {
	brain := newFakeBrain()
	now := time.Unix(41_000_000, 0)
	home := newService(brain, config.Config{AgentID: "home"})
	home.now = func() time.Time { return now }
	task, _ := home.dispatch(context.Background(), "wk-1", "do work", KindTask)

	worker := newService(brain, config.Config{AgentID: "wk-1"})
	var injected []string
	worker.injectFn = func(sid, tk, _ string) error { injected = append(injected, sid+"|"+tk); return nil }
	worker.seen = worker.loadSeen(context.Background())

	if err := worker.DrainInbox(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Simulate a restart: reload seen from the brain, drain again — no re-inject.
	worker.seen = worker.loadSeen(context.Background())
	if err := worker.DrainInbox(context.Background()); err != nil {
		t.Fatal(err)
	}

	if len(injected) != 1 || injected[0] != task.SessionID+"|do work" {
		t.Fatalf("expected exactly one injection, got %v", injected)
	}
}

// StartTaskPolling drains immediately, so a task waiting in the brain at boot is
// delivered without waiting for the backstop tick (the nudge fast-path relies on
// this same immediate drain when a peer pokes us).
func TestStartTaskPollingDrainsImmediately(t *testing.T) {
	brain := newFakeBrain()
	now := time.Unix(42_000_000, 0)
	home := newService(brain, config.Config{AgentID: "home"})
	home.now = func() time.Time { return now }
	home.dispatch(context.Background(), "wk-1", "waiting task", KindTask)

	worker := newService(brain, config.Config{AgentID: "wk-1"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // stop the backstop goroutine when the test ends

	done := make(chan string, 1)
	worker.StartTaskPolling(ctx, func(_, tk, _ string) error { done <- tk; return nil }, func() bool { return false })

	select {
	case got := <-done:
		if got != "waiting task" {
			t.Fatalf("unexpected task: %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("StartTaskPolling did not drain the waiting task on boot")
	}
}

func TestDrainSerializesExecutableTasks(t *testing.T) {
	brain := newFakeBrain()
	now := time.Unix(70_000_000, 0)
	home := newService(brain, config.Config{AgentID: "home"})
	home.now = func() time.Time { return now }
	home.dispatch(context.Background(), "wk-1", "A", KindTask)
	home.dispatch(context.Background(), "wk-1", "B", KindTask)

	var injected []string
	w := newService(brain, config.Config{AgentID: "wk-1"})
	w.injectFn = func(_, tk, _ string) error { injected = append(injected, tk); return nil }
	w.seen = map[string]bool{}
	w.busy = func() bool { return false }

	w.DrainInbox(context.Background())
	if len(injected) != 1 {
		t.Fatalf("one executable task per drain; got %v", injected)
	}
	w.DrainInbox(context.Background())
	if len(injected) != 2 {
		t.Fatalf("second drain takes the next; got %v", injected)
	}
	home.dispatch(context.Background(), "wk-1", "C", KindTask)
	w.busy = func() bool { return true }
	w.DrainInbox(context.Background())
	if len(injected) != 2 {
		t.Fatalf("busy worker must not start new tasks; got %v", injected)
	}
}
