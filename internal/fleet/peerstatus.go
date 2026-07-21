package fleet

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// peerStatusTimeout bounds the direct live-status fetch from a peer.
const peerStatusTimeout = 10 * time.Second

// PeerStatus answers "how is <target> doing?" on demand: the roster gives its
// last-published phase + liveness, and a direct authenticated call to its /health
// gives its LIVE state (generating right now? how many sessions? awake?). The two
// together disambiguate what a stale phase alone can't — e.g. phase "running
// tests" with generating=false and idle means it probably finished or is stuck.
//
// This is the read-side counterpart to dispatch's nudge: same .sprites.app URL +
// Bearer path. A peer can't read another's transcript, so this is how home gets a
// fresh answer without the worker having to remember to publish.
func (s *Service) PeerStatus(ctx context.Context, target string) (interface{}, error) {
	if target == "" {
		target = s.id
	}
	roster, err := s.roster(ctx)
	if err != nil {
		return nil, err
	}
	var entry *RosterEntry
	for i := range roster {
		if roster[i].ID == target {
			entry = &roster[i]
			break
		}
	}
	if entry == nil {
		// Not in the roster: destroyed, or a bare app sprite that never registered.
		// A clear state, not an error — callers shouldn't have to parse "no such agent".
		return map[string]interface{}{"id": target, "state": "gone",
			"note": "not in the fleet roster (destroyed, or never registered)"}, nil
	}

	out := map[string]interface{}{
		"id":        entry.ID,
		"phase":     entry.Phase, // last self-published activity (may be stale)
		"alive":     entry.Alive,
		"present":   entry.Present,
		"url":       entry.URL,
		"last_seen": entry.LastSeen,
		"state":     "paused", // refined below once we know more
	}
	// Pull live state directly from the target (skip self — the caller already
	// knows its own; and a sprite calling its own public URL is wasteful).
	switch {
	case target == s.id:
		out["state"] = "self"
	case !entry.Alive:
		// Stale heartbeat: suspended/paused (sprites pause when idle) or crashed.
		out["state"] = "paused"
		out["note"] = "heartbeat stale — sprite is paused/suspended (or down)"
	case entry.URL == "":
		out["state"] = "alive"
	default:
		if live, err := s.fetchHealth(ctx, entry.URL); err != nil {
			out["state"] = "unreachable" // in roster + recent heartbeat, but the live call failed
			out["live_error"] = err.Error()
		} else {
			out["state"] = "active"
			out["live"] = live
		}
	}
	return out, nil
}

// PeerResult fetches what a worker produced in a given session — its final
// assistant message — by an authenticated GET of the worker's
// /api/sessions/<session>/result. This is how home RETRIEVES a delegated result:
// it pulls from the exact session dispatch returned, so the worker never pushes
// anything back (no report-as-work-order trap). Empty session is rejected.
func (s *Service) PeerResult(ctx context.Context, target, session string) (interface{}, error) {
	if target == "" || session == "" {
		return nil, fmt.Errorf("target and session are required")
	}
	url := s.agentURL(ctx, target)
	if url == "" {
		return map[string]interface{}{"session": session, "ready": false, "state": "gone",
			"note": "target not in the fleet roster (destroyed, or never registered)"}, nil
	}
	res, err := s.authedGetJSON(ctx, strings.TrimRight(url, "/")+"/api/sessions/"+session+"/result")
	if err != nil {
		// Reachable in the roster but the live pull failed → paused/unreachable, not fatal.
		return map[string]interface{}{"session": session, "ready": false, "state": "paused",
			"note": "couldn't reach the worker (paused/suspended?): " + err.Error()}, nil
	}
	return res, nil
}

// fetchHealth does an authenticated GET of a peer's /health.
func (s *Service) fetchHealth(ctx context.Context, baseURL string) (interface{}, error) {
	return s.authedGetJSON(ctx, strings.TrimRight(baseURL, "/")+"/health")
}

// authedGetJSON GETs a peer URL with the sprites token as a Bearer (the verified
// .sprites.app cross-sprite path) and returns the parsed JSON body.
func (s *Service) authedGetJSON(ctx context.Context, url string) (interface{}, error) {
	tok := s.GetSecret(ctx, SecretSpritesAPIToken)
	if tok == "" {
		return nil, fmt.Errorf("no sprites token to authenticate the call")
	}
	ctx, cancel := context.WithTimeout(ctx, peerStatusTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s returned %d", url, resp.StatusCode)
	}
	var parsed interface{}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("bad JSON body: %w", err)
	}
	return parsed, nil
}
