// Package memsync keeps a local markdown "fleet memory" directory in sync with the
// shared brain, so recording a learning is a plain file write (like Claude Code's
// own memory) and every sprite boots already knowing what the fleet has learned.
//
// Layout mirrors the brain: <dir>/<author>/<file>.md  <->  fleet/memory-fs/<author>/<file>.md
//   - pull (boot + periodic): download every OTHER author's files into <dir>.
//   - push (on local change under <dir>/<self>/): upload this sprite's own memory.
//
// A sprite is authoritative over its own subdir (never pulled over), so there are
// no write conflicts: each sprite writes only under <dir>/<self>/ and reads all.
package memsync

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

const brainPrefix = "fleet/memory-fs/"

// Store is the brain subset memsync needs (satisfied by the fleet brain).
type Store interface {
	List(ctx context.Context, prefix string) ([]string, error)
	Get(ctx context.Context, key string) ([]byte, error)
	Put(ctx context.Context, key string, data []byte) error
	Delete(ctx context.Context, key string) error
}

// Run syncs dir with the brain until ctx is done (run in a goroutine). author is
// this sprite's id (its own writable namespace under dir).
func Run(ctx context.Context, store Store, dir, author string) {
	ownDir := filepath.Join(dir, author)
	if err := os.MkdirAll(ownDir, 0o755); err != nil {
		log.Printf("memsync: cannot create %s: %v", ownDir, err)
		return
	}
	sep := string(os.PathSeparator)
	ownPrefix := ownDir + sep

	relKey := func(p string) string { // local path -> brain key
		return brainPrefix + filepath.ToSlash(strings.TrimPrefix(p, dir+sep))
	}

	pull := func() {
		keys, err := store.List(ctx, brainPrefix)
		if err != nil {
			return
		}
		for _, k := range keys {
			rel := strings.TrimPrefix(k, brainPrefix)
			// Own namespace is locally authoritative — never overwrite it from the brain.
			if rel == "" || rel == author+"/" || strings.HasPrefix(rel, author+"/") {
				continue
			}
			data, err := store.Get(ctx, k)
			if err != nil {
				continue
			}
			p := filepath.Join(dir, filepath.FromSlash(rel))
			_ = os.MkdirAll(filepath.Dir(p), 0o755)
			_ = os.WriteFile(p, data, 0o644)
		}
		writeIndex(dir)
	}
	push := func(p string) {
		if !strings.HasPrefix(p, ownPrefix) {
			return // only this sprite's own memory is pushed
		}
		if data, err := os.ReadFile(p); err == nil {
			if err := store.Put(ctx, relKey(p), data); err != nil {
				log.Printf("memsync: push %s failed: %v", p, err)
			}
		}
	}
	remove := func(p string) {
		if strings.HasPrefix(p, ownPrefix) {
			_ = store.Delete(ctx, relKey(p))
		}
	}

	pull() // boot: inherit the fleet's learnings before anything else

	w, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("memsync: watcher unavailable: %v", err)
		return
	}
	defer w.Close()
	_ = w.Add(dir)
	_ = w.Add(ownDir)
	log.Printf("memsync: fleet memory at %s (write under %s)", dir, ownDir)

	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pull()
		case ev, ok := <-w.Events:
			if !ok {
				return
			}
			if ev.Op&(fsnotify.Write|fsnotify.Create) != 0 {
				if fi, err := os.Stat(ev.Name); err == nil && fi.IsDir() {
					if strings.HasPrefix(ev.Name+sep, ownPrefix) {
						_ = w.Add(ev.Name) // watch new subdirs under our own namespace
					}
					continue
				}
				push(ev.Name)
			}
			if ev.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
				remove(ev.Name)
			}
		case <-w.Errors:
		}
	}
}

// writeIndex regenerates MEMORY.md: a flat list of every memory file so a sprite
// can see at a glance what the fleet knows. Not synced (it's derived).
func writeIndex(dir string) {
	var b strings.Builder
	b.WriteString("# Fleet memory index\n\n")
	b.WriteString("Shared learnings across the fleet. Read the relevant files; add your own.\n\n")
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name := filepath.Base(p)
		if !strings.HasSuffix(name, ".md") || name == "MEMORY.md" {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		b.WriteString("- " + filepath.ToSlash(rel) + "\n")
		return nil
	})
	_ = os.WriteFile(filepath.Join(dir, "MEMORY.md"), []byte(b.String()), 0o644)
}
