package hub

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/clouvet/sprite-swarm/internal/config"
	"github.com/clouvet/sprite-swarm/internal/watcher"
)

// A conversation created before the per-chat-workdir change has its transcript under the
// old base-cwd projects dir. resolveTranscriptPath must still find it (so it LOADS), and
// migrateOrphanTranscript must copy it into the per-chat layout (so it RESUMES).
func TestOrphanTranscriptLoadAndMigrate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h := &Hub{cfg: providers{workDir: "/home/sprite"}}
	const sid = "abc123-old"

	// Seed an orphan under the OLD base-cwd projects dir (not the per-chat one).
	oldProjects := config.ProjectsDirFor("/home/sprite")
	if err := os.MkdirAll(oldProjects, 0o755); err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(oldProjects, sid+".jsonl")
	body := `{"type":"user","message":{"role":"user","content":"an old discovery"}}` + "\n"
	if err := os.WriteFile(orphan, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	perChat := watcher.TranscriptPath(h.sessionProjectsDir(sid), sid)
	if fileExists(perChat) {
		t.Fatal("per-chat transcript should not exist before migration")
	}

	// Read-fallback: resolve points at the orphan so the chat still loads.
	if got := h.resolveTranscriptPath(sid, sid); got != orphan {
		t.Fatalf("resolve = %q, want orphan %q", got, orphan)
	}

	// Migrate-on-open copies it into the per-chat location.
	h.migrateOrphanTranscript(sid)
	if !fileExists(perChat) {
		t.Fatal("migrate should have created the per-chat transcript")
	}
	if got := h.resolveTranscriptPath(sid, sid); got != perChat {
		t.Fatalf("after migration resolve = %q, want per-chat %q", got, perChat)
	}
	if b, _ := os.ReadFile(perChat); !strings.Contains(string(b), "an old discovery") {
		t.Fatalf("migrated content mismatch: %q", string(b))
	}

	// Idempotent: a second open is a no-op and must not error or clobber.
	h.migrateOrphanTranscript(sid)

	// A brand-new session with no orphan resolves to its per-chat path (which may not
	// exist yet) — never a stray match.
	if got := h.resolveTranscriptPath("fresh-xyz", "fresh-xyz"); got != watcher.TranscriptPath(h.sessionProjectsDir("fresh-xyz"), "fresh-xyz") {
		t.Fatalf("no-orphan resolve should return the per-chat path, got %q", got)
	}
}
