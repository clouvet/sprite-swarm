package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/clouvet/sprite-swarm/internal/config"
)

// writeTranscript lays down the profile (so the integration reads as enabled) and
// a transcript for session id under a temp HOME/WorkDir, returning a Server bound
// to it.
func discourseFixture(t *testing.T, id, transcript string, sites string) *Server {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	workDir := filepath.Join(home, "work")

	profileDir := filepath.Join(workDir, ".sprite-agent")
	if err := os.MkdirAll(profileDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profileDir, "discourse-profile.json"), []byte(sites), 0o600); err != nil {
		t.Fatal(err)
	}

	dir := config.ProjectsDirFor(filepath.Join(workDir, "chats", id))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(transcript), 0o644); err != nil {
		t.Fatal(err)
	}
	return &Server{cfg: config.Config{WorkDir: workDir}}
}

const twoSiteProfile = `{"auth_pairs":[{"site":"https://community.fly.io/"},{"site":"https://flyio.discourse.team"}]}`

func TestSessionDiscourse(t *testing.T) {
	// select community.fly.io, read topic 10119; then select the private site and
	// read topic 942. Two reads of 10119 to prove de-dup. Realistic mcp__-prefixed
	// tool names and a text-block tool_result.
	transcript := `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"a","name":"mcp__discourse__discourse_select_site","input":{"site":"https://community.fly.io/"}}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"b","name":"mcp__discourse__discourse_read_topic","input":{"topic_id":10119}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"b","content":[{"type":"text","text":"{\"id\":10119,\"title\":\"Clone a Fly machine\",\"slug\":\"clone-a-fly-machine\",\"posts\":[]}"}]}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"c","name":"mcp__discourse__discourse_read_topic","input":{"topic_id":10119}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"c","content":[{"type":"text","text":"{\"id\":10119,\"title\":\"Clone a Fly machine\",\"slug\":\"clone-a-fly-machine\"}"}]}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"d","name":"mcp__discourse__discourse_select_site","input":{"site":"https://flyio.discourse.team"}}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"e","name":"mcp__discourse__discourse_read_topic","input":{"topic_id":942}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"e","content":[{"type":"text","text":"{\"id\":942,\"title\":\"Prometheus on Fly\",\"slug\":\"prometheus-on-fly\"}"}]}]}}
`
	s := discourseFixture(t, "sess1", transcript, twoSiteProfile)
	refs := s.sessionDiscourse("sess1")
	if len(refs) != 2 {
		t.Fatalf("want 2 refs (deduped), got %d: %+v", len(refs), refs)
	}
	if refs[0].Title != "Clone a Fly machine" || refs[0].URL != "https://community.fly.io/t/clone-a-fly-machine/10119" {
		t.Errorf("ref[0] wrong: %+v", refs[0])
	}
	if refs[1].Title != "Prometheus on Fly" || refs[1].URL != "https://flyio.discourse.team/t/prometheus-on-fly/942" {
		t.Errorf("ref[1] wrong (site tracking): %+v", refs[1])
	}
}

func TestSessionDiscourseDisabled(t *testing.T) {
	// No profile file -> integration not set up -> empty, and no transcript scan.
	home := t.TempDir()
	t.Setenv("HOME", home)
	s := &Server{cfg: config.Config{WorkDir: filepath.Join(home, "work")}}
	if refs := s.sessionDiscourse("x"); len(refs) != 0 {
		t.Errorf("want empty when disabled, got %+v", refs)
	}
}

func TestSessionDiscourseSingleSiteDefault(t *testing.T) {
	// With one configured site, a read without an explicit select still links.
	transcript := `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"b","name":"mcp__discourse__discourse_read_topic","input":{"topic_id":5}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"b","content":[{"type":"text","text":"{\"id\":5,\"title\":\"Solo\",\"slug\":\"solo\"}"}]}]}}
`
	s := discourseFixture(t, "sess2", transcript, `{"auth_pairs":[{"site":"https://only.example.com"}]}`)
	refs := s.sessionDiscourse("sess2")
	if len(refs) != 1 || refs[0].URL != "https://only.example.com/t/solo/5" {
		t.Fatalf("single-site default failed: %+v", refs)
	}
}
