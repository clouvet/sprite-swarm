package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/clouvet/sprite-swarm/internal/config"
)

// writeTranscript creates <root>/<slug>/<id>.jsonl with the given lines.
func writeTranscript(t *testing.T, root, id string, lines ...string) {
	t.Helper()
	dir := filepath.Join(root, "-chat-"+id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func userLine(text string) string {
	return `{"type":"user","message":{"role":"user","content":"` + text + `"}}`
}
func asstLine(text string) string {
	return `{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"` + text + `"}]}}`
}

func newTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	root := t.TempDir()
	cfg := config.Config{ClaudeProjectsDir: filepath.Join(root, "-slug")}
	s := &Server{cfg: cfg, store: newMetaStore(filepath.Join(t.TempDir(), "sessions.json"))}
	return s, root
}

func TestSearchSessions(t *testing.T) {
	s, root := newTestServer(t)

	writeTranscript(t, root, "aaaaaaaa", userLine("how do I set up Grafana dashboards"), asstLine("Use the grafana update_dashboard tool"))
	writeTranscript(t, root, "bbbbbbbb", userLine("unrelated chat about postgres"), asstLine("vacuum analyze"))
	writeTranscript(t, root, "cccccccc", userLine("GRAFANA again"), asstLine("more grafana talk here"))
	s.store.EnsureNamed("aaaaaaaa", "Grafana setup")
	s.store.EnsureNamed("cccccccc", "Metrics")
	// Deterministic ordering (real-time timestamps would flake at ms resolution).
	s.store.byID["aaaaaaaa"].LastMessageAt = 2000
	s.store.byID["cccccccc"].LastMessageAt = 1000

	hits := s.searchSessions("grafana", 50)
	if len(hits) != 2 {
		t.Fatalf("expected 2 hits, got %d: %+v", len(hits), hits)
	}
	got := map[string]SearchHit{}
	for _, h := range hits {
		got[h.SessionID] = h
	}
	if _, ok := got["bbbbbbbb"]; ok {
		t.Error("postgres chat should not match 'grafana'")
	}
	a := got["aaaaaaaa"]
	if a.Name != "Grafana setup" {
		t.Errorf("name = %q, want 'Grafana setup'", a.Name)
	}
	if a.Matches != 2 { // matches the user line and the assistant line
		t.Errorf("matches = %d, want 2", a.Matches)
	}
	if !strings.Contains(strings.ToLower(a.Snippet), "grafana") {
		t.Errorf("snippet missing term: %q", a.Snippet)
	}
	// aaaaaaaa was Touched (newer lastMessageAt) so it sorts first.
	if hits[0].SessionID != "aaaaaaaa" {
		t.Errorf("expected most-recent first, got %s", hits[0].SessionID)
	}
}

func TestSearchExcludesUnnamed(t *testing.T) {
	s, root := newTestServer(t)
	// A transcript whose body matches but which has NO store entry (a dispatched
	// or orphaned session) must be excluded — no "session <id>" clutter.
	writeTranscript(t, root, "eeeeeeee", userLine("lots of grafana talk here"))
	if hits := s.searchSessions("grafana", 50); len(hits) != 0 {
		t.Fatalf("expected unnamed session to be excluded, got %+v", hits)
	}
	// Once it's a named chat, it shows up.
	s.store.EnsureNamed("eeeeeeee", "Grafana notes")
	if hits := s.searchSessions("grafana", 50); len(hits) != 1 {
		t.Fatalf("expected the named session to match, got %+v", hits)
	}
}

func TestSearchNameOnlyMatch(t *testing.T) {
	s, root := newTestServer(t)
	// Body has no "deploy" but the NAME does — still surfaced.
	writeTranscript(t, root, "dddddddd", userLine("hello there"))
	s.store.EnsureNamed("dddddddd", "Deploy runbook")
	hits := s.searchSessions("deploy", 50)
	if len(hits) != 1 || hits[0].SessionID != "dddddddd" {
		t.Fatalf("name-only match failed: %+v", hits)
	}
	if hits[0].Matches != 0 {
		t.Errorf("expected 0 body matches, got %d", hits[0].Matches)
	}
}

func TestSearchEmptyQuery(t *testing.T) {
	s, _ := newTestServer(t)
	if hits := s.searchSessions("   ", 50); hits != nil {
		t.Errorf("blank query should return nil, got %+v", hits)
	}
}

func TestMakeSnippet(t *testing.T) {
	long := strings.Repeat("x", 200) + " needle " + strings.Repeat("y", 200)
	snip := makeSnippet(long, "needle")
	if !strings.Contains(snip, "needle") {
		t.Fatalf("snippet lost the term: %q", snip)
	}
	if !strings.HasPrefix(snip, "…") || !strings.HasSuffix(snip, "…") {
		t.Errorf("expected ellipses on both ends: %q", snip)
	}
	if len(snip) > 200 {
		t.Errorf("snippet too long (%d): %q", len(snip), snip)
	}
}
