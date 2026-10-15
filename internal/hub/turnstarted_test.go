package hub

import (
	"strings"
	"testing"
)

// turnStartedInTranscript backs the sweep's retry-clear: it must say YES only once a
// delivered message's turn has actually begun responding (an assistant record after its
// user turn), so a missed confirm self-heals — but it must say NO for a message that was
// received yet produced nothing, so a stalled turn is never cleared (and lost).
func TestTurnStartedInTranscript(t *testing.T) {
	// A user turn followed by assistant output → started.
	withReply := strings.Join([]string{
		`{"type":"user","message":{"role":"user","content":"do the thing"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"on it"}]}}`,
	}, "\n")
	if !turnStartedInTranscript(strings.NewReader(withReply), "do the thing") {
		t.Fatal("a user turn followed by assistant output should count as started")
	}

	// The same transcript, but asking about a turn that is the LAST line with nothing
	// after it → not started (received, no output yet): must NOT be cleared.
	received := strings.Join([]string{
		`{"type":"user","message":{"role":"user","content":"do the thing"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"on it"}]}}`,
		`{"type":"user","message":{"role":"user","content":"and this too"}}`,
	}, "\n")
	if turnStartedInTranscript(strings.NewReader(received), "and this too") {
		t.Fatal("a received turn with no assistant output after it must NOT count as started")
	}

	// A tool_result (role user) after the turn is not assistant output; the turn is only
	// started once a real assistant record follows.
	toolThenAssistant := strings.Join([]string{
		`{"type":"user","message":{"role":"user","content":"run it"}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"ok"}]}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}`,
	}, "\n")
	if !turnStartedInTranscript(strings.NewReader(toolThenAssistant), "run it") {
		t.Fatal("an assistant record after the user turn (even past a tool_result) is started")
	}

	// Absent text, and empty text, are never started.
	if turnStartedInTranscript(strings.NewReader(withReply), "never sent this") {
		t.Fatal("a turn not in the transcript must not count as started")
	}
	if turnStartedInTranscript(strings.NewReader(withReply), "   ") {
		t.Fatal("empty text must not count as started")
	}
}
