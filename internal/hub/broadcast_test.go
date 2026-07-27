package hub

import "testing"

// BroadcastAll must reach clients on EVERY session (so a session_created signal
// lands in the sidebar of someone watching a different chat), and must drop a
// client whose send buffer is full rather than block the fan-out.
func TestBroadcastAll(t *testing.T) {
	h := &Hub{clients: make(map[string]map[*Client]bool)}

	a := &Client{sessionID: "A", send: make(chan []byte, 1)}
	b := &Client{sessionID: "B", send: make(chan []byte, 1)}
	full := &Client{sessionID: "B", send: make(chan []byte)} // unbuffered, no reader → not ready
	h.clients["A"] = map[*Client]bool{a: true}
	h.clients["B"] = map[*Client]bool{b: true, full: true}

	h.BroadcastAll([]byte("hi"))

	for name, c := range map[string]*Client{"A": a, "B": b} {
		select {
		case got := <-c.send:
			if string(got) != "hi" {
				t.Fatalf("client %s got %q, want hi", name, got)
			}
		default:
			t.Fatalf("client %s received nothing", name)
		}
	}

	// The unready client was evicted from its session bucket (its send closed).
	if h.clients["B"][full] {
		t.Fatal("expected the full client to be dropped")
	}
}
