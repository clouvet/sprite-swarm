package transcriptsync

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type fakeStore struct {
	objs map[string][]byte
}

func newFakeStore() *fakeStore { return &fakeStore{objs: map[string][]byte{}} }
func (f *fakeStore) Put(_ context.Context, k string, d []byte) error {
	f.objs[k] = append([]byte(nil), d...)
	return nil
}
func (f *fakeStore) Delete(_ context.Context, k string) error { delete(f.objs, k); return nil }

func gunzip(t *testing.T, b []byte) string {
	t.Helper()
	r, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	out, _ := io.ReadAll(r)
	return string(out)
}

// writeTranscript drops a <slug>/<sid>.jsonl under root and returns its path.
func writeTranscript(t *testing.T, root, slug, sid, body string) string {
	t.Helper()
	dir := filepath.Join(root, slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, sid+".jsonl")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSweepUploadsChangedGzipped(t *testing.T) {
	root := t.TempDir()
	writeTranscript(t, root, "-home-sprite-chats-a", "a", `{"type":"user"}`)
	writeTranscript(t, root, "-home-sprite-chats-b", "b", `{"type":"assistant"}`)
	fs := newFakeStore()
	s := New(fs, root, "", "wk-1", time.Hour, nil)

	s.sweep(context.Background())
	if len(fs.objs) != 2 {
		t.Fatalf("objects = %d, want 2", len(fs.objs))
	}
	if got := gunzip(t, fs.objs["fleet/transcripts/wk-1/a.jsonl.gz"]); got != `{"type":"user"}` {
		t.Fatalf("a content = %q", got)
	}

	// Unchanged sweep: no new Puts (same bytes tracked).
	before := len(fs.objs)
	fs.objs = map[string][]byte{} // clear to detect a re-Put
	s.sweep(context.Background())
	if len(fs.objs) != 0 {
		t.Fatalf("unchanged sweep re-uploaded %d objects, want 0", len(fs.objs))
	}
	_ = before

	// Change one transcript → only it re-uploads.
	writeTranscript(t, root, "-home-sprite-chats-a", "a", `{"type":"user"}{"type":"user"}`)
	s.sweep(context.Background())
	if _, ok := fs.objs["fleet/transcripts/wk-1/a.jsonl.gz"]; !ok {
		t.Fatal("changed transcript a was not re-uploaded")
	}
	if _, ok := fs.objs["fleet/transcripts/wk-1/b.jsonl.gz"]; ok {
		t.Fatal("unchanged transcript b was re-uploaded")
	}
}

func TestExcludedChatIsSkippedAndPruned(t *testing.T) {
	root := t.TempDir()
	writeTranscript(t, root, "-home-sprite-chats-keep", "keep", `{"x":1}`)
	writeTranscript(t, root, "-home-sprite-chats-scratch", "scratch", `{"x":2}`)
	fs := newFakeStore()
	excluded := map[string]bool{}
	s := New(fs, root, "", "wk-1", time.Hour, func(sid string) bool { return !excluded[sid] })

	s.sweep(context.Background())
	if _, ok := fs.objs["fleet/transcripts/wk-1/scratch.jsonl.gz"]; !ok {
		t.Fatal("scratch should back up before it's excluded")
	}

	// Opt scratch out → next sweep skips it AND prunes its brain copy.
	excluded["scratch"] = true
	s.sweep(context.Background())
	if _, ok := fs.objs["fleet/transcripts/wk-1/scratch.jsonl.gz"]; ok {
		t.Fatal("excluded chat's brain copy must be pruned")
	}
	if _, ok := fs.objs["fleet/transcripts/wk-1/keep.jsonl.gz"]; !ok {
		t.Fatal("kept chat must remain backed up")
	}
}

func TestDeletedChatIsPruned(t *testing.T) {
	root := t.TempDir()
	p := writeTranscript(t, root, "-home-sprite-chats-gone", "gone", `{"x":1}`)
	fs := newFakeStore()
	s := New(fs, root, "", "wk-1", time.Hour, nil)
	s.sweep(context.Background())
	if _, ok := fs.objs["fleet/transcripts/wk-1/gone.jsonl.gz"]; !ok {
		t.Fatal("chat should be backed up first")
	}
	os.Remove(p) // chat deleted on the sprite
	s.sweep(context.Background())
	if _, ok := fs.objs["fleet/transcripts/wk-1/gone.jsonl.gz"]; ok {
		t.Fatal("deleted chat's brain copy must be pruned")
	}
}
