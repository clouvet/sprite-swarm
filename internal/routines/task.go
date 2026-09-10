// Package routines runs per-sprite scheduled tasks ("Routines"): background agent
// turns that fire on a timer so a sprite stays aware of context happening OUTSIDE its
// chats — repos and PRs that moved, a Slack channel to summarize, and so on. Each run
// is an ordinary headless turn injected into a dedicated session; its final message is
// captured as a standing digest and injected into future chats' per-turn context, so a
// chat "already knows" where things stand without the human prompting a check.
//
// Every sprite has the same code-defined DEFAULT tasks plus its own CUSTOM tasks
// (created/deleted/listed by the human in chat via /api/tasks). This package is
// pure — it holds no reference to the hub or server; main wires the run/inject/result
// closures in.
package routines

import "time"

// Kinds of task. Default tasks ship in the binary and exist on every sprite; they can
// be disabled but not deleted. Custom tasks are created per-sprite by the human.
const (
	KindDefault = "default"
	KindCustom  = "custom"
)

// Run statuses recorded after each execution.
const (
	StatusOK      = "ok"
	StatusError   = "error"
	StatusRunning = "running"
)

// Task is one scheduled routine. The persisted run-state (LastRun/LastResult/…) is
// merged onto the code-defined defaults at load; custom tasks are persisted whole.
type Task struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Prompt      string    `json:"prompt"`
	IntervalMin int       `json:"interval_min"`
	Kind        string    `json:"kind"`
	Enabled     bool      `json:"enabled"`
	LastRun     time.Time `json:"last_run,omitempty"`
	LastResult  string    `json:"last_result,omitempty"`
	LastError   string    `json:"last_error,omitempty"`
	LastStatus  string    `json:"last_status,omitempty"`
	LastSession string    `json:"last_session,omitempty"`
}

// due reports whether the task should run now: never run yet, or at least its interval
// has elapsed since the last run. A running task is not re-fired.
func (t Task) due(now time.Time) bool {
	if !t.Enabled || t.LastStatus == StatusRunning {
		return false
	}
	if t.LastRun.IsZero() {
		return true
	}
	return now.Sub(t.LastRun) >= t.interval()
}

// interval is the run cadence, clamped to a sane floor so a misconfigured task can't
// hammer the agent every tick.
func (t Task) interval() time.Duration {
	m := t.IntervalMin
	if m < 5 {
		m = 5
	}
	return time.Duration(m) * time.Minute
}

// DefaultTasks returns the code-defined routines present on every sprite. There are
// none today: the built-in "context awareness" routine was removed because a scheduled
// LLM digest was the wrong mechanism (it barely ran while sprites slept, and narrating a
// diff from a prior digest produced stale, confabulated summaries). The Routines
// framework stays for human-created custom tasks; add a built-in here to ship one
// fleet-wide again.
func DefaultTasks() []Task {
	return nil
}
