package fleet

import (
	"testing"
	"time"
)

func TestAgentIDFromKey(t *testing.T) {
	cases := map[string]string{
		"fleet/agent-a/status.json":    "agent-a",
		"fleet/agent-b/heartbeat.json": "agent-b",
		"fleet/":                       "",
		"other/x/status.json":          "",
		"fleet/only":                   "",
	}
	for key, want := range cases {
		if got := agentIDFromKey(key); got != want {
			t.Errorf("agentIDFromKey(%q) = %q, want %q", key, got, want)
		}
	}
}

func TestKeyDerivation(t *testing.T) {
	if statusKey("a") != "fleet/a/status.json" {
		t.Errorf("statusKey = %q", statusKey("a"))
	}
	if heartbeatKey("a") != "fleet/a/heartbeat.json" {
		t.Errorf("heartbeatKey = %q", heartbeatKey("a"))
	}
}

func TestBuildRosterLiveness(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	statuses := map[string]Status{
		"fresh": {ID: "fresh", Role: "home", UpdatedAt: now.Unix()},
		"stale": {ID: "stale", Role: "worker", UpdatedAt: now.Add(-10 * time.Minute).Unix()},
	}
	heartbeats := map[string]Heartbeat{
		"fresh": {TS: now.Unix()},
		"stale": {TS: now.Add(-10 * time.Minute).Unix()},
	}

	roster := BuildRoster(statuses, heartbeats, now)
	if len(roster) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(roster))
	}
	// Sorted by id: "fresh" < "stale".
	if roster[0].ID != "fresh" || roster[1].ID != "stale" {
		t.Fatalf("roster not sorted by id: %v", roster)
	}
	if !roster[0].Alive {
		t.Error("fresh agent should be alive")
	}
	if roster[1].Alive {
		t.Error("stale agent should be dead (heartbeat past TTL)")
	}
}

func TestBuildRosterHeartbeatBeatsStaleStatus(t *testing.T) {
	now := time.Unix(2_000_000, 0)
	// Status is old but a recent heartbeat keeps the agent alive.
	statuses := map[string]Status{"a": {ID: "a", UpdatedAt: now.Add(-time.Hour).Unix()}}
	heartbeats := map[string]Heartbeat{"a": {TS: now.Unix()}}

	roster := BuildRoster(statuses, heartbeats, now)
	if len(roster) != 1 || !roster[0].Alive {
		t.Fatalf("recent heartbeat should keep agent alive: %+v", roster)
	}
	if roster[0].LastSeen != now.Unix() {
		t.Errorf("LastSeen = %d, want %d", roster[0].LastSeen, now.Unix())
	}
}
