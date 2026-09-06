package routines

import (
	"context"
	"crypto/rand"
	"crypto/sha1"
	"fmt"
	"log"
	"strings"
	"time"
)

// Deps are the runtime hooks the scheduler needs, wired by main so this package stays
// free of hub/server imports. Inject runs a turn in a session; Result reads that
// session's latest assistant message (text, ts-millis, ok); Register labels the session
// in the UI list; DeleteSession removes a session (used to roll the previous run away).
type Deps struct {
	Inject        func(sessionID, content string) error
	Result        func(sessionID string) (string, int64, bool)
	Register      func(sessionID, name string)
	DeleteSession func(sessionID string)
	Timeout       time.Duration // max wait for one run to produce its digest
}

// Service ticks over a sprite's routines and fires the ones that are due.
type Service struct {
	store *Store
	deps  Deps
	tick  time.Duration
	poll  time.Duration // how often waitResult re-reads the transcript
	busy  chan struct{} // capacity-1 guard: at most one run cycle at a time
}

// NewService builds the scheduler. tickEvery is how often due-ness is checked (a run
// only fires when a task's own interval has elapsed).
func NewService(store *Store, deps Deps, tickEvery time.Duration) *Service {
	if deps.Timeout <= 0 {
		deps.Timeout = 8 * time.Minute
	}
	if tickEvery <= 0 {
		tickEvery = time.Minute
	}
	return &Service{store: store, deps: deps, tick: tickEvery, poll: 3 * time.Second, busy: make(chan struct{}, 1)}
}

// Store exposes the underlying store for the HTTP CRUD handlers.
func (s *Service) Store() *Store { return s.store }

// Start runs the scheduler loop until ctx is cancelled. It waits a short settle delay
// before the first sweep so a freshly-booted sprite isn't immediately busy.
func (s *Service) Start(ctx context.Context) {
	select {
	case <-time.After(45 * time.Second):
	case <-ctx.Done():
		return
	}
	t := time.NewTicker(s.tick)
	defer t.Stop()
	s.sweep(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.sweep(ctx)
		}
	}
}

// sweep runs all due tasks, one at a time. If a previous sweep is still running (a slow
// task), this one is skipped — the next tick picks up whatever is still due.
func (s *Service) sweep(ctx context.Context) {
	select {
	case s.busy <- struct{}{}:
		defer func() { <-s.busy }()
	default:
		return
	}
	now := time.Now()
	for _, t := range s.store.List() {
		if !t.due(now) {
			continue
		}
		s.runTask(ctx, t)
		if ctx.Err() != nil {
			return
		}
	}
}

// RunNow fires a single task immediately, in the background, regardless of its schedule.
func (s *Service) RunNow(ctx context.Context, id string) error {
	t, ok := s.store.Get(id)
	if !ok {
		return fmt.Errorf("no task %q", id)
	}
	go func() {
		// Serialize against the regular sweep so two runs of the same task can't overlap.
		s.busy <- struct{}{}
		defer func() { <-s.busy }()
		s.runTask(ctx, t)
	}()
	return nil
}

// runTask runs one routine turn in a FRESH session, seeded with the previous run's
// stored digest so it can report what changed — then rolls the previous run's session
// away. Because the diff baseline comes from stored state (not the transcript), a run
// never replays accumulated history, and the human can delete the chat anytime without
// losing anything.
func (s *Service) runTask(ctx context.Context, t Task) {
	sessionID := newSessionID()
	prevSession := t.LastSession // the session the last run used, if any
	s.deps.Register(sessionID, "🔁 "+t.Name)
	s.store.setStatus(t.ID, StatusRunning)
	log.Printf("routines: running %q (%s) in %s", t.Name, t.ID, sessionID)

	if err := s.deps.Inject(sessionID, frame(t, t.LastResult)); err != nil {
		s.store.recordRun(t.ID, time.Now(), "", "inject: "+err.Error(), sessionID)
		return
	}
	text, err := s.waitResult(ctx, sessionID, 0) // fresh session: any assistant message is this run's
	errStr := ""
	if err != nil {
		errStr = err.Error()
	}
	s.store.recordRun(t.ID, time.Now(), text, errStr, sessionID)

	// Roll the previous run's chat away so a routine leaves just one rolling entry in
	// the list instead of accumulating one per run. Best-effort; 4h-old, no live process.
	if s.deps.DeleteSession != nil {
		switch {
		case prevSession != "" && prevSession != sessionID:
			s.deps.DeleteSession(prevSession)
		case prevSession == "":
			// First run under the fresh-session scheme: clean up the one chat the old
			// fixed-session version left behind (a no-op if there isn't one).
			s.deps.DeleteSession(legacyStableSessionID(t.ID))
		}
	}

	if errStr != "" {
		log.Printf("routines: %q finished with error: %s", t.ID, errStr)
	} else {
		log.Printf("routines: %q finished (%d chars)", t.ID, len(text))
	}
}

// waitResult polls the session transcript until a NEW assistant message appears (ts >
// beforeTS) and its timestamp stabilizes (the turn has stopped emitting), or the timeout
// elapses. Returns the final assistant text.
func (s *Service) waitResult(ctx context.Context, sessionID string, beforeTS int64) (string, error) {
	poll := s.poll
	deadline := time.Now().Add(s.deps.Timeout)
	var lastText string
	var lastTS int64
	stable := 0
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return lastText, ctx.Err()
		case <-time.After(poll):
		}
		text, ts, ok := s.deps.Result(sessionID)
		if !ok || ts <= beforeTS {
			continue // no new assistant message yet
		}
		if ts == lastTS {
			if stable++; stable >= 2 { // ~6s with no further output → turn settled
				return text, nil
			}
		} else {
			lastTS, lastText, stable = ts, text, 0
		}
	}
	if lastText != "" {
		return lastText, nil // timed out but we did get a digest; keep it
	}
	return "", fmt.Errorf("timed out after %s with no result", s.deps.Timeout)
}

// ContextDigest returns the standing digest from the context-awareness routine, framed
// for injection into per-turn chat context. Empty until the routine has run once.
func (s *Service) ContextDigest() string {
	t, ok := s.store.Get(ContextAwarenessID)
	if !ok || strings.TrimSpace(t.LastResult) == "" {
		return ""
	}
	age := "just now"
	if !t.LastRun.IsZero() {
		age = humanAge(time.Since(t.LastRun))
	}
	var b strings.Builder
	b.WriteString("## Standing context (from your background awareness routine, updated ")
	b.WriteString(age)
	b.WriteString(")\n")
	b.WriteString("You gathered this on a schedule so you already know where things stand. Something may have landed since — re-check if the human asks for the very latest.\n\n")
	b.WriteString(strings.TrimSpace(t.LastResult))
	b.WriteString("\n")
	return b.String()
}

// newSessionID mints a fresh random v4 UUID for a run's session. It must be a real UUID
// — Claude rejects a non-UUID --session-id — and a new one each run means no accumulated
// transcript is replayed to the model (the diff baseline comes from stored state instead).
func newSessionID() string {
	var u [16]byte
	_, _ = rand.Read(u[:])
	u[6] = (u[6] & 0x0f) | 0x40 // version 4
	u[8] = (u[8] & 0x3f) | 0x80 // RFC-4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", u[0:4], u[4:6], u[6:8], u[8:10], u[10:16])
}

// legacyStableSessionID reproduces the pre-change deterministic (v5) session id a task
// used to run under, so the first fresh-session run can delete that one leftover chat.
func legacyStableSessionID(taskID string) string {
	h := sha1.Sum([]byte("sprite-swarm/routines:" + taskID))
	var u [16]byte
	copy(u[:], h[:16])
	u[6] = (u[6] & 0x0f) | 0x50 // version 5
	u[8] = (u[8] & 0x3f) | 0x80 // RFC-4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", u[0:4], u[4:6], u[6:8], u[8:10], u[10:16])
}

// frame wraps a task's prompt with the routine contract and seeds the previous run's
// summary (from stored state) so the agent can report what changed — this runs in a
// fresh session with no prior history, so the baseline must be provided here.
func frame(t Task, prevDigest string) string {
	var b strings.Builder
	b.WriteString("[Scheduled routine: " + t.Name + "] This runs automatically on a timer to keep you aware of " +
		"context outside your chats. Do the work below, then put a concise, self-contained summary as your " +
		"LAST message in this session — it is captured as standing context and injected into your future " +
		"chats, so write it for future-you. Do not dispatch or message anyone; just finish here.\n\n")
	if d := strings.TrimSpace(prevDigest); d != "" {
		b.WriteString("Your previous run produced the summary below. Compare against it and LEAD with what has " +
			"changed since:\n<<<PREVIOUS_SUMMARY\n" + d + "\nPREVIOUS_SUMMARY>>>\n\n")
	} else {
		b.WriteString("This is your first run — there's no previous summary to diff against; produce a full baseline.\n\n")
	}
	b.WriteString(t.Prompt)
	return b.String()
}

// humanAge renders a coarse "N minutes/hours ago" for the digest header.
func humanAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d h ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%d d ago", int(d.Hours()/24))
	}
}
