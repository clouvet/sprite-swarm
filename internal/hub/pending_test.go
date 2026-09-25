package hub

import (
	"testing"
)

// harness drives a pendingStore with recording callbacks.
type harness struct {
	delivered  []string // Text of each delivered message, in order
	dropped    []string // Text of each given-up message
	processed  map[string]bool
	deliverErr error
}

func (h *harness) deliver(p *pendingStore, id string) error {
	return p.deliver(id,
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

func (h *harness) confirm(p *pendingStore, id string) {
	p.confirm(id, func(m pendingMsg) bool { return h.processed[m.Text] })
}

func (h *harness) reset(p *pendingStore, id string) {
	p.resetDelivery(id, func(m pendingMsg) bool { return h.processed[m.Text] })
}

func enq(p *pendingStore, id, text string) {
	p.enqueue(id, pendingMsg{ID: p.nextID(), Content: text, Text: text})
}

func TestDeliversWhenIdle(t *testing.T) {
	p := newPendingStore("")
	h := &harness{processed: map[string]bool{}}
	enq(p, "s", "hello")
	if err := h.deliver(p, "s"); err != nil {
		t.Fatal(err)
	}
	if len(h.delivered) != 1 || h.delivered[0] != "hello" {
		t.Fatalf("delivered = %v", h.delivered)
	}
	// Turn completes and the turn is in the transcript → confirm drops it.
	h.processed["hello"] = true
	h.confirm(p, "s")
	if n := p.pending("s"); n != 0 {
		t.Fatalf("pending after confirm = %d, want 0", n)
	}
}

// Mid-turn steering: a message enqueued while a turn is generating is handed to the
// process immediately, NOT held to the turn boundary (the inverse of the old design).
func TestDeliversMidTurnImmediately(t *testing.T) {
	p := newPendingStore("")
	h := &harness{processed: map[string]bool{}}
	enq(p, "s", "first")
	_ = h.deliver(p, "s") // first delivered; a turn is now generating
	enq(p, "s", "second") // typed mid-turn
	_ = h.deliver(p, "s")
	if len(h.delivered) != 2 || h.delivered[1] != "second" {
		t.Fatalf("delivered = %v, want both delivered immediately [first second]", h.delivered)
	}
	if n := p.pending("s"); n != 2 {
		t.Fatalf("pending = %d, want 2 (both delivered but unconfirmed)", n)
	}
	// Both turns land in the transcript → confirm clears the queue.
	h.processed["first"], h.processed["second"] = true, true
	h.confirm(p, "s")
	if n := p.pending("s"); n != 0 {
		t.Fatalf("pending after confirm = %d, want 0", n)
	}
}

func TestReplayAfterDeath(t *testing.T) {
	p := newPendingStore("")
	h := &harness{processed: map[string]bool{}}
	enq(p, "s", "do the thing")
	_ = h.deliver(p, "s") // delivered
	// Process dies before a result: mark unconfirmed messages for replay.
	p.resetDelivery("s", nil)
	// Not in the transcript (processed=false) → redeliver.
	_ = h.deliver(p, "s")
	if len(h.delivered) != 2 {
		t.Fatalf("delivered = %v, want the message replayed (2 deliveries)", h.delivered)
	}
}

func TestDedupSkipsAlreadyProcessedOnReplay(t *testing.T) {
	p := newPendingStore("")
	// Processed from the start, but a FIRST delivery must still send it — the dedup
	// only applies to a replay, else a new message matching an old turn would vanish.
	h := &harness{processed: map[string]bool{"already ran": true}}
	enq(p, "s", "already ran")
	_ = h.deliver(p, "s") // first delivery ignores alreadyProcessed
	if len(h.delivered) != 1 {
		t.Fatalf("delivered = %v, want the first delivery to go through", h.delivered)
	}
	p.resetDelivery("s", nil)
	// Now it's a replay AND it's in the transcript → must NOT be re-sent, and drop it.
	_ = h.deliver(p, "s")
	if len(h.delivered) != 1 {
		t.Fatalf("delivered = %v, want no replay (dedup)", h.delivered)
	}
	if n := p.pending("s"); n != 0 {
		t.Fatalf("pending = %d, want 0 (dropped as processed)", n)
	}
}

// confirm must never drop a message that hasn't been delivered yet, even if its text
// coincidentally matches a processed turn — otherwise a fresh message is lost.
func TestConfirmSpareUndelivered(t *testing.T) {
	p := newPendingStore("")
	h := &harness{processed: map[string]bool{"dup": true}}
	enq(p, "s", "dup") // queued, never delivered
	h.confirm(p, "s")
	if n := p.pending("s"); n != 1 {
		t.Fatalf("pending = %d, want 1 (undelivered message must survive confirm)", n)
	}
	if err := h.deliver(p, "s"); err != nil {
		t.Fatal(err)
	}
	if len(h.delivered) != 1 || h.delivered[0] != "dup" {
		t.Fatalf("delivered = %v, want it delivered after all", h.delivered)
	}
}

func TestAttemptCapGivesUp(t *testing.T) {
	old := maxSendAttempts
	maxSendAttempts = 2
	defer func() { maxSendAttempts = old }()

	p := newPendingStore("")
	h := &harness{processed: map[string]bool{}}
	enq(p, "s", "poison")
	// Each death→deliver cycle re-delivers and counts an attempt.
	_ = h.deliver(p, "s") // attempt 1
	p.resetDelivery("s", nil)
	_ = h.deliver(p, "s") // attempt 2
	p.resetDelivery("s", nil)
	_ = h.deliver(p, "s") // exceeds cap → dropped
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
	if err := h.deliver(p, "s"); err == nil {
		t.Fatal("expected deliver to surface the error")
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
	// Drain to empty: deliver both, mark processed, confirm → file removed.
	h := &harness{processed: map[string]bool{}}
	_ = h.deliver(p2, "sess1")
	h.processed["survive a restart"], h.processed["me too"] = true, true
	h.confirm(p2, "sess1")
	if n := p2.pending("sess1"); n != 0 {
		t.Fatalf("pending after draining = %d, want 0", n)
	}
	p3 := newPendingStore(dir)
	p3.load()
	if n := p3.pending("sess1"); n != 0 {
		t.Fatalf("reloaded after drain = %d, want 0 (file removed)", n)
	}
}

// resetDelivery must DROP a delivered message whose turn already ran (in the
// transcript) rather than replay it — the fix for the "my messages keep getting
// replayed" loop when a kill/roll lands after claude processed the turn.
func TestResetDeliveryDropsProcessed(t *testing.T) {
	p := newPendingStore("")
	h := &harness{processed: map[string]bool{}}
	enq(p, "s", "ran already")
	_ = h.deliver(p, "s") // delivered
	h.processed["ran already"] = true
	h.reset(p, "s") // death/kill AFTER it ran → must drop, not re-queue
	if n := p.pending("s"); n != 0 {
		t.Fatalf("pending = %d, want 0 (processed turn dropped on reset)", n)
	}
	_ = h.deliver(p, "s")
	if len(h.delivered) != 1 {
		t.Fatalf("delivered = %v, want no replay of a processed turn", h.delivered)
	}
}

// resetDelivery must REPLAY a delivered message that did NOT reach the transcript
// (genuinely lost in the death) — the #95 durability guarantee.
func TestResetDeliveryReplaysUnprocessed(t *testing.T) {
	p := newPendingStore("")
	h := &harness{processed: map[string]bool{}}
	enq(p, "s", "never ran")
	_ = h.deliver(p, "s")
	h.reset(p, "s") // died before it ran → keep for replay
	if n := p.pending("s"); n != 1 {
		t.Fatalf("pending = %d, want 1 (unprocessed turn kept for replay)", n)
	}
	_ = h.deliver(p, "s")
	if len(h.delivered) != 2 {
		t.Fatalf("delivered = %v, want the unprocessed turn replayed", h.delivered)
	}
}

// A double-enqueue of the same turn (double-submit / reconnect resend) collapses to
// one; distinct turns don't; empty-text (image-only) turns are never deduped.
func TestEnqueueDedupsDuplicate(t *testing.T) {
	p := newPendingStore("")
	enq(p, "s", "be careful there")
	enq(p, "s", "be careful there") // identical, still queued → dropped
	if n := p.pending("s"); n != 1 {
		t.Fatalf("pending = %d, want 1 (duplicate collapsed)", n)
	}
	enq(p, "s", "different") // distinct → added
	if n := p.pending("s"); n != 2 {
		t.Fatalf("pending = %d, want 2 (distinct turn added)", n)
	}
	enq(p, "s", "")
	enq(p, "s", "") // empty text must NOT dedup (two image-only turns)
	if n := p.pending("s"); n != 4 {
		t.Fatalf("pending = %d, want 4 (empty-text turns not deduped)", n)
	}
}

var errBoom = &boomError{}

type boomError struct{}

func (*boomError) Error() string { return "boom" }
