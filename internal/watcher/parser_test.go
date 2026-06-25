package watcher

import "testing"

func TestExtractContentUser(t *testing.T) {
	line := `{"type":"user","timestamp":"2026-06-22T20:00:00Z","message":{"role":"user","content":"hello"}}`
	msg, err := ParseJSONLLine(line)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ExtractContent(msg)
	if err != nil || parsed == nil {
		t.Fatalf("expected parsed user message, got %v %v", parsed, err)
	}
	if parsed.Role != "user" || parsed.Content != "hello" {
		t.Fatalf("got role=%q content=%q", parsed.Role, parsed.Content)
	}
}

func TestExtractContentAssistant(t *testing.T) {
	line := `{"type":"assistant","timestamp":"2026-06-22T20:00:00Z","message":{"role":"assistant","content":[{"type":"text","text":"hi "},{"type":"text","text":"there"}]}}`
	msg, _ := ParseJSONLLine(line)
	parsed, err := ExtractContent(msg)
	if err != nil || parsed == nil {
		t.Fatalf("expected parsed assistant message, got %v %v", parsed, err)
	}
	if parsed.Role != "assistant" || parsed.Content != "hi there" {
		t.Fatalf("got role=%q content=%q", parsed.Role, parsed.Content)
	}
}

func TestExtractContentSkipsMarkers(t *testing.T) {
	line := `{"type":"user","timestamp":"2026-06-22T20:00:00Z","message":{"role":"user","content":"<system-reminder>internal</system-reminder>"}}`
	msg, _ := ParseJSONLLine(line)
	parsed, _ := ExtractContent(msg)
	if parsed != nil {
		t.Fatalf("expected nil for internal marker, got %+v", parsed)
	}
}

func TestExtractContentSkipsTaskNotifications(t *testing.T) {
	// Background-task notifications are injected as user turns; they must not render
	// as the human's message (the refresh-clobber bug). Both wrapper forms.
	cases := []string{
		`{"type":"user","timestamp":"2026-06-22T20:00:00Z","message":{"role":"user","content":"<task-notification>\n<task-id>x</task-id>\n</task-notification>"}}`,
		`{"type":"user","timestamp":"2026-06-22T20:00:00Z","message":{"role":"user","content":"[SYSTEM NOTIFICATION - NOT USER INPUT]\nbackground task done\n<task-notification></task-notification>"}}`,
	}
	for i, line := range cases {
		msg, _ := ParseJSONLLine(line)
		if parsed, _ := ExtractContent(msg); parsed != nil {
			t.Fatalf("case %d: expected task-notification to be skipped, got %+v", i, parsed)
		}
	}
}

func TestExtractContentSkipsNonDisplayable(t *testing.T) {
	line := `{"type":"file-history-snapshot","timestamp":"2026-06-22T20:00:00Z"}`
	msg, _ := ParseJSONLLine(line)
	parsed, _ := ExtractContent(msg)
	if parsed != nil {
		t.Fatalf("expected nil for file-history-snapshot, got %+v", parsed)
	}
}
