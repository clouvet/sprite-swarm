package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// slackClient calls the Slack Web API through the Sprites gateway connector by
// sprite identity — token-free (the gateway injects the OAuth token). base is the
// connector's gateway_base_url; methods are reached as <base>/<method>. It caches
// channel and user lookups so name/id resolution and readable output are cheap.
type slackClient struct {
	base   string
	http   *http.Client
	self   string            // cached auth.test user_id
	chans  map[string]string // lowercased channel name -> id
	users  map[string]string // user id -> display name
	loaded bool              // channel cache built
}

func newSlackClient(base string) *slackClient {
	return &slackClient{base: strings.TrimRight(base, "/"), http: &http.Client{Timeout: 30 * time.Second}}
}

// call invokes a Slack method. GET encodes params in the query; POST sends them
// as JSON. It returns the decoded response and an error if ok:false.
func (c *slackClient) call(ctx context.Context, method string, params map[string]string, post bool) (map[string]any, error) {
	var req *http.Request
	var err error
	if post {
		body, _ := json.Marshal(mapAny(params))
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/"+method, bytes.NewReader(body))
		if req != nil {
			req.Header.Set("Content-Type", "application/json")
		}
	} else {
		q := url.Values{}
		for k, v := range params {
			if v != "" {
				q.Set(k, v)
			}
		}
		u := c.base + "/" + method
		if e := q.Encode(); e != "" {
			u += "?" + e
		}
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	}
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("slack %s: bad response (%d)", method, resp.StatusCode)
	}
	if ok, _ := out["ok"].(bool); !ok {
		e, _ := out["error"].(string)
		if e == "" {
			e = fmt.Sprintf("http %d", resp.StatusCode)
		}
		return out, fmt.Errorf("slack %s: %s", method, e)
	}
	return out, nil
}

func mapAny(m map[string]string) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// selfID returns the connected user's id (auth.test), cached.
func (c *slackClient) selfID(ctx context.Context) (string, error) {
	if c.self != "" {
		return c.self, nil
	}
	r, err := c.call(ctx, "auth.test", nil, true)
	if err != nil {
		return "", err
	}
	c.self, _ = r["user_id"].(string)
	return c.self, nil
}

// loadChannels builds the name->id cache from public + private channels (paged).
func (c *slackClient) loadChannels(ctx context.Context) error {
	if c.loaded {
		return nil
	}
	c.chans = map[string]string{}
	cursor := ""
	for pages := 0; pages < 20; pages++ {
		r, err := c.call(ctx, "conversations.list", map[string]string{
			"limit": "1000", "exclude_archived": "true",
			"types": "public_channel,private_channel", "cursor": cursor,
		}, false)
		if err != nil {
			return err
		}
		for _, ch := range asList(r["channels"]) {
			m, _ := ch.(map[string]any)
			name, _ := m["name"].(string)
			id, _ := m["id"].(string)
			if name != "" && id != "" {
				c.chans[strings.ToLower(name)] = id
			}
		}
		cursor = nextCursor(r)
		if cursor == "" {
			break
		}
	}
	c.loaded = true
	return nil
}

// resolveChannel accepts a channel id (C…/G…/D…) or a name (with/without '#') and
// returns the id.
func (c *slackClient) resolveChannel(ctx context.Context, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("channel is required")
	}
	if isChannelID(ref) {
		return ref, nil
	}
	name := strings.ToLower(strings.TrimPrefix(ref, "#"))
	if err := c.loadChannels(ctx); err != nil {
		return "", err
	}
	if id, ok := c.chans[name]; ok {
		return id, nil
	}
	return "", fmt.Errorf("channel %q not found (is the connected user a member?)", ref)
}

func isChannelID(s string) bool {
	if len(s) < 8 || (s[0] != 'C' && s[0] != 'G' && s[0] != 'D') {
		return false
	}
	for _, r := range s {
		if !(r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

// userName resolves a user id to a display name (lazily loading users.list once).
func (c *slackClient) userName(ctx context.Context, id string) string {
	if id == "" {
		return ""
	}
	if c.users == nil {
		c.users = map[string]string{}
		cursor := ""
		for pages := 0; pages < 20; pages++ {
			r, err := c.call(ctx, "users.list", map[string]string{"limit": "1000", "cursor": cursor}, false)
			if err != nil {
				break
			}
			for _, u := range asList(r["members"]) {
				m, _ := u.(map[string]any)
				uid, _ := m["id"].(string)
				prof, _ := m["profile"].(map[string]any)
				name, _ := m["name"].(string)
				disp, _ := prof["display_name"].(string)
				real, _ := prof["real_name"].(string)
				best := firstNonEmpty(disp, real, name, uid)
				if uid != "" {
					c.users[uid] = best
				}
			}
			cursor = nextCursor(r)
			if cursor == "" {
				break
			}
		}
	}
	if n, ok := c.users[id]; ok {
		return n
	}
	return id
}

// history fetches messages in [oldest, latest], paging up to maxMsgs, newest-first
// from Slack; returns them oldest-first for readable summaries.
func (c *slackClient) history(ctx context.Context, channelID string, oldest, latest string, maxMsgs int) ([]map[string]any, bool, error) {
	var all []map[string]any
	cursor := ""
	truncated := false
	for {
		p := map[string]string{"channel": channelID, "limit": "200", "oldest": oldest, "latest": latest, "cursor": cursor}
		r, err := c.call(ctx, "conversations.history", p, false)
		if err != nil {
			return nil, false, err
		}
		for _, m := range asList(r["messages"]) {
			if mm, ok := m.(map[string]any); ok {
				all = append(all, mm)
			}
		}
		if len(all) >= maxMsgs {
			all = all[:maxMsgs]
			truncated = true
			break
		}
		cursor = nextCursor(r)
		if cursor == "" {
			break
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return tsFloat(all[i]) < tsFloat(all[j]) })
	return all, truncated, nil
}

// formatMessages renders messages as "HH:MM name: text" lines (resolving users).
func (c *slackClient) formatMessages(ctx context.Context, msgs []map[string]any) string {
	var b strings.Builder
	for _, m := range msgs {
		ts := tsFloat(m)
		who := c.userName(ctx, str(m["user"]))
		if who == "" {
			who = firstNonEmpty(str(m["username"]), str(m["bot_id"]), "?")
		}
		text := messageBody(m)
		when := time.Unix(int64(ts), 0).UTC().Format("2006-01-02 15:04")
		fmt.Fprintf(&b, "[%s] %s: %s", when, who, text)
		if rc, ok := m["reply_count"].(float64); ok && rc > 0 {
			fmt.Fprintf(&b, "  (thread: %d replies, thread_ts=%s)", int(rc), str(m["ts"]))
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// messageBody returns a message's readable content. Slack puts a lot of content
// (bot posts, alerts, link unfurls, Block Kit) in attachments[]/blocks[] rather
// than the top-level text, so we fall back to those, and list any uploaded files.
func messageBody(m map[string]any) string {
	var parts []string
	if t := strings.TrimSpace(str(m["text"])); t != "" {
		parts = append(parts, t)
	}
	for _, a := range asList(m["attachments"]) {
		if at, ok := a.(map[string]any); ok {
			if s := attachmentText(at); s != "" {
				parts = append(parts, s)
			}
		}
	}
	if len(parts) == 0 { // only reach into blocks if nothing else surfaced text
		if s := blocksText(asList(m["blocks"])); s != "" {
			parts = append(parts, s)
		}
	}
	for _, f := range asList(m["files"]) {
		if ff, ok := f.(map[string]any); ok {
			name := firstNonEmpty(str(ff["name"]), str(ff["title"]), "file")
			link := firstNonEmpty(str(ff["permalink"]), str(ff["url_private"]))
			parts = append(parts, strings.TrimSpace(fmt.Sprintf("📎 %s (%s) %s", name, str(ff["mimetype"]), link)))
		}
	}
	return strings.Join(parts, " | ")
}

// attachmentText pulls title + text/fallback + fields (and nested blocks) from one
// legacy message attachment.
func attachmentText(at map[string]any) string {
	var b []string
	if t := strings.TrimSpace(str(at["title"])); t != "" {
		b = append(b, t)
	}
	if body := firstNonEmpty(strings.TrimSpace(str(at["text"])), strings.TrimSpace(str(at["fallback"]))); body != "" && !strings.EqualFold(body, "[no preview available]") {
		b = append(b, body)
	}
	for _, f := range asList(at["fields"]) {
		if fm, ok := f.(map[string]any); ok {
			if kv := strings.TrimSpace(strings.TrimSpace(str(fm["title"])) + ": " + strings.TrimSpace(str(fm["value"]))); kv != ":" && kv != "" {
				b = append(b, kv)
			}
		}
	}
	if len(b) == 0 {
		if s := blocksText(asList(at["blocks"])); s != "" {
			b = append(b, s)
		}
	}
	return strings.Join(uniqStrings(b), " — ")
}

// blocksText recursively extracts text from Block Kit blocks/elements (section
// text, rich_text runs, etc.).
func blocksText(blocks []any) string {
	var out []string
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if t, ok := x["text"].(string); ok {
				if s := strings.TrimSpace(t); s != "" {
					out = append(out, s)
				}
			}
			for _, vv := range x {
				walk(vv)
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		}
	}
	for _, b := range blocks {
		walk(b)
	}
	return strings.Join(uniqStrings(out), " ")
}

// uniqStrings drops duplicate entries (Block Kit often repeats a run's text at
// multiple nesting levels), preserving order.
func uniqStrings(in []string) []string {
	seen := map[string]bool{}
	out := in[:0:0]
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// ---- small helpers ----

func asList(v any) []any { l, _ := v.([]any); return l }

func nextCursor(r map[string]any) string {
	if md, ok := r["response_metadata"].(map[string]any); ok {
		if c, ok := md["next_cursor"].(string); ok {
			return c
		}
	}
	return ""
}

func tsFloat(m map[string]any) float64 {
	f, _ := strconv.ParseFloat(str(m["ts"]), 64)
	return f
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

// unixFromRange computes a Slack `oldest` timestamp from hours/days-ago (0 = unset).
func unixFromRange(now time.Time, hours, days int) string {
	if hours <= 0 && days <= 0 {
		return ""
	}
	d := time.Duration(hours)*time.Hour + time.Duration(days)*24*time.Hour
	return strconv.FormatInt(now.Add(-d).Unix(), 10)
}
