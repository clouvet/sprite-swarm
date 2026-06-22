// Package claude holds types for Claude Code's stream-json protocol
// (--output-format stream-json). Lifted from claude-hub and extended to handle
// the stream_event envelope emitted under --include-partial-messages.
package claude

import "encoding/json"

// StreamMessage is one line of Claude Code's stream-json output.
//
// Under --include-partial-messages, token-level events arrive wrapped as
// {"type":"stream_event","event":{...}}. The inner Event holds the real
// content_block_start / content_block_delta / message_stop the UI consumes, so
// callers unwrap stream_event and forward Event to clients (see Unwrap).
type StreamMessage struct {
	Type    string          `json:"type"`
	Message json.RawMessage `json:"message,omitempty"`
	Delta   json.RawMessage `json:"delta,omitempty"`

	// Envelope for --include-partial-messages streaming events.
	Event json.RawMessage `json:"event,omitempty"`

	// For system messages.
	Subtype   string `json:"subtype,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

// IsStreamEvent reports whether this message is a partial-message envelope.
func (m *StreamMessage) IsStreamEvent() bool {
	return m.Type == "stream_event" && len(m.Event) > 0
}

// UserMessage is a message sent to Claude over stream-json input.
type UserMessage struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"` // string or array of content blocks
}
