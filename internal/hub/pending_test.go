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

// One in flight at a time, strictly in order: a message enqueued while a turn is
// in flight is HELD (not delivered) until the in-flight one is confirmed — so turns
// can't surface out of order across a death/replay.
func TestDeliversOneInFlightInOrder(t *testing.T) {
	p := newPendingStore("")
	h := &harness{processed: map[string]bool{}}
	enq(p, "s", "first")
	_ = h.deliver(p, "s") // first delivered; now in flight
	enq(p, "s", "second") // typed while first is still in flight
	_ = h.deliver(p, "s")
	if len(h.delivered) != 1 || h.delivered[0] != "first" {
		t.Fatalf("delivered = %v, want only [first] (second held behind the in-flight one)", h.delivered)
	}
	if n := p.pending("s"); n != 2 {
		t.Fatalf("pending = %d, want 2 (both queued)", n)
	}
	// first's turn lands in the transcript → confirm removes it, next deliver sends second.
	h.processed["first"] = true
	h.confirm(p, "s")
	_ = h.deliver(p, "s")
	if len(h.delivered) != 2 || h.delivered[1] != "second" {
		t.Fatalf("delivered = %v, want [first second] in order", h.delivered)
	}
}

// The lever bug: a message in flight when the process dies must replay BEFORE any
// message queued behind it — never let a later turn jump ahead across a death.
func TestOrderPreservedAcrossDeath(t *testing.T) {
	p := newPendingStore("")
	h := &harness{processed: map[string]bool{}}
	enq(p, "s", "update some secrets?")
	_ = h.deliver(p, "s")       // in flight
	enq(p, "s", "PR is merged") // queued behind it while in flight
	p.resetDelivery("s", nil)   // process died before processing the front one
	_ = h.deliver(p, "s")
	if len(h.delivered) != 2 || h.delivered[1] != "update some secrets?" {
		t.Fatalf("delivered = %v, want the front one replayed, not the later one jumping ahead", h.delivered)
	}
	// Only after the front is confirmed does the later one go.
	h.processed["update some secrets?"] = true
	h.confirm(p, "s")
	_ = h.deliver(p, "s")
	if len(h.delivered) != 3 || h.delivered[2] != "PR is merged" {
		t.Fatalf("delivered = %v, want [.. .. 'PR is merged'] last, in order", h.delivered)
	}
}

// A stale REPLAY (delivered before, process died, now hours old) is dropped, not
// resurfaced — the "message from two days ago" bug. A first delivery is never expired.
func TestStaleReplayExpires(t *testing.T) {
	clock := int64(1000)
	oldNow, oldAge := nowUnix, maxReplayAge
	nowUnix = func() int64 { return clock }
	defer func() { nowUnix, maxReplayAge = oldNow, oldAge }()

	p := newPendingStore("")
	h := &harness{processed: map[string]bool{}}
	enq(p, "s", "stale secrets message") // stamped EnqueuedAt=1000
	_ = h.deliver(p, "s")                // first delivery — never expired
	if len(h.delivered) != 1 {
		t.Fatalf("first delivery should go through, got %v", h.delivered)
	}
	p.resetDelivery("s", nil) // process died; Attempts stays 1, now a replay candidate

	clock += int64(maxReplayAge.Seconds()) + 1 // a day-and-a-half later
	_ = h.deliver(p, "s")
	if len(h.delivered) != 1 {
		t.Fatalf("stale replay must NOT be re-delivered, got %v", h.delivered)
	}
	if n := p.pending("s"); n != 0 {
		t.Fatalf("stale replay must be dropped, pending = %d", n)
	}
}

// A full page refresh drops the WAITING (undelivered) backlog but keeps the in-flight
// message — so a stale backlog can't apply to a freshly-viewed conversation, while the
// turn already running is left alone.
func TestClearUndeliveredKeepsInFlight(t *testing.T) {
	p := newPendingStore("")
	h := &harness{processed: map[string]bool{}}
	enq(p, "s", "in flight")
	_ = h.deliver(p, "s") // delivered → in flight
	enq(p, "s", "waiting 1")
	enq(p, "s", "waiting 2")
	if n := p.clearUndelivered("s"); n != 2 {
		t.Fatalf("dropped = %d, want 2 (the two waiting ones)", n)
	}
	if n := p.pending("s"); n != 1 {
		t.Fatalf("pending = %d, want 1 (the in-flight one kept)", n)
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
	// Drain to empty, one in flight at a time: deliver+confirm the front, then the next.
	h := &harness{processed: map[string]bool{}}
	_ = h.deliver(p2, "sess1")
	h.processed["survive a restart"] = true
	h.confirm(p2, "sess1")
	_ = h.deliver(p2, "sess1")
	h.processed["me too"] = true
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
