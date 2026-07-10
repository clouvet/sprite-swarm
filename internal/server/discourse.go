package server

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/clouvet/sprite-agent/internal/config"
)

// discourseRef is one Discourse topic the agent pulled into a conversation. It
// shows in the context panel as a title linking back to the post.
type discourseRef struct {
	Title string `json:"title"`
	URL   string `json:"url,omitempty"`
}

// discourseProfilePath is the generated @discourse/mcp profile (see
// cmd/sprite-agent/discourse.go). Its existence is the signal that the Discourse
// integration is set up on this sprite.
func (s *Server) discourseProfilePath() string {
	return filepath.Join(s.cfg.WorkDir, ".sprite-agent", "discourse-profile.json")
}

func (s *Server) discourseEnabled() bool {
	_, err := os.Stat(s.discourseProfilePath())
	return err == nil
}

// discourseSites returns the configured site base URLs (trailing slash trimmed).
func (s *Server) discourseSites() []string {
	b, err := os.ReadFile(s.discourseProfilePath())
	if err != nil {
		return nil
	}
	var p struct {
		AuthPairs []struct {
			Site string `json:"site"`
		} `json:"auth_pairs"`
	}
	if json.Unmarshal(b, &p) != nil {
		return nil
	}
	sites := make([]string, 0, len(p.AuthPairs))
	for _, a := range p.AuthPairs {
		if a.Site != "" {
			sites = append(sites, strings.TrimRight(a.Site, "/"))
		}
	}
	return sites
}

// sessionDiscourse scans the chat transcript for Discourse topics the agent read
// via the @discourse/mcp tools and returns them as linked titles. Empty unless
// the integration is configured and used — with no discourse tool calls there is
// nothing to show. It tracks the selected site (discourse_select_site) so a read
// result's id/slug becomes an absolute topic URL; with a single configured site
// that site is the default even if the agent never called select explicitly.
func (s *Server) sessionDiscourse(id string) []discourseRef {
	refs := []discourseRef{}
	if !s.discourseEnabled() {
		return refs
	}
	dir := config.ProjectsDirFor(filepath.Join(s.cfg.WorkDir, "chats", id))
	f, err := os.Open(filepath.Join(dir, id+".jsonl"))
	if err != nil {
		return refs
	}
	defer f.Close()

	currentSite := ""
	if sites := s.discourseSites(); len(sites) == 1 {
		currentSite = sites[0]
	}
	toolName := map[string]string{} // tool_use_id -> discourse tool name
	type key struct {
		site string
		id   int64
	}
	seen := map[key]bool{}

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		var msg struct {
			Type    string          `json:"type"`
			Message json.RawMessage `json:"message"`
		}
		if json.Unmarshal(sc.Bytes(), &msg) != nil {
			continue
		}
		switch msg.Type {
		case "assistant":
			var am struct {
				Content []struct {
					Type  string          `json:"type"`
					ID    string          `json:"id"`
					Name  string          `json:"name"`
					Input json.RawMessage `json:"input"`
				} `json:"content"`
			}
			if json.Unmarshal(msg.Message, &am) != nil {
				continue
			}
			for _, b := range am.Content {
				if b.Type != "tool_use" || !strings.Contains(b.Name, "discourse") {
					continue
				}
				toolName[b.ID] = b.Name
				// Track the active site so later read results can be linked.
				if strings.Contains(b.Name, "select_site") {
					var in struct {
						Site string `json:"site"`
					}
					if json.Unmarshal(b.Input, &in) == nil && in.Site != "" {
						currentSite = strings.TrimRight(in.Site, "/")
					}
				}
			}
		case "user":
			var um struct {
				Content []struct {
					Type      string          `json:"type"`
					ToolUseID string          `json:"tool_use_id"`
					Content   json.RawMessage `json:"content"`
				} `json:"content"`
			}
			if json.Unmarshal(msg.Message, &um) != nil {
				continue
			}
			for _, b := range um.Content {
				if b.Type != "tool_result" || !strings.Contains(toolName[b.ToolUseID], "read_topic") {
					continue
				}
				var topic struct {
					ID    int64  `json:"id"`
					Title string `json:"title"`
					Slug  string `json:"slug"`
				}
				if json.Unmarshal([]byte(toolResultText(b.Content)), &topic) != nil || topic.Title == "" {
					continue
				}
				k := key{currentSite, topic.ID}
				if seen[k] {
					continue
				}
				seen[k] = true
				ref := discourseRef{Title: topic.Title}
				if currentSite != "" && topic.Slug != "" {
					ref.URL = currentSite + "/t/" + topic.Slug + "/" + strconv.FormatInt(topic.ID, 10)
				}
				refs = append(refs, ref)
			}
		}
	}
	return refs
}

// toolResultText flattens a tool_result's content, which is either a bare string
// or an array of {type:"text", text:...} blocks (the @discourse/mcp shape).
func toolResultText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) == nil {
		var b strings.Builder
		for _, bl := range blocks {
			if bl.Type == "text" {
				b.WriteString(bl.Text)
			}
		}
		return b.String()
	}
	return ""
}
