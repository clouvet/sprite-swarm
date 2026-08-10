package fleet

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const (
	// tzKey is the brain config value holding the user's IANA timezone. It's
	// fleet-wide (set once, every sprite localizes the same way) and defaults to
	// Hanoi. Not a secret — lives beside the other fleet/config values.
	tzKey     = "fleet/config/timezone"
	defaultTZ = "Asia/Ho_Chi_Minh" // Hanoi (UTC+7)
	tzTTL     = 60 * time.Second    // cache so the per-turn context hook stays fast
)

// Timezone returns the configured IANA zone (default Hanoi), cached ~60s.
func (s *Service) Timezone(ctx context.Context) string {
	s.mu.Lock()
	if s.tzName != "" && s.now().Sub(s.tzFetched) < tzTTL {
		name := s.tzName
		s.mu.Unlock()
		return name
	}
	s.mu.Unlock()

	name := defaultTZ
	if b, err := s.brain.Get(ctx, tzKey); err == nil {
		if v := strings.TrimSpace(string(b)); v != "" {
			name = v
		}
	}
	s.mu.Lock()
	s.tzName, s.tzFetched = name, s.now()
	s.mu.Unlock()
	return name
}

// SetTimezone validates + persists the IANA zone fleet-wide (brain).
func (s *Service) SetTimezone(ctx context.Context, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("timezone is required")
	}
	if _, err := time.LoadLocation(name); err != nil {
		return fmt.Errorf("invalid IANA timezone %q (e.g. Asia/Ho_Chi_Minh, America/New_York)", name)
	}
	if err := s.brain.Put(ctx, tzKey, []byte(name)); err != nil {
		return err
	}
	s.mu.Lock()
	s.tzName, s.tzFetched = name, s.now()
	s.mu.Unlock()
	return nil
}

// timeContext renders the current-time block injected each turn: local time (in
// the user's zone) + UTC, plus a note when significant time has passed since the
// previous turn on this sprite (so the agent re-verifies state after e.g. an
// overnight gap instead of assuming the repo/deploys are unchanged).
func (s *Service) timeContext(ctx context.Context) string {
	zone := s.Timezone(ctx)
	loc, err := time.LoadLocation(zone)
	if err != nil {
		loc, zone = time.UTC, "UTC"
	}
	now := s.now()
	local := now.In(loc)
	_, off := local.Zone()

	var b strings.Builder
	fmt.Fprintf(&b, "## Now: %s (%s, UTC%+d) = %s UTC\n",
		local.Format("Mon 2006-01-02 15:04"), zone, off/3600, now.UTC().Format("2006-01-02 15:04"))

	s.mu.Lock()
	last := s.lastContext
	s.lastContext = now
	s.mu.Unlock()
	if !last.IsZero() {
		if gap := now.Sub(last); gap >= 20*time.Minute {
			fmt.Fprintf(&b, "~%s since the previous turn here — the working tree, deploys, or others' work may have moved; re-verify before assuming state.\n", humanizeDuration(gap))
		}
	}
	return b.String()
}

// humanizeDuration renders a gap as e.g. "45m", "6h", "1d 3h".
func humanizeDuration(d time.Duration) string {
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		h := int(d.Hours())
		if m := int(d.Minutes()) % 60; m >= 5 {
			return fmt.Sprintf("%dh %dm", h, m)
		}
		return fmt.Sprintf("%dh", h)
	}
	days := int(d.Hours()) / 24
	h := int(d.Hours()) % 24
	if h > 0 {
		return fmt.Sprintf("%dd %dh", days, h)
	}
	return fmt.Sprintf("%dd", days)
}
