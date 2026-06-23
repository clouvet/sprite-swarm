// Package fleet is the minimal Phase 1 fleet brain: an agent registers itself
// into shared S3/Tigris storage on boot and any agent can list the roster.
//
// Per DESIGN §4.1 pattern 1 ("derive the index, don't store it"): each agent
// writes ONLY its own keys (fleet/<id>/status.json, fleet/<id>/heartbeat.json),
// and the roster is computed from ListObjects("fleet/"). Two agents never write
// the same key, so concurrent-write corruption of a shared index is impossible.
//
// Phase 1 is roster only — no dispatch, no durable shared memory (Phase 2).
package fleet

import (
	"path"
	"sort"
	"strings"
	"time"
)

// heartbeatTTL is how long after its last heartbeat an agent is still "alive".
const heartbeatTTL = 90 * time.Second

// Status is what an agent writes to fleet/<id>/status.json each turn/boot.
type Status struct {
	ID        string `json:"id"`
	Role      string `json:"role"`              // "home" | "worker"
	Phase     string `json:"phase"`             // free-text current activity
	URL       string `json:"url"`               // session-service URL, if known
	Artifact  string `json:"artifact"`          // bootstrap pointer it's running
	Reapable  bool   `json:"reapable"`          // worker self-declares it can be reaped (idle/done)
	Present   bool   `json:"present"`           // a human is currently attached to this agent (presence, §2.4)
	Session   string `json:"session,omitempty"` // the session the human is attached to, if any
	StartedAt int64  `json:"started_at"`
	UpdatedAt int64  `json:"updated_at"`
}

// Heartbeat is what an agent writes to fleet/<id>/heartbeat.json periodically.
type Heartbeat struct {
	TS int64 `json:"ts"`
}

// RosterEntry is one agent as seen in the derived roster.
type RosterEntry struct {
	Status
	Alive    bool  `json:"alive"`
	LastSeen int64 `json:"last_seen"` // unix seconds of latest heartbeat (or status)
}

// ReapTargets is the pure reaping policy: which agents should be destroyed now.
//
// Reapable = a worker (never role "home") that either self-declared Reapable
// (idle/done) or has been dead far longer than the liveness TTL (its sprite
// crashed/was destroyed and isn't coming back — clean up its brain entry).
// Home is always protected (DESIGN §4.2: home is pinned, never auto-reaped).
func ReapTargets(roster []RosterEntry, now time.Time, deadReapAfter time.Duration) []string {
	var ids []string
	for _, e := range roster {
		if e.Role == "home" {
			continue
		}
		dead := e.LastSeen > 0 && now.Sub(time.Unix(e.LastSeen, 0)) > deadReapAfter
		if e.Reapable || dead {
			ids = append(ids, e.ID)
		}
	}
	return ids
}

// statusKey / heartbeatKey are the per-agent keys an agent owns.
func statusKey(id string) string    { return path.Join("fleet", id, "status.json") }
func heartbeatKey(id string) string { return path.Join("fleet", id, "heartbeat.json") }

// agentIDFromKey extracts "<id>" from "fleet/<id>/<file>", or "" if it doesn't
// match that shape.
func agentIDFromKey(key string) string {
	parts := strings.Split(key, "/")
	if len(parts) >= 3 && parts[0] == "fleet" {
		return parts[1]
	}
	return ""
}

// BuildRoster derives the roster from per-agent status + heartbeat objects.
//
// It is a pure function (no I/O) so it can be unit-tested without S3: given the
// objects discovered under fleet/, it merges each agent's status with its
// heartbeat to decide liveness against now. Entries are sorted by id for stable
// output.
func BuildRoster(statuses map[string]Status, heartbeats map[string]Heartbeat, now time.Time) []RosterEntry {
	entries := make([]RosterEntry, 0, len(statuses))
	for id, st := range statuses {
		lastSeen := st.UpdatedAt
		if hb, ok := heartbeats[id]; ok && hb.TS > lastSeen {
			lastSeen = hb.TS
		}
		alive := lastSeen > 0 && now.Sub(time.Unix(lastSeen, 0)) <= heartbeatTTL
		entries = append(entries, RosterEntry{Status: st, Alive: alive, LastSeen: lastSeen})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })
	return entries
}
