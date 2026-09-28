// Package transcriptsync periodically backs a sprite's chat transcripts up to the
// shared brain, so a sprite can die (corruption, a bad reboot) and its conversations
// — where the real discoveries are made — are still recoverable. It is push-only and
// best-effort: uncommitted work and open PRs may be lost, but the chat histories are
// not.
//
// Design around suspension: a sprite pauses when idle, and transcripts only change
// while it is awake and working. So a plain periodic sweep is enough — the ticker
// only fires while the sprite runs (it is frozen on suspend), each sweep uploads the
// transcripts that changed since the last one, and nothing changes while paused. We
// deliberately do NOT hold the sprite awake to sync or stream continuously.
//
// Layout in the brain:
//
//	fleet/transcripts/<sprite-id>/<session-id>.jsonl.gz   one per chat, gzipped
//	fleet/transcripts/<sprite-id>/_sessions.json.gz       session metadata (names/models)
package transcriptsync

import (
	"bytes"
	"compress/gzip"
	"context"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// BrainPrefix is where a sprite's backed-up transcripts live (public so the recovery
// read-path can list/fetch them).
const BrainPrefix = "fleet/transcripts/"

// Store is the brain subset transcriptsync needs (satisfied by the fleet brain).
type Store interface {
	Put(ctx context.Context, key string, data []byte) error
	Delete(ctx context.Context, key string) error
}

// fileState is the size+mtime we last uploaded, so an unchanged transcript isn't
// re-gzipped and re-uploaded every sweep.
type fileState struct {
	size    int64
	modUnix int64
}

// Syncer periodically mirrors projectsRoot's transcripts (and the session metadata
// file) into the brain under BrainPrefix/<agentID>/.
type Syncer struct {
	store        Store
	projectsRoot string // ~/.claude/projects (holds <slug>/<session-id>.jsonl per chat)
	metaPath     string // session metadata JSON file ("" to skip)
	agentID      string
	interval     time.Duration
	include      func(sid string) bool // sessions to back up (nil = all); scratch chats opt out
	uploaded     map[string]fileState  // brain key -> last-uploaded state
}

// New builds a Syncer. interval <= 0 defaults to 3 minutes. include reports whether a
// given session id should be backed up (nil = back up everything); an excluded chat is
// skipped, and its brain copy removed if we'd previously uploaded it.
func New(store Store, projectsRoot, metaPath, agentID string, interval time.Duration, include func(sid string) bool) *Syncer {
	if interval <= 0 {
		interval = 3 * time.Minute
	}
	return &Syncer{
		store:        store,
		projectsRoot: projectsRoot,
		metaPath:     metaPath,
		agentID:      agentID,
		interval:     interval,
		include:      include,
		uploaded:     make(map[string]fileState),
	}
}

// Run sweeps until ctx is done (run in a goroutine). One sweep on start, then every
// interval — the ticker only advances while the sprite is awake, so this is exactly
// "sync periodically while active".
func (s *Syncer) Run(ctx context.Context) {
	if s.store == nil || s.projectsRoot == "" || s.agentID == "" {
		return
	}
	log.Printf("transcript-backup: every %s → %s%s/", s.interval, BrainPrefix, s.agentID)
	s.sweep(ctx)
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.sweep(ctx)
		}
	}
}

// sweep uploads every included transcript (and the metadata file) whose size/mtime
// changed since we last sent it, and removes the brain copy of any chat that has since
// been deleted locally or opted out of backup. Best-effort: a single failure is logged
// and skipped, never aborting the sweep.
func (s *Syncer) sweep(ctx context.Context) {
	seen := make(map[string]bool, len(s.uploaded)+8)
	_ = filepath.WalkDir(s.projectsRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".jsonl") {
			return nil
		}
		// The filename is the deterministic session id (<id>.jsonl); key flat by it so
		// recovery lists chats by id regardless of which per-cwd slug dir they sit in.
		sid := strings.TrimSuffix(d.Name(), ".jsonl")
		if s.include != nil && !s.include(sid) {
			return nil // scratch chat opted out — leave it for the reconcile below
		}
		key := BrainPrefix + s.agentID + "/" + sid + ".jsonl.gz"
		seen[key] = true
		s.upload(ctx, p, key)
		return nil
	})
	// Reconcile: a key we uploaded before but didn't sync this sweep is a chat that was
	// deleted locally or opted out — drop it from the brain so backups track the live,
	// still-wanted set. (Only a LIVE sprite prunes; a dead sprite's syncer isn't running,
	// so its backups persist — which is the whole point.)
	for key := range s.uploaded {
		if key == BrainPrefix+s.agentID+"/_sessions.json.gz" || seen[key] {
			continue
		}
		if err := s.store.Delete(ctx, key); err == nil {
			delete(s.uploaded, key)
		}
	}
	if s.metaPath != "" {
		s.upload(ctx, s.metaPath, BrainPrefix+s.agentID+"/_sessions.json.gz")
	}
}

// upload gzips path and Puts it at key, skipping when size+mtime are unchanged from
// the last upload of that key.
func (s *Syncer) upload(ctx context.Context, path, key string) {
	fi, err := os.Stat(path)
	if err != nil {
		return
	}
	st := fileState{size: fi.Size(), modUnix: fi.ModTime().UnixNano()}
	if prev, ok := s.uploaded[key]; ok && prev == st {
		return // unchanged since last sweep
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	gz, err := gzipBytes(data)
	if err != nil {
		return
	}
	if err := s.store.Put(ctx, key, gz); err != nil {
		log.Printf("transcript-backup: put %s failed: %v", key, err)
		return
	}
	s.uploaded[key] = st
}

func gzipBytes(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(data); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
