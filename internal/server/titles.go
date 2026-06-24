package server

import (
	"bufio"
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/clouvet/sprite-agent/internal/config"
	"github.com/clouvet/sprite-agent/internal/watcher"
)

// titleModel is the cheap/fast model used for generating chat titles, so titling
// doesn't burn the main model. Falls back gracefully (the title just doesn't
// update) if unavailable.
const titleModel = "claude-haiku-4-5-20251001"

// retitle regenerates a session's title from its (recent) conversation via a cheap
// one-shot Claude call, so titles evolve continuously as the chat grows. Best-
// effort: failures leave the existing title untouched.
func (s *Server) retitle(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	convo := s.readConversation(id, 3000)
	if strings.TrimSpace(convo) == "" {
		http.Error(w, "no conversation yet", http.StatusBadRequest)
		return
	}
	title, err := generateTitle(r.Context(), convo)
	if err != nil || title == "" {
		http.Error(w, "title generation unavailable", http.StatusBadGateway)
		return
	}
	s.store.Rename(id, title)
	writeJSON(w, map[string]string{"name": title})
}

// readConversation reads the tail of a session's transcript as plain
// "role: text" lines (recent context → an evolving, relevant title).
func (s *Server) readConversation(id string, maxChars int) string {
	dir := config.ProjectsDirFor(filepath.Join(s.cfg.WorkDir, "chats", id))
	f, err := os.Open(filepath.Join(dir, id+".jsonl"))
	if err != nil {
		return ""
	}
	defer f.Close()

	var b strings.Builder
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		msg, err := watcher.ParseJSONLLine(sc.Text())
		if err != nil {
			continue
		}
		p, err := watcher.ExtractContent(msg)
		if err != nil || p == nil || p.Content == "" {
			continue
		}
		b.WriteString(p.Role)
		b.WriteString(": ")
		b.WriteString(p.Content)
		b.WriteString("\n")
	}
	out := b.String()
	if len(out) > maxChars {
		out = out[len(out)-maxChars:]
	}
	return out
}

// generateTitle runs a cheap one-shot Claude to summarize a conversation into a
// short title. Inherits the process env (Anthropic gateway on workers / OAuth on home).
func generateTitle(ctx context.Context, convo string) (string, error) {
	prompt := "Summarize the following chat as a concise 3-5 word title. " +
		"Reply with ONLY the title — no quotes, no trailing punctuation, no preamble.\n\n" + convo
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, "claude", "--model", titleModel, "-p")
	cmd.Stdin = strings.NewReader(prompt)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	title := strings.TrimSpace(string(out))
	if i := strings.IndexByte(title, '\n'); i >= 0 {
		title = title[:i]
	}
	title = strings.Trim(strings.TrimSpace(title), "\"'")
	if len(title) > 60 {
		title = title[:60]
	}
	return title, nil
}
