package hub

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/clouvet/sprite-swarm/internal/config"
	"github.com/clouvet/sprite-swarm/internal/session"
	"github.com/clouvet/sprite-swarm/internal/watcher"
)

// drain reads one JSON message off a client's send channel.
func drainMsg(t *testing.T, c *Client) map[string]interface{} {
	t.Helper()
	select {
	case b := <-c.send:
		var m map[string]interface{}
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("bad json: %v", err)
		}
		return m
	default:
		t.Fatal("expected a message on the client channel, got none")
		return nil
	}
}

// The rendered-history ETag: a matching client signature yields "history_nochange"
// (no re-send), and a changed transcript yields a fresh "history" with a new sig.
func TestHistoryETag(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := filepath.Join(home, "work")

	h := &Hub{cfg: providers{workDir: work}, sessions: map[string]*session.Session{}}
	sid := "sess-etag"
	dir := config.ProjectsDirFor(filepath.Join(work, "chats", sid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := watcher.TranscriptPath(dir, sid)
	base := `{"type":"user","message":{"role":"user","content":"hello"}}` + "\n" +
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hi there"}]}}` + "\n"
	if err := os.WriteFile(path, []byte(base), 0o644); err != nil {
		t.Fatal(err)
	}

	// First load (no client sig) → full history with a signature.
	c := &Client{sessionID: sid, send: make(chan []byte, 8)}
	h.sendHistoryToClient(c, sid, false, "")
	m1 := drainMsg(t, c)
	if m1["type"] != "history" {
		t.Fatalf("first load type = %v, want history", m1["type"])
	}
	if len(m1["messages"].([]interface{})) != 2 {
		t.Fatalf("messages = %v, want 2", m1["messages"])
	}
	sig, _ := m1["sig"].(string)
	if sig == "" {
		t.Fatal("history event carried no sig")
	}

	// Reconnect with the matching sig → history_nochange, no re-send of messages.
	h.sendHistoryToClient(c, sid, false, sig)
	m2 := drainMsg(t, c)
	if m2["type"] != "history_nochange" {
		t.Fatalf("matching sig type = %v, want history_nochange", m2["type"])
	}
	if _, hasMsgs := m2["messages"]; hasMsgs {
		t.Fatal("history_nochange must not carry messages")
	}

	// Transcript grows → sig changes → full history again.
	extra := `{"type":"user","message":{"role":"user","content":"another turn"}}` + "\n"
	if err := os.WriteFile(path, []byte(base+extra), 0o644); err != nil {
		t.Fatal(err)
	}
	h.sendHistoryToClient(c, sid, false, sig)
	m3 := drainMsg(t, c)
	if m3["type"] != "history" {
		t.Fatalf("after change type = %v, want history", m3["type"])
	}
	if m3["sig"] == sig {
		t.Fatal("sig should change when the rendered transcript changes")
	}
	if len(m3["messages"].([]interface{})) != 3 {
		t.Fatalf("messages = %v, want 3", m3["messages"])
	}
}
