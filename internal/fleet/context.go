package fleet

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// FleetContext renders live fleet state for injection into the agent's context
// each turn (DESIGN §5 "inject live fleet state" + §2.4 presence-routing): the
// roster with status/liveness/presence, plus the shared-memory index. A
// UserPromptSubmit hook curls this so every turn the agent knows who exists,
// what they're doing, where the human is, and what durable memory exists.
//
// Presence-routing rule, made explicit to the agent: do NOT narrate or act on a
// worker a human is currently attached to — the human is steering it (§2.4).
// cwd is the working directory of the chat this turn belongs to (the hook passes
// it); when it's a git checkout with a PR, the current branch's live PR state is
// injected so the agent knows if it was merged/closed. Empty cwd → no PR section.
func (s *Service) FleetContext(ctx context.Context, memLimit int, cwd string) (string, error) {
	// Only the cheap, local/already-cached parts run in the request path: the time
	// block and the per-cwd branch-PR line (itself cached + background-refreshed). The
	// slow brain-backed part (roster + memory) is served from a background-refreshed
	// cache so a turn never waits on the brain.
	var b strings.Builder
	b.WriteString(s.timeContext(ctx))
	b.WriteString("\n")
	if pr := s.branchPRContext(cwd); pr != "" {
		b.WriteString(pr)
		b.WriteString("\n")
	}
	b.WriteString(s.fleetBody(ctx, memLimit))
	return b.String(), nil
}

// fleetBodyTTL is how stale the cached roster+memory block may get before a background
// refresh is kicked. A few seconds of roster staleness in ambient context is harmless;
// the heartbeat also refreshes it every ~30s.
const fleetBodyTTL = 25 * time.Second

// fleetBody returns the cached roster + memory block, kicking a background refresh when
// stale. The very first call (cold cache) builds synchronously so turn 1 isn't blank;
// every call after is served from cache instantly.
func (s *Service) fleetBody(ctx context.Context, memLimit int) string {
	s.fcMu.Lock()
	cold := s.fcAt.IsZero()
	body := s.fcBody
	stale := s.now().Sub(s.fcAt) > fleetBodyTTL
	refreshing := s.fcRefreshing
	if cold || (stale && !refreshing) {
		s.fcRefreshing = true
	}
	s.fcMu.Unlock()

	if cold {
		body = s.buildFleetBody(ctx, memLimit)
		s.storeFleetBody(body)
		return body
	}
	if stale && !refreshing {
		go func() {
			nb := s.buildFleetBody(context.Background(), memLimit)
			s.storeFleetBody(nb)
		}()
	}
	return body
}

// RefreshFleetBody rebuilds the cached roster+memory block now. Called off the request
// path (boot registration + each heartbeat tick) to keep the cache warm.
func (s *Service) RefreshFleetBody(ctx context.Context) {
	s.storeFleetBody(s.buildFleetBody(ctx, fleetMemLimit))
}

func (s *Service) storeFleetBody(body string) {
	s.fcMu.Lock()
	s.fcBody, s.fcAt, s.fcRefreshing = body, s.now(), false
	s.fcMu.Unlock()
}

// fleetMemLimit is the memory-index size used for background refreshes (the request
// path passes its own; they match in practice).
const fleetMemLimit = 50

// buildFleetBody renders the roster + shared-memory index — the brain-backed portion of
// the per-turn context. On a roster read error it returns an empty string (the cache
// keeps the last good body), so a transient brain hiccup never errors a turn.
func (s *Service) buildFleetBody(ctx context.Context, memLimit int) string {
	roster, err := s.roster(ctx)
	if err != nil {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "## Fleet (live) — you are %q\n", s.id)
	var attended []string
	for _, e := range roster {
		dot := "○" // not alive
		if e.Alive {
			dot = "●"
		}
		self := ""
		if e.ID == s.id {
			self = " (you)"
		}
		line := fmt.Sprintf("- %s%s · %s · %q", e.ID, self, dot, e.Phase)
		if e.Version != "" {
			line += " · " + e.Version
		} else if e.Build != "" {
			line += " · build " + e.Build
		}
		// Staleness is judged on the content hash (exact bytes), not the human version.
		if e.Build != "" && e.ID != s.id && s.build != "" && e.Build != s.build {
			line += " (stale)"
		}
		if e.Present && e.ID != s.id {
			line += "  👤 human attached → DEFER (don't act/narrate)"
			attended = append(attended, e.ID)
		}
		b.WriteString(line + "\n")
	}
	if len(attended) > 0 {
		fmt.Fprintf(&b, "A human is steering: %s — defer to them on those workers.\n", strings.Join(attended, ", "))
	}

	if mem, err := s.MemoryContext(ctx, memLimit); err == nil && mem != "" {
		b.WriteString("\n")
		b.WriteString(mem)
	}
	return b.String()
}
