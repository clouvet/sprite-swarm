package pi

import (
	"encoding/json"
	"strings"
)

// Translator turns Pi RPC events (from `pi --mode rpc` stdout) into the SAME
// Claude-Code stream-json events the hub + UI already consume, and accumulates a
// Claude-shaped .jsonl transcript. This is what keeps the rest of sprite-agent —
// UI rendering, history, search, resume — byte-for-byte unchanged when the backend
// is Pi rather than Claude Code: only the provider differs.
//
// It is a state machine because Claude's protocol is block-structured (a content
// block opens, streams deltas, closes) while Pi streams flat delta events. One
// Translator instance drives one session.
type Translator struct {
	sessionID   string
	initSent    bool
	blockIdx    int    // current claude content-block index
	openBlock   string // "", "text", or "thinking"
	assistant   []map[string]any // content blocks accumulated for the transcript
	textBuf     strings.Builder
	thinkBuf    strings.Builder
}

// NewTranslator builds a translator for a session id (used as the transcript's
// claude session_id, so the hub points history at the file pi-run writes).
func NewTranslator(sessionID string) *Translator { return &Translator{sessionID: sessionID} }

// ev is a convenience for a claude stream_event envelope the UI unwraps.
func streamEvent(inner map[string]any) map[string]any {
	return map[string]any{"type": "stream_event", "event": inner}
}

// Feed consumes one parsed Pi event and returns:
//   - uiEvents: Claude stream-json objects to write to pi-run's stdout (the hub reads
//     these exactly as if from `claude`).
//   - transcriptMsgs: complete Claude .jsonl message objects to append to the
//     transcript (assistant turns), if any.
func (t *Translator) Feed(ev map[string]any) (uiEvents []map[string]any, transcriptMsgs []map[string]any) {
	typ, _ := ev["type"].(string)

	// Emit a synthetic system/init once so the hub learns the session id and points
	// its transcript/history at <sessionID>.jsonl (which pi-run writes).
	if !t.initSent {
		t.initSent = true
		uiEvents = append(uiEvents, map[string]any{"type": "system", "subtype": "init", "session_id": t.sessionID})
	}

	switch typ {
	case "message_update":
		am, _ := ev["assistantMessageEvent"].(map[string]any)
		if am != nil {
			uiEvents = append(uiEvents, t.assistantDelta(am)...)
		}
	case "turn_end":
		// One LLM turn finished (there may be more within an agent run). Close any
		// open block and mark the assistant message boundary.
		uiEvents = append(uiEvents, t.closeOpenBlock()...)
		uiEvents = append(uiEvents, streamEvent(map[string]any{"type": "message_stop"}))
		if msg := t.flushAssistant(); msg != nil {
			transcriptMsgs = append(transcriptMsgs, msg)
		}
	case "agent_end", "agent_settled":
		// The whole agent run settled → Claude's terminal "result".
		uiEvents = append(uiEvents, t.closeOpenBlock()...)
		if msg := t.flushAssistant(); msg != nil {
			transcriptMsgs = append(transcriptMsgs, msg)
		}
		uiEvents = append(uiEvents, map[string]any{"type": "result", "subtype": "success"})
	case "agent_error", "extension_error":
		msg, _ := ev["error"].(string)
		if msg == "" {
			msg = "the Pi runtime reported an error"
		}
		uiEvents = append(uiEvents, map[string]any{"type": "result", "subtype": "error_during_execution", "is_error": true, "result": msg})
	}
	return uiEvents, transcriptMsgs
}

// assistantDelta maps a Pi assistantMessageEvent to claude content-block events and
// accumulates transcript content.
func (t *Translator) assistantDelta(am map[string]any) []map[string]any {
	var out []map[string]any
	sub, _ := am["type"].(string)
	switch sub {
	case "text_start":
		out = append(out, t.openText("text")...)
	case "text_delta":
		d, _ := am["delta"].(string)
		if t.openBlock != "text" {
			out = append(out, t.openText("text")...)
		}
		t.textBuf.WriteString(d)
		out = append(out, streamEvent(map[string]any{
			"type": "content_block_delta", "index": t.blockIdx,
			"delta": map[string]any{"type": "text_delta", "text": d},
		}))
	case "text_end":
		out = append(out, t.closeOpenBlock()...)
	case "thinking_start":
		out = append(out, t.openText("thinking")...)
	case "thinking_delta":
		d, _ := am["delta"].(string)
		if t.openBlock != "thinking" {
			out = append(out, t.openText("thinking")...)
		}
		t.thinkBuf.WriteString(d)
		out = append(out, streamEvent(map[string]any{
			"type": "content_block_delta", "index": t.blockIdx,
			"delta": map[string]any{"type": "thinking_delta", "thinking": d},
		}))
	case "thinking_end":
		out = append(out, t.closeOpenBlock()...)
	case "toolcall_end":
		// A complete tool call. Close any open text block, then emit a single tool_use
		// content block the UI renders via addTool(name, input).
		out = append(out, t.closeOpenBlock()...)
		tc, _ := am["toolCall"].(map[string]any)
		name, _ := tc["toolName"].(string)
		if name == "" {
			name, _ = tc["name"].(string)
		}
		input := tc["args"]
		if input == nil {
			input = tc["input"]
		}
		block := map[string]any{"type": "tool_use", "name": name, "input": input}
		out = append(out, streamEvent(map[string]any{
			"type": "content_block_start", "index": t.blockIdx, "content_block": block,
		}))
		out = append(out, streamEvent(map[string]any{"type": "content_block_stop", "index": t.blockIdx}))
		t.assistant = append(t.assistant, block)
		t.blockIdx++
	}
	return out
}

// openText opens a text/thinking content block (closing a prior one first).
func (t *Translator) openText(kind string) []map[string]any {
	var out []map[string]any
	if t.openBlock != "" && t.openBlock != kind {
		out = append(out, t.closeOpenBlock()...)
	}
	if t.openBlock == kind {
		return out
	}
	t.openBlock = kind
	out = append(out, streamEvent(map[string]any{
		"type": "content_block_start", "index": t.blockIdx,
		"content_block": map[string]any{"type": kind},
	}))
	return out
}

// closeOpenBlock closes the current text/thinking block (flushing its text into the
// transcript) and advances the block index.
func (t *Translator) closeOpenBlock() []map[string]any {
	if t.openBlock == "" {
		return nil
	}
	switch t.openBlock {
	case "text":
		if s := t.textBuf.String(); s != "" {
			t.assistant = append(t.assistant, map[string]any{"type": "text", "text": s})
		}
		t.textBuf.Reset()
	case "thinking":
		if s := t.thinkBuf.String(); s != "" {
			t.assistant = append(t.assistant, map[string]any{"type": "thinking", "thinking": s})
		}
		t.thinkBuf.Reset()
	}
	out := []map[string]any{streamEvent(map[string]any{"type": "content_block_stop", "index": t.blockIdx})}
	t.openBlock = ""
	t.blockIdx++
	return out
}

// flushAssistant returns the accumulated assistant message as a Claude .jsonl
// transcript entry (role assistant + content blocks), or nil if empty, and resets.
func (t *Translator) flushAssistant() map[string]any {
	if len(t.assistant) == 0 {
		return nil
	}
	msg := map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"role":    "assistant",
			"content": t.assistant,
		},
	}
	t.assistant = nil
	t.blockIdx = 0
	return msg
}

// UserTranscript builds a Claude .jsonl transcript entry for a user turn (so history
// and search see it exactly as they would a Claude user message).
func UserTranscript(content any) map[string]any {
	return map[string]any{
		"type": "user",
		"message": map[string]any{
			"role":    "user",
			"content": content,
		},
	}
}

// Marshal is a small helper so pi-run can write a compact JSON line.
func Marshal(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
