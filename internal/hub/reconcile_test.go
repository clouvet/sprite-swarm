package hub

import (
	"testing"
	"time"
)

// decideReconcile is the safety-critical core: it must reap-and-replay a stuck in-flight
// message, but never abort a turn that's actually running or one that may have just been
// handed over. The grace + "turn started" + transcript-idle gates make that distinction.
func TestDecideReconcile(t *testing.T) {
	const grace = 90 * time.Second
	cases := []struct {
		name                       string
		inFlight, hasProc, started bool
		idle                       time.Duration
		want                       reconcileAction
	}{
		{"queued front — nothing to do", false, true, false, time.Hour, reconcileNone},
		{"no process → reclaim orphan", true, false, false, 0, reconcileReclaim},
		{"turn started, process live → leave (confirm clears it)", true, true, true, time.Hour, reconcileNone},
		{"long tool call: started but transcript briefly idle → leave", true, true, true, 10 * time.Minute, reconcileNone},
		{"just delivered, not started, within grace → leave", true, true, false, 10 * time.Second, reconcileNone},
		{"never started + transcript idle past grace → reap & replay", true, true, false, 5 * time.Minute, reconcileReapReplay},
		{"never started + no process still reclaims (no-process wins)", true, false, false, 5 * time.Minute, reconcileReclaim},
	}
	for _, c := range cases {
		if got := decideReconcile(c.inFlight, c.hasProc, c.started, c.idle, grace); got != c.want {
			t.Errorf("%s: decideReconcile=%d, want %d", c.name, got, c.want)
		}
	}
}
