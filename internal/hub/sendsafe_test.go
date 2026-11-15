package hub

import "testing"

// Reproduces the crash: a detached history goroutine sending to a client that has
// just disconnected (chat switch) hit `send on closed channel` and panicked the whole
// agent. sendRawSafe must deliver to a live client, skip a full one, and — the fix —
// be a safe no-op after the client is unregistered and its channel closed.
func TestSendRawSafe(t *testing.T) {
	h := &Hub{clients: map[string]map[*Client]bool{}}
	c := &Client{sessionID: "s", send: make(chan []byte, 1)}
	h.clients["s"] = map[*Client]bool{c: true}

	// Live client: delivered.
	h.sendRawSafe(c, []byte("a"))
	select {
	case got := <-c.send:
		if string(got) != "a" {
			t.Fatalf("got %q, want %q", got, "a")
		}
	default:
		t.Fatal("expected delivery to a live client")
	}

	// Full client (buffer of 1 already filled): skipped, never blocks.
	h.sendRawSafe(c, []byte("fill"))
	h.sendRawSafe(c, []byte("overflow")) // would block if not best-effort
	if got := <-c.send; string(got) != "fill" {
		t.Fatalf("got %q, want %q", got, "fill")
	}

	// Disconnect exactly as unregisterClient does (remove from set, then close) and
	// send again: must NOT panic and must NOT send.
	delete(h.clients["s"], c)
	close(c.send)
	h.sendRawSafe(c, []byte("late")) // the former panic path — now a no-op

	// A session with no client set at all is also safe.
	h.sendRawSafe(&Client{sessionID: "gone", send: make(chan []byte)}, []byte("x"))
}
