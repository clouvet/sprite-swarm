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

// ContextAwarenessID is the id of the built-in "keep aware of my repos/PRs" routine.
const ContextAwarenessID = "context-awareness"

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

// DefaultTasks returns the code-defined routines present on every sprite. There is one
// today (context awareness); add more here and they appear fleet-wide on next boot.
func DefaultTasks() []Task {
	return []Task{
		{
			ID:          ContextAwarenessID,
			Name:        "Context awareness",
			Kind:        KindDefault,
			Enabled:     true,
			IntervalMin: 30,
			Prompt:      contextAwarenessPrompt,
		},
	}
}

// contextAwarenessPrompt drives the default routine: survey the sprite's repos/PRs and
// note what moved remotely, producing a standing brief for future chats.
const contextAwarenessPrompt = `You are refreshing this sprite's awareness of work happening OUTSIDE your chats, so that when your human returns and asks "where do things stand?", you already know — without them having to prompt a check.

Survey, using your tools (bash, git, gh):
1. Repos this sprite works in. Look under /home/sprite/chats/*/ for git repos (each chat has its own working directory) and any other clones under /home/sprite. For each: the repo (remote/name), current branch, and any uncommitted or unpushed local work.
2. Pull requests. For each active repo run ` + "`gh pr list --state all --json number,title,state,headRefName,updatedAt,url`" + ` (and ` + "`gh pr view`" + ` when useful) to see open/merged/closed PRs — especially ones authored here or on branches you've worked on.
3. What MOVED since your last run. Compare against your previous digest earlier in this conversation: did a PR get merged or closed? Did new PRs or new commits land on the default branch? Did CI status flip? Call these out explicitly (e.g. "since last run: repoA PR #12 merged, PR #14 opened").
4. Anything else locally worth knowing: a failing build, a notable new file.

Keep it efficient — a ` + "`git fetch`" + ` per active repo plus ` + "`gh pr list`" + ` is enough; don't pull large histories or clone anything new. If a repo has no remote movement, say so in one line.

Produce your digest as your FINAL message: a concise, skimmable brief organized by repo, leading with what changed since last time. This digest is injected verbatim into your future chats, so write it for future-you and your human — no preamble, just the current state of the world.`
