package hub

import (
	"path/filepath"
	"testing"
)

// harness drives a pendingStore with recording callbacks.
type harness struct {
	delivered []string // Text of each delivered message, in order
	dropped   []string // Text of each given-up message
	processed map[string]bool
	deliverErr error
}

func (h *harness) pump(p *pendingStore, id string) error {
	return p.pump(id,
		func(m pendingMsg) error {
			if h.deliverErr != nil {
				return h.deliverErr
			}
			h.delivered = append(h.delivered, m.Text)
			return nil
		},
		func(m pendingMsg) bool { return h.processed[m.Text] },
		func(m pendingMsg) { h.dropped = append(h.dropped, m.Text) },
	)
}

func enq(p *pendingStore, id, text string) {
	p.enqueue(id, pendingMsg{ID: p.nextID(), Content: text, Text: text})
}

func TestPumpDeliversWhenIdle(t *testing.T) {
	p := newPendingStore("")
	h := &harness{}
	enq(p, "s", "hello")
	if err := h.pump(p, "s"); err != nil {
		t.Fatal(err)
	}
	if len(h.delivered) != 1 || h.delivered[0] != "hello" {
		t.Fatalf("delivered = %v", h.delivered)
	}
	// Turn completes → ack clears the queue.
	p.ackInFlight("s")
	if n := p.pending("s"); n != 0 {
		t.Fatalf("pending after ack = %d, want 0", n)
	}
}

func TestPumpHoldsWhileGenerating(t *testing.T) {
	p := newPendingStore("")
	h := &harness{}
	enq(p, "s", "first")
	_ = h.pump(p, "s") // first is now in flight
	enq(p, "s", "second")
	_ = h.pump(p, "s") // must HOLD second — a turn is in flight
	if len(h.delivered) != 1 {
		t.Fatalf("delivered = %v, want only [first] (second held)", h.delivered)
	}
	if n := p.pending("s"); n != 2 {
		t.Fatalf("pending = %d, want 2 (both queued)", n)
	}
	// Turn boundary: ack first, pump delivers second.
	p.ackInFlight("s")
	_ = h.pump(p, "s")
	if len(h.delivered) != 2 || h.delivered[1] != "second" {
		t.Fatalf("delivered = %v, want [first second]", h.delivered)
	}
}

func TestReplayAfterDeath(t *testing.T) {
	p := newPendingStore("")
	h := &harness{}
	enq(p, "s", "do the thing")
	_ = h.pump(p, "s") // in flight, Sent=true
	// Process dies before a result: clear in-flight, message stays queued.
	p.clearInFlight("s")
	// Not in the transcript (processed=false) → redeliver.
	_ = h.pump(p, "s")
	if len(h.delivered) != 2 {
		t.Fatalf("delivered = %v, want the message replayed (2 deliveries)", h.delivered)
	}
}

func TestDedupSkipsAlreadyProcessed(t *testing.T) {
	p := newPendingStore("")
	h := &harness{processed: map[string]bool{"already ran": true}}
	enq(p, "s", "already ran")
	_ = h.pump(p, "s") // delivered once, in flight
	p.clearInFlight("s")
	// It made it into the transcript before the death → must NOT be re-sent.
	_ = h.pump(p, "s")
	if len(h.delivered) != 1 {
		t.Fatalf("delivered = %v, want no replay (dedup)", h.delivered)
	}
	if n := p.pending("s"); n != 0 {
		t.Fatalf("pending = %d, want 0 (dropped as processed)", n)
	}
}

func TestAttemptCapGivesUp(t *testing.T) {
	old := maxSendAttempts
	maxSendAttempts = 2
	defer func() { maxSendAttempts = old }()

	p := newPendingStore("")
	h := &harness{}
	enq(p, "s", "poison")
	// Each death→pump cycle re-delivers and counts an attempt.
	_ = h.pump(p, "s") // attempt 1
	p.clearInFlight("s")
	_ = h.pump(p, "s") // attempt 2
	p.clearInFlight("s")
	_ = h.pump(p, "s") // exceeds cap → dropped
	if len(h.dropped) != 1 || h.dropped[0] != "poison" {
		t.Fatalf("dropped = %v, want [poison]", h.dropped)
	}
	if n := p.pending("s"); n != 0 {
		t.Fatalf("pending = %d, want 0 (given up)", n)
	}
}

func TestDeliverErrorKeepsQueued(t *testing.T) {
	p := newPendingStore("")
	h := &harness{deliverErr: errBoom}
	enq(p, "s", "keep me")
	if err := h.pump(p, "s"); err == nil {
		t.Fatal("expected pump to surface the deliver error")
	}
	if n := p.pending("s"); n != 1 {
		t.Fatalf("pending = %d, want 1 (still queued after a failed delivery)", n)
	}
}

func TestPersistAndReload(t *testing.T) {
	dir := t.TempDir()
	p := newPendingStore(dir)
	enq(p, "sess1", "survive a restart")
	enq(p, "sess1", "me too")
	if _, err := filepath.Abs(dir); err != nil {
		t.Fatal(err)
	}
	// Simulate a full restart: a fresh store loads from disk.
	p2 := newPendingStore(dir)
	p2.load()
	if n := p2.pending("sess1"); n != 2 {
		t.Fatalf("reloaded pending = %d, want 2", n)
	}
	// New ids must not collide with the reloaded ones.
	if p2.nextID() == "1" {
		t.Fatal("nextID collided with a reloaded id")
	}
	// Draining to empty removes the file.
	p2.ackInFlight("sess1") // nothing in flight yet → no-op
	h := &harness{}
	_ = h.pump(p2, "sess1")
	p2.ackInFlight("sess1")
	_ = h.pump(p2, "sess1")
	p2.ackInFlight("sess1")
	if n := p2.pending("sess1"); n != 0 {
		t.Fatalf("pending after draining = %d, want 0", n)
	}
	p3 := newPendingStore(dir)
	p3.load()
	if n := p3.pending("sess1"); n != 0 {
		t.Fatalf("reloaded after drain = %d, want 0 (file removed)", n)
	}
}

var errBoom = &boomError{}

type boomError struct{}

func (*boomError) Error() string { return "boom" }
