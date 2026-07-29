package fleet

import "testing"

func TestPeerSearchAction(t *testing.T) {
	entry := func(id, url string, alive bool) RosterEntry {
		e := RosterEntry{Alive: alive}
		e.ID = id
		e.URL = url
		return e
	}
	cases := []struct {
		name          string
		e             RosterEntry
		includeAsleep bool
		want          string
	}{
		{"self is always local", entry("home", "https://home", false), false, "self"},
		{"awake peer is queried", entry("wk1", "https://wk1", true), false, "query"},
		{"dormant peer skipped by default", entry("wk2", "https://wk2", false), false, "skip-asleep"},
		{"dormant peer queried when includeAsleep", entry("wk2", "https://wk2", false), true, "query"},
		{"no url reported", entry("wk3", "", true), false, "no-url"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := peerSearchAction(c.e, "home", c.includeAsleep); got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}
