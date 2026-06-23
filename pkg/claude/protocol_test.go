package claude

import (
	"encoding/json"
	"testing"
)

func TestIsStreamEvent(t *testing.T) {
	// A partial-message envelope: type=stream_event with an inner event.
	line := `{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"hi"}}}`
	var m StreamMessage
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !m.IsStreamEvent() {
		t.Fatal("expected IsStreamEvent true for stream_event envelope")
	}

	// The inner event must carry the top-level type the UI consumes.
	var inner struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(m.Event, &inner); err != nil {
		t.Fatalf("inner unmarshal: %v", err)
	}
	if inner.Type != "content_block_delta" {
		t.Fatalf("inner event type = %q, want content_block_delta", inner.Type)
	}
}

func TestIsStreamEventFalse(t *testing.T) {
	for _, line := range []string{
		`{"type":"result","subtype":"success"}`,
		`{"type":"assistant","message":{}}`,
		`{"type":"system","subtype":"init","session_id":"x"}`,
	} {
		var m StreamMessage
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("unmarshal %s: %v", line, err)
		}
		if m.IsStreamEvent() {
			t.Fatalf("expected IsStreamEvent false for %s", line)
		}
	}
}
