package fleet

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// FleetSearchSprite is one sprite's slice of a fleet-wide search. Hits is that
// sprite's raw []SearchHit JSON (passed through verbatim — the server package
// owns that shape). Exactly one of Hits / Error / Skipped is meaningful.
type FleetSearchSprite struct {
	Sprite  string          `json:"sprite"`
	URL     string          `json:"url,omitempty"`
	Self    bool            `json:"self,omitempty"`    // the sprite running this search (open hits locally)
	Present bool            `json:"present,omitempty"` // a human is attached here
	Hits    json.RawMessage `json:"hits,omitempty"`
	Error   string          `json:"error,omitempty"`
	Skipped bool            `json:"skipped,omitempty"` // dormant + not asked (or 502)
}

// FleetSearchResult is the merged response: one entry per roster sprite.
type FleetSearchResult struct {
	Query   string              `json:"query"`
	Sprites []FleetSearchSprite `json:"sprites"`
}

const fleetSearchCallTimeout = 12 * time.Second

// peerSearchAction decides how a roster entry participates in a fleet search:
// "self" (query locally), "query" (authed fan-out), "skip-asleep" (dormant and
// includeAsleep is false), or "no-url". Pure, so the policy is unit-tested.
func peerSearchAction(e RosterEntry, selfID string, includeAsleep bool) string {
	if e.ID == selfID {
		return "self"
	}
	if e.URL == "" {
		return "no-url"
	}
	if !e.Alive && !includeAsleep {
		return "skip-asleep"
	}
	return "query"
}

// SearchFleet fans this sprite's /api/sessions/search out across the roster and
// merges the results, one slice per sprite. self is queried locally; peers via
// an authed GET (Bearer sprites token, to pass the org-login proxy). Dormant
// peers are skipped unless includeAsleep (then they're woken by the call, or
// reported skipped on a 502). Concurrent, bounded, with a per-call timeout so
// one slow/waking sprite can't stall the whole search.
func (s *Service) SearchFleet(ctx context.Context, query string, includeAsleep bool) (interface{}, error) {
	if strings.TrimSpace(query) == "" {
		return FleetSearchResult{Query: query, Sprites: []FleetSearchSprite{}}, nil
	}
	roster, err := s.roster(ctx)
	if err != nil {
		return nil, err
	}
	tok := s.GetSecret(ctx, SecretSpritesAPIToken)

	out := make([]FleetSearchSprite, len(roster))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8) // bound concurrent fan-out
	for i, e := range roster {
		out[i] = FleetSearchSprite{Sprite: e.ID, URL: e.URL, Present: e.Present}
		var base, bearer string
		switch peerSearchAction(e, s.id, includeAsleep) {
		case "self":
			out[i].Self = true
			base = "http://localhost:8080"
		case "query":
			base, bearer = strings.TrimRight(e.URL, "/"), tok
		case "skip-asleep":
			out[i].Skipped = true
			continue
		case "no-url":
			out[i].Error = "no url"
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, base, bearer string) {
			defer wg.Done()
			defer func() { <-sem }()
			body, code, err := getJSON(ctx, base+"/api/sessions/search?q="+url.QueryEscape(query), bearer)
			switch {
			case err != nil:
				out[i].Error = err.Error()
			case code == http.StatusBadGateway:
				out[i].Skipped = true // dormant / not reachable right now
			case code/100 != 2:
				out[i].Error = fmt.Sprintf("http %d", code)
			default:
				out[i].Hits = json.RawMessage(body)
			}
		}(i, base, bearer)
	}
	wg.Wait()
	return FleetSearchResult{Query: query, Sprites: out}, nil
}

// getJSON GETs a URL (optionally Bearer-authed) with a bounded timeout and
// returns the body + status.
func getJSON(ctx context.Context, url, bearer string) ([]byte, int, error) {
	ctx, cancel := context.WithTimeout(ctx, fleetSearchCallTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20)) // 4MB cap
	return body, resp.StatusCode, err
}
