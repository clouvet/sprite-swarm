package server

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/clouvet/sprite-swarm/internal/config"
	"github.com/clouvet/sprite-swarm/internal/watcher"
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

// titleSystemPrompt frames the one-shot strictly as a title generator. Feeding a
// raw transcript to `claude -p` otherwise sometimes makes it *answer* or *continue*
// the conversation instead of titling it (that reply then became the "title").
const titleSystemPrompt = "You generate short chat titles. Given a conversation " +
	"transcript, reply with ONLY a concise 3-5 word title naming its topic. Never answer, " +
	"continue, or comment on the conversation, and never ask a question. Output just the " +
	"title — no quotes, no ending punctuation, no preamble."

// generateTitle runs a cheap one-shot Claude to summarize a conversation into a
// short title. Inherits the process env (Anthropic gateway on workers / OAuth on home).
func generateTitle(ctx context.Context, convo string) (string, error) {
	prompt := "Title this conversation:\n\n<conversation>\n" + convo + "\n</conversation>"
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, "claude", "--model", titleModel, "--append-system-prompt", titleSystemPrompt, "-p")
	cmd.Stdin = strings.NewReader(prompt)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	title := strings.TrimSpace(string(out))
	if i := strings.IndexByte(title, '\n'); i >= 0 {
		title = title[:i]
	}
	title = strings.TrimSpace(strings.Trim(strings.TrimSpace(title), "\"'"))
	// Reject a conversational reply (the model answered instead of titling): keep
	// the existing title rather than letting a sentence become the title.
	if !looksLikeTitle(title) {
		return "", fmt.Errorf("title generation returned a non-title response")
	}
	if len(title) > 60 {
		title = title[:60]
	}
	return title, nil
}

// looksLikeTitle rejects output that reads as the model answering/continuing the
// chat rather than titling it: real titles are short and don't trail off as a
// sentence or a question.
func looksLikeTitle(t string) bool {
	if t == "" {
		return false
	}
	if strings.HasSuffix(t, "?") || strings.HasSuffix(t, ":") {
		return false
	}
	if len(strings.Fields(t)) > 8 {
		return false
	}
	return true
}
