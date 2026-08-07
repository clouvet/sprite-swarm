package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/clouvet/sprite-swarm/internal/gateway"
)

// runSlackMCP is the `sprite-agent slack-mcp` subcommand: a small stdio MCP server
// exposing Slack read/search + self-DM tools. It reaches Slack through the Sprites
// gateway connector by sprite identity — token-free, no secret. Wired into mcp.json
// at boot only when a slack connector is present (see setupSlackMCP). Speaks the
// MCP JSON-RPC subset Claude needs: initialize, tools/list, tools/call.
func runSlackMCP() {
	ctx := context.Background()
	base := gateway.SlackBase(ctx)
	if base == "" {
		fmt.Fprintln(os.Stderr, "slack-mcp: no slack connector configured for this org")
		os.Exit(1)
	}
	c := newSlackClient(base)
	srv := &mcpServer{client: c, tools: slackTools()}
	srv.serve()
}

// ---- MCP JSON-RPC over stdio ----

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"` // absent for notifications
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type mcpTool struct {
	Name        string
	Description string
	Schema      map[string]any
	Handle      func(ctx context.Context, c *slackClient, args map[string]any) (string, error)
}

type mcpServer struct {
	client *slackClient
	tools  []mcpTool
	out    *bufio.Writer
}

func (s *mcpServer) serve() {
	s.out = bufio.NewWriter(os.Stdout)
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for in.Scan() {
		line := in.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal(line, &req); err != nil {
			continue
		}
		s.dispatch(&req)
	}
}

func (s *mcpServer) reply(id json.RawMessage, result any) {
	if id == nil {
		return // notification — no response
	}
	s.write(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func (s *mcpServer) replyErr(id json.RawMessage, code int, msg string) {
	if id == nil {
		return
	}
	s.write(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": msg}})
}

func (s *mcpServer) write(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	s.out.Write(data)
	s.out.WriteByte('\n')
	s.out.Flush()
}

func (s *mcpServer) dispatch(req *rpcRequest) {
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		version := p.ProtocolVersion
		if version == "" {
			version = "2025-06-18"
		}
		s.reply(req.ID, map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "sprite-slack", "version": "1.0.0"},
		})
	case "notifications/initialized":
		// no-op notification
	case "ping":
		s.reply(req.ID, map[string]any{})
	case "tools/list":
		list := make([]map[string]any, 0, len(s.tools))
		for _, t := range s.tools {
			list = append(list, map[string]any{"name": t.Name, "description": t.Description, "inputSchema": t.Schema})
		}
		s.reply(req.ID, map[string]any{"tools": list})
	case "tools/call":
		s.handleToolCall(req)
	default:
		s.replyErr(req.ID, -32601, "method not found: "+req.Method)
	}
}

func (s *mcpServer) handleToolCall(req *rpcRequest) {
	var p struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &p); err != nil {
		s.replyErr(req.ID, -32602, "bad params")
		return
	}
	var tool *mcpTool
	for i := range s.tools {
		if s.tools[i].Name == p.Name {
			tool = &s.tools[i]
			break
		}
	}
	if tool == nil {
		s.toolResult(req.ID, "unknown tool: "+p.Name, true)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	text, err := tool.Handle(ctx, s.client, p.Arguments)
	if err != nil {
		s.toolResult(req.ID, "error: "+err.Error(), true)
		return
	}
	if strings.TrimSpace(text) == "" {
		text = "(no results)"
	}
	s.toolResult(req.ID, text, false)
}

func (s *mcpServer) toolResult(id json.RawMessage, text string, isErr bool) {
	s.reply(id, map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": isErr,
	})
}

// ---- arg helpers ----

func argStr(a map[string]any, k string) string { s, _ := a[k].(string); return strings.TrimSpace(s) }
func argInt(a map[string]any, k string) int {
	switch v := a[k].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return 0
}

// ---- tools ----

func slackTools() []mcpTool {
	return []mcpTool{
		{
			Name:        "slack_channels",
			Description: "List Slack channels (public + private the connected user is in) as 'name — id'. Optional case-insensitive name filter.",
			Schema: map[string]any{"type": "object", "properties": map[string]any{
				"filter": map[string]any{"type": "string", "description": "substring to filter channel names"},
			}},
			Handle: func(ctx context.Context, c *slackClient, a map[string]any) (string, error) {
				if err := c.loadChannels(ctx); err != nil {
					return "", err
				}
				filter := strings.ToLower(argStr(a, "filter"))
				var b strings.Builder
				for name, id := range c.chans {
					if filter == "" || strings.Contains(name, filter) {
						fmt.Fprintf(&b, "%s — %s\n", name, id)
					}
				}
				return b.String(), nil
			},
		},
		{
			Name:        "slack_history",
			Description: "Read a channel's messages over a time range for summaries. Give a channel (name '#dev' or id) and a range via hours OR days (e.g. days=7 for last week), or explicit oldest/latest unix timestamps. User ids are resolved to names.",
			Schema: map[string]any{"type": "object", "properties": map[string]any{
				"channel": map[string]any{"type": "string", "description": "channel name (#dev) or id"},
				"hours":   map[string]any{"type": "integer", "description": "look back this many hours"},
				"days":    map[string]any{"type": "integer", "description": "look back this many days"},
				"oldest":  map[string]any{"type": "string", "description": "explicit start unix ts"},
				"latest":  map[string]any{"type": "string", "description": "explicit end unix ts"},
				"limit":   map[string]any{"type": "integer", "description": "max messages (default 150, cap 400)"},
			}, "required": []string{"channel"}},
			Handle: func(ctx context.Context, c *slackClient, a map[string]any) (string, error) {
				id, err := c.resolveChannel(ctx, argStr(a, "channel"))
				if err != nil {
					return "", err
				}
				oldest := argStr(a, "oldest")
				if oldest == "" {
					oldest = unixFromRange(time.Now(), argInt(a, "hours"), argInt(a, "days"))
				}
				limit := argInt(a, "limit")
				if limit <= 0 {
					limit = 150
				}
				if limit > 400 {
					limit = 400
				}
				msgs, truncated, err := c.history(ctx, id, oldest, argStr(a, "latest"), limit)
				if err != nil {
					return "", err
				}
				out := fmt.Sprintf("%d messages in %s:\n%s", len(msgs), argStr(a, "channel"), c.formatMessages(ctx, msgs))
				if truncated {
					out += fmt.Sprintf("\n(truncated at %d messages — narrow the range for more)", limit)
				}
				return out, nil
			},
		},
		{
			Name:        "slack_thread",
			Description: "Read a full thread: all replies under a parent message. Give the channel and the parent message's thread_ts.",
			Schema: map[string]any{"type": "object", "properties": map[string]any{
				"channel":   map[string]any{"type": "string"},
				"thread_ts": map[string]any{"type": "string", "description": "parent message ts"},
			}, "required": []string{"channel", "thread_ts"}},
			Handle: func(ctx context.Context, c *slackClient, a map[string]any) (string, error) {
				id, err := c.resolveChannel(ctx, argStr(a, "channel"))
				if err != nil {
					return "", err
				}
				r, err := c.call(ctx, "conversations.replies", map[string]string{"channel": id, "ts": argStr(a, "thread_ts"), "limit": "200"}, false)
				if err != nil {
					return "", err
				}
				var msgs []map[string]any
				for _, m := range asList(r["messages"]) {
					if mm, ok := m.(map[string]any); ok {
						msgs = append(msgs, mm)
					}
				}
				return c.formatMessages(ctx, msgs), nil
			},
		},
		{
			Name:        "slack_search",
			Description: "Search Slack messages (as the connected user). Optionally scope to a channel; results include channel, author, text and ts.",
			Schema: map[string]any{"type": "object", "properties": map[string]any{
				"query":   map[string]any{"type": "string"},
				"channel": map[string]any{"type": "string", "description": "optional: limit to this channel"},
				"count":   map[string]any{"type": "integer", "description": "max results (default 20)"},
			}, "required": []string{"query"}},
			Handle: func(ctx context.Context, c *slackClient, a map[string]any) (string, error) {
				query := argStr(a, "query")
				if ch := argStr(a, "channel"); ch != "" {
					query = "in:" + strings.TrimPrefix(ch, "#") + " " + query
				}
				count := argInt(a, "count")
				if count <= 0 {
					count = 20
				}
				r, err := c.call(ctx, "search.messages", map[string]string{"query": query, "count": fmt.Sprint(count)}, false)
				if err != nil {
					return "", err
				}
				mm, _ := r["messages"].(map[string]any)
				var b strings.Builder
				for _, m := range asList(mm["matches"]) {
					x, _ := m.(map[string]any)
					ch, _ := x["channel"].(map[string]any)
					fmt.Fprintf(&b, "[#%s] %s: %s\n", str(ch["name"]), firstNonEmpty(str(x["username"]), c.userName(ctx, str(x["user"]))), messageBody(x))
				}
				return b.String(), nil
			},
		},
		{
			Name:        "slack_dm_self",
			Description: "Send a Slack direct message to YOURSELF (the connected user). This is the only posting tool and is hard-locked to your own DM — it cannot post to any other channel or person.",
			Schema: map[string]any{"type": "object", "properties": map[string]any{
				"text": map[string]any{"type": "string", "description": "message text"},
			}, "required": []string{"text"}},
			Handle: func(ctx context.Context, c *slackClient, a map[string]any) (string, error) {
				text := argStr(a, "text")
				if text == "" {
					return "", fmt.Errorf("text is required")
				}
				self, err := c.selfID(ctx)
				if err != nil {
					return "", err
				}
				open, err := c.call(ctx, "conversations.open", map[string]string{"users": self}, true)
				if err != nil {
					return "", err
				}
				ch, _ := open["channel"].(map[string]any)
				dm := str(ch["id"])
				if dm == "" {
					return "", fmt.Errorf("could not open self-DM")
				}
				res, err := c.call(ctx, "chat.postMessage", map[string]string{"channel": dm, "text": text}, true)
				if err != nil {
					return "", err
				}
				return "sent to your DM (ts " + str(res["ts"]) + ")", nil
			},
		},
	}
}
