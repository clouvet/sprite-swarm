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
		return nil, fmt.Errorf("no such agent in the roster: %s", target)
	}

	out := map[string]interface{}{
		"id":        entry.ID,
		"role":      entry.Role,
		"phase":     entry.Phase, // last self-published activity (may be stale)
		"alive":     entry.Alive,
		"present":   entry.Present,
		"url":       entry.URL,
		"last_seen": entry.LastSeen,
	}
	// Pull live state directly from the target (skip self — the caller already
	// knows its own; and a sprite calling its own public URL is wasteful).
	if entry.URL != "" && target != s.id {
		if live, err := s.fetchHealth(ctx, entry.URL); err != nil {
			out["live_error"] = err.Error() // suspended/gone/unreachable — phase+alive still answer
		} else {
			out["live"] = live
		}
	}
	return out, nil
}

// fetchHealth does an authenticated GET of a peer's /health and returns the parsed
// JSON. Uses the sprites token as a Bearer against the peer's .sprites.app URL.
func (s *Service) fetchHealth(ctx context.Context, baseURL string) (interface{}, error) {
	tok := s.GetSecret(ctx, SecretSpritesAPIToken)
	if tok == "" {
		return nil, fmt.Errorf("no sprites token to authenticate the call")
	}
	ctx, cancel := context.WithTimeout(ctx, peerStatusTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/health", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("health returned %d", resp.StatusCode)
	}
	var parsed interface{}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("bad health body: %w", err)
	}
	return parsed, nil
}
