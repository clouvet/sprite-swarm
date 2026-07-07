package process

import "strings"

import "testing"

func argsString(opts Options) string { return strings.Join(buildArgs(opts), " ") }

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
