package routines

import (
	"context"
	"crypto/sha1"
	"fmt"
	"log"
	"strings"
	"time"
)

// Deps are the runtime hooks the scheduler needs, wired by main so this package stays
// free of hub/server imports. Inject runs a turn in a session; Result reads that
// session's latest assistant message (text, ts-millis, ok); Register labels the session
// in the UI list.
type Deps struct {
	Inject   func(sessionID, content string) error
	Result   func(sessionID string) (string, int64, bool)
	Register func(sessionID, name string)
	Timeout  time.Duration // max wait for one run to produce its digest
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

// runTask injects the task's prompt into its dedicated session, waits for the turn to
// settle, and records the resulting digest (or error).
func (s *Service) runTask(ctx context.Context, t Task) {
	sessionID := routineSessionID(t.ID)
	s.deps.Register(sessionID, "🔁 "+t.Name)
	s.store.setStatus(t.ID, StatusRunning)
	log.Printf("routines: running %q (%s)", t.Name, t.ID)

	_, beforeTS, _ := s.deps.Result(sessionID)
	if err := s.deps.Inject(sessionID, frame(t)); err != nil {
		s.store.recordRun(t.ID, time.Now(), "", "inject: "+err.Error())
		return
	}
	text, err := s.waitResult(ctx, sessionID, beforeTS)
	errStr := ""
	if err != nil {
		errStr = err.Error()
	}
	s.store.recordRun(t.ID, time.Now(), text, errStr)
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

// routineSessionID derives a stable, valid UUID (v5-style) for a task's dedicated
// session. It must be a real UUID — Claude rejects a non-UUID --session-id — and stable
// per task so the session persists across runs, letting the routine diff against its own
// previous digest in the transcript ("since last run, PR #12 merged").
func routineSessionID(taskID string) string {
	h := sha1.Sum([]byte("sprite-swarm/routines:" + taskID))
	var u [16]byte
	copy(u[:], h[:16])
	u[6] = (u[6] & 0x0f) | 0x50 // version 5
	u[8] = (u[8] & 0x3f) | 0x80 // RFC-4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", u[0:4], u[4:6], u[6:8], u[8:10], u[10:16])
}

// frame wraps a task's prompt with the routine contract: the agent's LAST message is
// captured as the digest and injected into future chats.
func frame(t Task) string {
	return "[Scheduled routine: " + t.Name + "] This runs automatically on a timer to keep you aware of " +
		"context outside your chats. Do the work below, then put a concise, self-contained summary as your " +
		"LAST message in this session — it is captured as standing context and injected into your future " +
		"chats, so write it for future-you. Do not dispatch or message anyone; just finish here.\n\n" + t.Prompt
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
