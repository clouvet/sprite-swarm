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
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
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

// writeIndex regenerates MEMORY.md: every memory file grouped by TOPIC category
// (the path segment after the author), across all sprites — so working on repo X
// means scanning the "repos" group, regardless of who wrote each note. Derived
// (not synced). Storage stays author-namespaced; the index gives the topic view.
func writeIndex(dir string) {
	groups := map[string][]string{}
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name := filepath.Base(p)
		if !strings.HasSuffix(name, ".md") || name == "MEMORY.md" {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		parts := strings.Split(filepath.ToSlash(rel), "/") // <author>/<category>/<file> or <author>/<file>
		cat := "general"
		if len(parts) >= 3 {
			cat = parts[1]
		}
		groups[cat] = append(groups[cat], filepath.ToSlash(rel))
		return nil
	})

	cats := make([]string, 0, len(groups))
	for c := range groups {
		cats = append(cats, c)
	}
	sort.Strings(cats)

	var b strings.Builder
	b.WriteString("# Fleet memory index\n\n")
	b.WriteString("Shared learnings across the fleet, grouped by topic. Read the relevant files before working; add your own.\n")
	for _, c := range cats {
		fmt.Fprintf(&b, "\n## %s\n", c)
		sort.Strings(groups[c])
		for _, f := range groups[c] {
			b.WriteString("- " + f + "\n")
		}
	}
	_ = os.WriteFile(filepath.Join(dir, "MEMORY.md"), []byte(b.String()), 0o644)
}
