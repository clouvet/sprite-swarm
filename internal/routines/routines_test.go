package routines

import (
	"context"
	"regexp"
	"sync"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

func TestStoreNoDefaultsAndCustomCRUD(t *testing.T) {
	s := newTestStore(t)

	// No built-in routines ship today.
	if got := s.List(); len(got) != 0 {
		t.Fatalf("expected 0 default tasks, got %+v", got)
	}

	c, err := s.Create("Slack digest", "Summarize #eng", 45)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if c.Kind != KindCustom || c.IntervalMin != 45 || !c.Enabled {
		t.Fatalf("unexpected custom task: %+v", c)
	}
	if got := s.List(); len(got) != 1 {
		t.Fatalf("expected 1 task after create, got %d", len(got))
	}

	// Missing name/prompt is rejected.
	if _, err := s.Create("", "x", 10); err == nil {
		t.Fatalf("expected error on empty name")
	}

	// Enable/disable persists.
	if _, err := s.Patch(c.ID, nil, nil, nil, boolp(false)); err != nil {
		t.Fatalf("Patch: %v", err)
	}
	if s.List()[0].Enabled {
		t.Fatalf("task should be disabled after patch")
	}

	// Delete the custom task.
	if err := s.Delete(c.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got := s.List(); len(got) != 0 {
		t.Fatalf("expected 0 tasks after delete, got %d", len(got))
	}
}

func TestStoreRecordRunPersists(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewStore(dir)
	c, _ := s.Create("Task", "do it", 30)
	now := time.Now()
	s.recordRun(c.ID, now, "the digest", "", "sess-1")

	s2, _ := NewStore(dir)
	got, _ := s2.Get(c.ID)
	if got.LastResult != "the digest" || got.LastStatus != StatusOK || got.LastSession != "sess-1" {
		t.Fatalf("run state did not persist: %+v", got)
	}
	// A later FAILED run preserves the previous digest as the diff baseline.
	s2.recordRun(c.ID, now, "", "boom", "sess-2")
	got, _ = s2.Get(c.ID)
	if got.LastResult != "the digest" || got.LastStatus != StatusError || got.LastSession != "sess-2" {
		t.Fatalf("failed run should preserve prior digest: %+v", got)
	}
}

func TestDue(t *testing.T) {
	now := time.Now()
	fresh := Task{Enabled: true, IntervalMin: 30, LastRun: now.Add(-10 * time.Minute)}
	if fresh.due(now) {
		t.Fatalf("task run 10m ago with 30m interval should not be due")
	}
	stale := Task{Enabled: true, IntervalMin: 30, LastRun: now.Add(-31 * time.Minute)}
	if !stale.due(now) {
		t.Fatalf("task run 31m ago with 30m interval should be due")
	}
	never := Task{Enabled: true, IntervalMin: 30}
	if !never.due(now) {
		t.Fatalf("never-run task should be due")
	}
	off := Task{Enabled: false, IntervalMin: 30}
	if off.due(now) {
		t.Fatalf("disabled task should never be due")
	}
	running := Task{Enabled: true, IntervalMin: 30, LastStatus: StatusRunning}
	if running.due(now) {
		t.Fatalf("running task should not re-fire")
	}
}

func TestRunTaskCapturesDigest(t *testing.T) {
	s := newTestStore(t)
	c, _ := s.Create("Task", "do it", 30)

	var mu sync.Mutex
	injected, registered := "", ""
	var haveResult bool
	deps := Deps{
		Inject: func(id, content string) error {
			mu.Lock()
			injected, haveResult = content, true
			mu.Unlock()
			return nil
		},
		Result: func(id string) (string, int64, bool) {
			mu.Lock()
			defer mu.Unlock()
			if !haveResult {
				return "", 0, false
			}
			return "REPO DIGEST: repoA PR #12 merged", 100, true
		},
		Register: func(id, name string) { registered = name },
		Timeout:  10 * time.Second,
	}
	svc := NewService(s, deps, time.Minute)
	svc.poll = 20 * time.Millisecond

	task, _ := s.Get(c.ID)
	svc.runTask(context.Background(), task)

	if injected == "" || registered == "" {
		t.Fatalf("expected inject + register (injected=%q registered=%q)", injected, registered)
	}
	got, _ := s.Get(c.ID)
	if got.LastResult != "REPO DIGEST: repoA PR #12 merged" || got.LastStatus != StatusOK {
		t.Fatalf("digest not captured: %+v", got)
	}
}

func TestRunTaskTimeoutNoResult(t *testing.T) {
	s := newTestStore(t)
	c, _ := s.Create("Task", "do it", 30)
	deps := Deps{
		Inject:   func(id, content string) error { return nil },
		Result:   func(id string) (string, int64, bool) { return "", 0, false }, // never produces
		Register: func(id, name string) {},
		Timeout:  60 * time.Millisecond,
	}
	svc := NewService(s, deps, time.Minute)
	svc.poll = 20 * time.Millisecond
	task, _ := s.Get(c.ID)
	svc.runTask(context.Background(), task)
	got, _ := s.Get(c.ID)
	if got.LastStatus != StatusError || got.LastError == "" {
		t.Fatalf("expected error status on timeout, got %+v", got)
	}
}

func TestNewSessionIDIsUniqueV4(t *testing.T) {
	a := newSessionID()
	if a == newSessionID() {
		t.Fatalf("each run must get a fresh session id")
	}
	re := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	if !re.MatchString(a) {
		t.Fatalf("not a valid v4 UUID: %q", a)
	}
}

// TestRunRollsPreviousSessionAndSeedsDigest checks that the second run seeds the first
// run's digest into its prompt and rolls the first run's session away.
func TestRunRollsPreviousSessionAndSeedsDigest(t *testing.T) {
	s := newTestStore(t)
	c, _ := s.Create("Task", "do the work", 30)
	var mu sync.Mutex
	var lastInjected, lastSession, deleted, digest string
	deps := Deps{
		Inject: func(id, content string) error {
			mu.Lock()
			lastInjected, lastSession, digest = content, id, "DIGEST run for "+id
			mu.Unlock()
			return nil
		},
		Result: func(id string) (string, int64, bool) {
			mu.Lock()
			defer mu.Unlock()
			if id != lastSession {
				return "", 0, false
			}
			return digest, 100, true
		},
		Register:      func(id, name string) {},
		DeleteSession: func(id string) { mu.Lock(); deleted = id; mu.Unlock() },
		Timeout:       5 * time.Second,
	}
	svc := NewService(s, deps, time.Minute)
	svc.poll = 10 * time.Millisecond

	task, _ := s.Get(c.ID)
	svc.runTask(context.Background(), task) // first run
	firstSession := lastSession
	if !contains(lastInjected, "first run") {
		t.Fatalf("first run should say it's the first run, got: %q", lastInjected)
	}

	task, _ = s.Get(c.ID)                   // reload: now has LastResult + LastSession
	svc.runTask(context.Background(), task) // second run
	if !contains(lastInjected, "PREVIOUS_SUMMARY") || !contains(lastInjected, "DIGEST run for "+firstSession) {
		t.Fatalf("second run should seed the previous digest, got: %q", lastInjected)
	}
	if deleted != firstSession {
		t.Fatalf("second run should roll away the first session %q, deleted %q", firstSession, deleted)
	}
}

func boolp(b bool) *bool { return &b }
func contains(s, sub string) bool {
	return len(s) >= len(sub) && indexOf(s, sub) >= 0
}
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
