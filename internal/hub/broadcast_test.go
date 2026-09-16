package hub

import (
	"sync"
	"testing"
	"time"
)

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

// broadcastToSession is now called directly from many goroutines (not a single loop),
// concurrently with client add/drop. This must never race or panic (double-close /
// send-on-closed). Run with -race.
func TestConcurrentBroadcastAndClientChurn(t *testing.T) {
	h := &Hub{clients: make(map[string]map[*Client]bool)}
	const sid = "S"
	h.clients[sid] = map[*Client]bool{}

	var wg sync.WaitGroup
	stop := make(chan struct{})

	for i := 0; i < 8; i++ { // broadcasters
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				h.broadcastToSession(&BroadcastMessage{SessionID: sid, Data: []byte("x")})
			}
		}()
	}
	for i := 0; i < 4; i++ { // churn: add a draining client, broadcast, drop it
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				c := &Client{sessionID: sid, send: make(chan []byte, 4)}
				done := make(chan struct{})
				go func() {
					for {
						select {
						case <-c.send:
						case <-done:
							return
						}
					}
				}()
				h.mu.Lock()
				h.clients[sid][c] = true
				h.mu.Unlock()
				h.broadcastToSession(&BroadcastMessage{SessionID: sid, Data: []byte("y")})
				h.dropClient(sid, c)
				close(done)
			}
		}()
	}
	time.Sleep(300 * time.Millisecond)
	close(stop)
	wg.Wait()
}
