package server

import (
	"bufio"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/clouvet/sprite-swarm/internal/watcher"
)

// SearchHit is one session that matched a query: its id/name, how many message
// lines matched, and a snippet of the first match for preview. Clicking it in the
// UI opens the session.
type SearchHit struct {
	SessionID string `json:"id"`
	Name      string `json:"name"`
	Snippet   string `json:"snippet"`
	Matches   int    `json:"matches"`
	UpdatedAt int64  `json:"lastMessageAt"`
}

// searchSessions scans every transcript on THIS sprite for query (case-insensitive
// substring over real user/assistant text — harness noise is filtered by the same
// parser the history replay uses) and returns matching sessions, most-recent
// first, capped at limit. A session whose name matches is included even with no
// body hit. This is the per-sprite half of fleet search; home fans out to peers.
func (s *Server) searchSessions(query string, limit int) []SearchHit {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil
	}

	// Names come from the meta store; transcripts come from the projects tree
	// (each chat runs in its own cwd, so transcripts are spread one-per-dir under
	// the projects root — glob catches them all, incl. sessions not in the store).
	names := map[string]string{}
	updated := map[string]int64{}
	for _, m := range s.store.List() {
		names[m.ID] = m.Name
		updated[m.ID] = m.LastMessageAt
	}

	root := filepath.Dir(s.cfg.ClaudeProjectsDir)
	files, _ := filepath.Glob(filepath.Join(root, "*", "*.jsonl"))

	hits := make([]SearchHit, 0, 16)
	for _, path := range files {
		id := strings.TrimSuffix(filepath.Base(path), ".jsonl")
		name := names[id]
		matches, snippet := scanTranscript(path, q)
		// A name match still surfaces the session even with no in-body hit.
		if matches == 0 && !strings.Contains(strings.ToLower(name), q) {
			continue
		}
		if name == "" {
			name = "session " + shortID(id)
		}
		if snippet == "" {
			snippet = name
		}
		hits = append(hits, SearchHit{SessionID: id, Name: name, Snippet: snippet, Matches: matches, UpdatedAt: updated[id]})
	}

	// Most-recently-active first; unknown timestamps (0) sink to the bottom.
	sort.Slice(hits, func(i, j int) bool { return hits[i].UpdatedAt > hits[j].UpdatedAt })
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}

// scanTranscript counts message lines containing q and returns a snippet of the
// first match. Errors (missing/unreadable file) yield (0, "").
func scanTranscript(path, qLower string) (int, string) {
	f, err := os.Open(path)
	if err != nil {
		return 0, ""
	}
	defer f.Close()

	matches := 0
	snippet := ""
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	for scanner.Scan() {
		msg, err := watcher.ParseJSONLLine(scanner.Text())
		if err != nil {
			continue
		}
		parsed, err := watcher.ExtractContent(msg)
		if err != nil || parsed == nil || parsed.Content == "" {
			continue
		}
		if !strings.Contains(strings.ToLower(parsed.Content), qLower) {
			continue
		}
		matches++
		if snippet == "" {
			snippet = makeSnippet(parsed.Content, qLower)
		}
	}
	return matches, snippet
}

// makeSnippet returns a one-line ~160-char window centred on the first match,
// with ellipses when truncated, so the UI can preview why a session matched.
func makeSnippet(content, qLower string) string {
	flat := strings.Join(strings.Fields(content), " ") // collapse newlines/runs
	idx := strings.Index(strings.ToLower(flat), qLower)
	if idx < 0 {
		if len(flat) > 160 {
			return flat[:160] + "…"
		}
		return flat
	}
	const pad = 70
	start := idx - pad
	if start < 0 {
		start = 0
	}
	end := idx + len(qLower) + pad
	if end > len(flat) {
		end = len(flat)
	}
	snippet := flat[start:end]
	if start > 0 {
		snippet = "…" + snippet
	}
	if end < len(flat) {
		snippet = snippet + "…"
	}
	return snippet
}

// serveSearch handles GET /api/sessions/search?q=<query>&limit=<n>, searching
// this sprite's own sessions. limit defaults to 50.
func (s *Server) serveSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query().Get("q")
	if strings.TrimSpace(q) == "" {
		writeJSON(w, []SearchHit{})
		return
	}
	writeJSON(w, s.searchSessions(q, 50))
}
