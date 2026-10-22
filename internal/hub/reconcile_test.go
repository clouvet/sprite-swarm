package hub

import "testing"

// reconcile's only safe automatic action is reclaiming an in-flight message whose process
// is GONE. A live process is never reaped — reaping a live, working process was what
// re-delivered mid-turn messages and got them answered twice.
func TestDecideReconcile(t *testing.T) {
	cases := []struct {
		name              string
		inFlight, hasProc bool
		want              reconcileAction
	}{
		{"queued front — nothing to do", false, true, reconcileNone},
		{"in-flight + no process → reclaim orphan", true, false, reconcileReclaim},
		{"in-flight + live process → leave it (never reap a live process)", true, true, reconcileNone},
		{"not in-flight + no process → nothing", false, false, reconcileNone},
	}
	for _, c := range cases {
		if got := decideReconcile(c.inFlight, c.hasProc); got != c.want {
			t.Errorf("%s: decideReconcile(%v,%v)=%d, want %d", c.name, c.inFlight, c.hasProc, got, c.want)
		}
	}
}
