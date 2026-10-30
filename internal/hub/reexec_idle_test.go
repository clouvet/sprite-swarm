package hub

import (
	"testing"
	"time"
)

// awaitIdle must proceed as soon as the sprite is idle (between turns), and must not block
// forever on a long-running turn — it proceeds after maxWait.
func TestAwaitIdle(t *testing.T) {
	h := &Hub{}

	// Becomes idle on the 3rd check → returns promptly, having polled.
	calls := 0
	start := time.Now()
	h.awaitIdle(func() bool { calls++; return calls >= 3 }, time.Second, time.Millisecond)
	if calls < 3 {
		t.Fatalf("should poll until idle; calls=%d", calls)
	}
	if d := time.Since(start); d > 300*time.Millisecond {
		t.Fatalf("took too long to notice idle: %s", d)
	}

	// Never idle → proceeds after maxWait (doesn't hang the roll), roughly on time.
	start = time.Now()
	h.awaitIdle(func() bool { return false }, 40*time.Millisecond, 5*time.Millisecond)
	if d := time.Since(start); d < 35*time.Millisecond || d > 400*time.Millisecond {
		t.Fatalf("timeout path off: waited %s, want ~40ms", d)
	}
}
