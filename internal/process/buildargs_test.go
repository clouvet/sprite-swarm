package process

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func argsString(opts Options) string { return strings.Join(buildArgs(opts), " ") }

// writeTranscript drops a .jsonl for session id in a temp projects dir and
// returns the dir, so buildArgs can be exercised against on-disk state.
func writeTranscript(t *testing.T, id, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestResumeDecision(t *testing.T) {
	// A brand-new chat interrupted before Claude persisted anything leaves an
	// empty (or header-only) transcript. That is NOT resumable — resuming it
	// crash-loops the session — so buildArgs must create a fresh session.
	cases := []struct {
		name       string
		body       string
		wantResume bool
	}{
		{"empty file", "", false},
		{"header only", `{"type":"file-history-snapshot","id":"x"}` + "\n", false},
		{"real user turn", `{"type":"file-history-snapshot"}` + "\n" + `{"type":"user","message":{"role":"user","content":"hi"}}` + "\n", true},
		{"assistant turn", `{"type":"assistant","message":{"role":"assistant"}}` + "\n", true},
		{"garbage lines", "not json\n\n", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := writeTranscript(t, "s1", c.body)
			a := argsString(Options{SessionID: "s1", ProjectsDir: dir, DangerousSkip: true})
			gotResume := strings.Contains(a, "--resume s1")
			gotCreate := strings.Contains(a, "--session-id s1")
			if gotResume != c.wantResume || gotCreate == c.wantResume {
				t.Fatalf("body %q: wantResume=%v got --resume=%v --session-id=%v (%s)", c.body, c.wantResume, gotResume, gotCreate, a)
			}
		})
	}

	// No transcript at all → create a fresh session.
	a := argsString(Options{SessionID: "s1", ProjectsDir: t.TempDir(), DangerousSkip: true})
	if !strings.Contains(a, "--session-id s1") || strings.Contains(a, "--resume") {
		t.Fatalf("missing transcript should create, got: %s", a)
	}
}

func TestBuildArgsDangerousSkip(t *testing.T) {
	a := argsString(Options{SessionID: "s1", ProjectsDir: "/nonexistent", DangerousSkip: true})
	if !strings.Contains(a, "--dangerously-skip-permissions") {
		t.Fatalf("expected --dangerously-skip-permissions, got: %s", a)
	}
	if strings.Contains(a, "--permission-mode") {
		t.Fatalf("should not pass --permission-mode when skipping: %s", a)
	}
	// New session (no transcript) uses --session-id; streaming flags always present.
	if !strings.Contains(a, "--session-id s1") || !strings.Contains(a, "--include-partial-messages") {
		t.Fatalf("missing session-id/streaming flags: %s", a)
	}
}

func TestBuildArgsScopedWhenSkipOff(t *testing.T) {
	a := argsString(Options{SessionID: "s1", ProjectsDir: "/nonexistent", PermissionMode: "plan"})
	if strings.Contains(a, "--dangerously-skip-permissions") {
		t.Fatalf("should not skip when DangerousSkip is false: %s", a)
	}
	if !strings.Contains(a, "--permission-mode plan") {
		t.Fatalf("expected scoped --permission-mode plan: %s", a)
	}
}

func TestBuildArgsModel(t *testing.T) {
	// A chosen model is passed through as --model.
	a := argsString(Options{SessionID: "s1", ProjectsDir: "/nonexistent", DangerousSkip: true, Model: "opus"})
	if !strings.Contains(a, "--model opus") {
		t.Fatalf("expected --model opus, got: %s", a)
	}
	// The default (empty) omits the flag entirely so the CLI picks the default.
	b := argsString(Options{SessionID: "s1", ProjectsDir: "/nonexistent", DangerousSkip: true})
	if strings.Contains(b, "--model") {
		t.Fatalf("empty model should omit --model, got: %s", b)
	}
}
