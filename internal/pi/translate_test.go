package pi

import (
	"encoding/json"
	"testing"
)

// feed parses JSON pi-event lines and returns all UI events + transcript msgs.
func feed(t *testing.T, tr *Translator, lines ...string) ([]map[string]any, []map[string]any) {
	t.Helper()
	var ui, tx []map[string]any
	for _, l := range lines {
		var ev map[string]any
		if err := json.Unmarshal([]byte(l), &ev); err != nil {
			t.Fatalf("bad event %q: %v", l, err)
		}
		u, x := tr.Feed(ev)
		ui = append(ui, u...)
		tx = append(tx, x...)
	}
	return ui, tx
}

// innerTypes extracts, for each ui event, the inner event type (unwrapping
// stream_event) or the top-level type.
func innerTypes(evs []map[string]any) []string {
	var out []string
	for _, e := range evs {
		if e["type"] == "stream_event" {
			inner, _ := e["event"].(map[string]any)
			out = append(out, "stream:"+inner["type"].(string))
		} else {
			out = append(out, e["type"].(string))
		}
	}
	return out
}

func TestTranslateTextStreamToResult(t *testing.T) {
	tr := NewTranslator("sess-1")
	ui, tx := feed(t, tr,
		`{"type":"message_update","assistantMessageEvent":{"type":"text_start","contentIndex":0}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"Hello"}}`,
		`{"type":"message_update","assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":" world"}}`,
		`{"type":"turn_end"}`,
		`{"type":"agent_end"}`,
	)
	got := innerTypes(ui)
	// First event must be the synthetic system/init so the hub learns the session id.
	if got[0] != "system" {
		t.Fatalf("expected system/init first, got %v", got)
	}
	want := []string{"system", "stream:content_block_start", "stream:content_block_delta", "stream:content_block_delta", "stream:content_block_stop", "stream:message_stop", "result"}
	if len(got) != len(want) {
		t.Fatalf("event sequence = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("event[%d] = %q, want %q (full %v)", i, got[i], want[i], got)
		}
	}
	// The terminal result clears the spinner.
	last := ui[len(ui)-1]
	if last["type"] != "result" {
		t.Errorf("last event should be result, got %v", last)
	}
	// Transcript: one assistant message with the concatenated text.
	if len(tx) != 1 {
		t.Fatalf("expected 1 transcript msg, got %d", len(tx))
	}
	content := tx[0]["message"].(map[string]any)["content"].([]map[string]any)
	if content[0]["type"] != "text" || content[0]["text"] != "Hello world" {
		t.Errorf("transcript text = %v, want 'Hello world'", content)
	}
}

func TestTranslateToolCall(t *testing.T) {
	tr := NewTranslator("s")
	ui, _ := feed(t, tr,
		`{"type":"message_update","assistantMessageEvent":{"type":"toolcall_end","toolCall":{"toolName":"bash","args":{"command":"ls"}}}}`,
	)
	// Find the tool_use content_block_start.
	var found map[string]any
	for _, e := range ui {
		if e["type"] == "stream_event" {
			inner := e["event"].(map[string]any)
			if inner["type"] == "content_block_start" {
				cb, _ := inner["content_block"].(map[string]any)
				if cb["type"] == "tool_use" {
					found = cb
				}
			}
		}
	}
	if found == nil {
		t.Fatalf("no tool_use block emitted: %v", innerTypes(ui))
	}
	if found["name"] != "bash" {
		t.Errorf("tool name = %v, want bash", found["name"])
	}
}

func TestTranslateError(t *testing.T) {
	tr := NewTranslator("s")
	ui, _ := feed(t, tr, `{"type":"agent_error","error":"API Error: Overloaded"}`)
	last := ui[len(ui)-1]
	if last["type"] != "result" || last["is_error"] != true {
		t.Fatalf("expected an error result, got %v", last)
	}
	if last["result"] != "API Error: Overloaded" {
		t.Errorf("error text not carried through: %v", last["result"])
	}
}
