package fleet

import (
	"context"
	"strings"
	"testing"
	"time"
	_ "time/tzdata" // deterministic zone loading in tests regardless of the host

	"github.com/clouvet/sprite-swarm/internal/config"
)

func TestHumanizeDuration(t *testing.T) {
	cases := map[time.Duration]string{
		45 * time.Minute:             "45m",
		6 * time.Hour:                "6h",
		6*time.Hour + 30*time.Minute: "6h 30m",
		25 * time.Hour:               "1d 1h",
		48 * time.Hour:               "2d",
	}
	for d, want := range cases {
		if got := humanizeDuration(d); got != want {
			t.Errorf("humanizeDuration(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestTimezoneDefaultAndSet(t *testing.T) {
	s := newService(newFakeBrain(), config.Config{AgentID: "a"})
	s.now = time.Now
	ctx := context.Background()

	if tz := s.Timezone(ctx); tz != defaultTZ {
		t.Errorf("default tz = %q, want %q", tz, defaultTZ)
	}
	if err := s.SetTimezone(ctx, "not/a/zone"); err == nil {
		t.Error("expected invalid timezone to be rejected")
	}
	if err := s.SetTimezone(ctx, "America/New_York"); err != nil {
		t.Fatalf("set valid tz: %v", err)
	}
	// New service reading the same brain should pick it up (fleet-wide).
	s2 := newService(s.brain, config.Config{AgentID: "b"})
	s2.now = time.Now
	if tz := s2.Timezone(ctx); tz != "America/New_York" {
		t.Errorf("persisted tz = %q, want America/New_York", tz)
	}
}

func TestTimeContext(t *testing.T) {
	s := newService(newFakeBrain(), config.Config{AgentID: "a"})
	now := time.Date(2026, 8, 10, 7, 32, 0, 0, time.UTC) // Hanoi = 14:32, UTC+7
	s.now = func() time.Time { return now }
	ctx := context.Background()

	first := s.timeContext(ctx)
	for _, want := range []string{"## Now", "14:32", "Asia/Ho_Chi_Minh", "UTC+7", "07:32 UTC"} {
		if !strings.Contains(first, want) {
			t.Errorf("first render missing %q in:\n%s", want, first)
		}
	}
	if strings.Contains(first, "since the previous turn") {
		t.Error("first render should have no gap note")
	}

	// A 15-hour jump (next day) surfaces the gap note.
	now = now.Add(15 * time.Hour)
	second := s.timeContext(ctx)
	if !strings.Contains(second, "15h") || !strings.Contains(second, "since the previous turn") {
		t.Errorf("expected a ~15h gap note, got:\n%s", second)
	}

	// A quick follow-up (5 min) does NOT nag.
	now = now.Add(5 * time.Minute)
	if third := s.timeContext(ctx); strings.Contains(third, "since the previous turn") {
		t.Errorf("small gap should not note, got:\n%s", third)
	}
}
