// Package watcher tails a Claude session .jsonl transcript so terminal activity
// (a `claude --resume <id>` session) surfaces in connected web clients —
// preserving v1's terminal co-presence. Lifted from claude-hub.
package watcher

import (
	"encoding/json"
	"strings"
	"time"
)

// ClaudeMessage is one line of a Claude .jsonl transcript.
type ClaudeMessage struct {
	Type        string          `json:"type"`
	UUID        string          `json:"uuid"`
	ParentUUID  string          `json:"parentUuid"`
	SessionID   string          `json:"sessionId"`
	Timestamp   string          `json:"timestamp"`
	Message     json.RawMessage `json:"message,omitempty"`
	IsSidechain bool            `json:"isSidechain"`
}

// ParsedMessage is a transcript line reduced to displayable content.
type ParsedMessage struct {
	Type      string
	Role      string // "user" or "assistant"
	Content   string
	Images    []string // data URLs for attached images, so history replays them
	Timestamp time.Time
	Raw       json.RawMessage
}

// ParseJSONLLine parses a single transcript line.
func ParseJSONLLine(line string) (*ClaudeMessage, error) {
	var msg ClaudeMessage
	if err := json.Unmarshal([]byte(line), &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

// ExtractContent pulls displayable text from a transcript message, or nil for
// message types/markers that should not be shown.
func ExtractContent(msg *ClaudeMessage) (*ParsedMessage, error) {
	if msg.Type == "file-history-snapshot" || msg.Type == "queue-operation" {
		return nil, nil
	}

	timestamp, _ := time.Parse(time.RFC3339, msg.Timestamp)
	parsed := &ParsedMessage{Type: msg.Type, Timestamp: timestamp}

	switch msg.Type {
	case "user":
		parsed.Role = "user"
		var content struct {
			Role    string      `json:"role"`
			Content interface{} `json:"content"`
		}
		if err := json.Unmarshal(msg.Message, &content); err == nil {
			switch v := content.Content.(type) {
			case string:
				parsed.Content = v
			case []interface{}:
				for _, block := range v {
					blockMap, ok := block.(map[string]interface{})
					if !ok {
						continue
					}
					switch blockMap["type"] {
					case "text":
						if text, ok := blockMap["text"].(string); ok {
							parsed.Content += text
						}
					case "image":
						if src, ok := blockMap["source"].(map[string]interface{}); ok {
							data, _ := src["data"].(string)
							mt, _ := src["media_type"].(string)
							if data != "" {
								if mt == "" {
									mt = "image/png"
								}
								parsed.Images = append(parsed.Images, "data:"+mt+";base64,"+data)
							}
						}
					}
				}
			}
		}
	case "assistant":
		parsed.Role = "assistant"
		var assistantMsg struct {
			Content json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(msg.Message, &assistantMsg); err == nil {
			var blocks []map[string]interface{}
			if err := json.Unmarshal(assistantMsg.Content, &blocks); err == nil {
				for _, block := range blocks {
					if block["type"] == "text" {
						if text, ok := block["text"].(string); ok {
							parsed.Content += text
						}
					}
				}
			}
		}
	}

	if len(parsed.Images) > 0 || (parsed.Content != "" && !shouldSkipMessage(parsed.Content)) {
		return parsed, nil
	}
	return nil, nil
}

// shouldSkipMessage filters internal Claude Code command/markers.
func shouldSkipMessage(content string) bool {
	markers := []string{
		"<local-command-caveat>",
		"<command-name>",
		"<command-message>",
		"<command-args>",
		"<local-command-stdout>",
		"<local-command-stderr>",
		"<system-reminder>",
	}
	for _, marker := range markers {
		if strings.HasPrefix(content, marker) {
			return true
		}
	}
	return false
}
