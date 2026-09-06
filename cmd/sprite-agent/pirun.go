package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/clouvet/sprite-swarm/internal/pi"
)

// runPiRun is the `sprite-agent pi-run` subcommand: a DROP-IN for the `claude` CLI
// that internally drives `pi --mode rpc` and translates between the two protocols,
// so the rest of sprite-agent (hub, UI, transcript-based history/search) is
// unchanged and only the model provider differs (experimental/pi-runtime branch).
//
// stdin  : Claude stream-json user lines  {"type":"user","message":{"role":"user","content":...}}
// stdout : Claude stream-json events      (content_block_*, message_stop, result, system/init)
// It also writes a Claude-shaped <projects-dir>/<session-id>.jsonl transcript.
func runPiRun(args []string) {
	fs := flag.NewFlagSet("pi-run", flag.ContinueOnError)
	sessionID := fs.String("session-id", "", "session id (also the transcript name)")
	projectsDir := fs.String("projects-dir", "", "dir for the <session-id>.jsonl transcript")
	workdir := fs.String("workdir", ".", "working directory for the agent")
	provider := fs.String("provider", "openai", "LLM provider (openai, anthropic, google)")
	model := fs.String("model", "", "model id (empty = provider default)")
	appendSystem := fs.String("append-system", "", "system-prompt text to prepend on the first turn")
	contextURL := fs.String("context-url", "http://localhost:8080/api/fleet/context", "per-turn fleet-context endpoint")
	if err := fs.Parse(args); err != nil {
		log.Fatalf("pi-run: %v", err)
	}
	if *sessionID == "" || *projectsDir == "" {
		log.Fatalf("pi-run: --session-id and --projects-dir are required")
	}

	pr := &piRunner{
		sessionID:    *sessionID,
		workdir:      *workdir,
		appendSystem: *appendSystem,
		contextURL:   *contextURL,
		transcript:   filepath.Join(*projectsDir, *sessionID+".jsonl"),
		tr:           pi.NewTranslator(*sessionID),
		out:          bufio.NewWriter(os.Stdout),
	}
	if err := pr.run(context.Background(), *provider, *model); err != nil {
		log.Fatalf("pi-run: %v", err)
	}
}

type piRunner struct {
	sessionID    string
	workdir      string
	appendSystem string
	contextURL   string
	transcript   string
	tr           *pi.Translator

	mu    sync.Mutex
	out   *bufio.Writer
	piIn  io.Writer
	first bool // whether the first prompt has been sent (to prepend the system prompt once)
}

func (p *piRunner) run(ctx context.Context, provider, model string) error {
	sessionDir := filepath.Join(p.workdir, ".pi", "sessions")
	_ = os.MkdirAll(sessionDir, 0o755)

	piBin := resolvePiBinary()
	args := []string{"--mode", "rpc", "--provider", provider, "--session-dir", sessionDir, "--name", p.sessionID}
	if model != "" {
		args = append(args, "--model", model)
	}
	cmd := exec.CommandContext(ctx, piBin, args...)
	cmd.Dir = p.workdir
	cmd.Env = os.Environ() // provider keys / models.json were prepared by the boot setup
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = os.Stderr
	p.piIn = stdin
	if err := cmd.Start(); err != nil {
		// Surface a launch failure to the chat (a "result" error) instead of exiting
		// silently — otherwise the turn just hangs with no explanation.
		p.emit([]map[string]any{{
			"type": "result", "subtype": "error_during_execution", "is_error": true,
			"result": fmt.Sprintf("Pi runtime failed to start (%s): %v — is the pi CLI installed?", piBin, err),
		}})
		return fmt.Errorf("start pi (%s): %w", piBin, err)
	}

	// stdin pump: Claude user lines from OUR stdin → pi prompts.
	go p.pumpUserInput(os.Stdin)

	// stdout pump: pi events → translated Claude events on OUR stdout + transcript.
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		var ev map[string]any
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		uiEvents, txMsgs := p.tr.Feed(ev)
		p.emit(uiEvents)
		p.appendTranscript(txMsgs)
	}
	return cmd.Wait()
}

// pumpUserInput reads Claude stream-json user lines and forwards each as a pi prompt,
// prepending the system prompt (first turn) and the live fleet context (every turn)
// — Pi has no UserPromptSubmit hook, so pi-run injects context itself.
func (p *piRunner) pumpUserInput(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var msg struct {
			Type    string `json:"type"`
			Message struct {
				Content any `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal([]byte(line), &msg) != nil || msg.Type != "user" {
			continue
		}
		text, images := extractUserContent(msg.Message.Content)

		// Record the user turn in the transcript (history/search parity).
		p.appendTranscript([]map[string]any{pi.UserTranscript(msg.Message.Content)})

		var prefix strings.Builder
		p.mu.Lock()
		if !p.first {
			p.first = true
			if p.appendSystem != "" {
				prefix.WriteString(p.appendSystem)
				prefix.WriteString("\n\n")
			}
		}
		p.mu.Unlock()
		if fc := p.fetchContext(); fc != "" {
			prefix.WriteString(fc)
			prefix.WriteString("\n\n")
		}

		prompt := map[string]any{"type": "prompt", "message": prefix.String() + text}
		if len(images) > 0 {
			prompt["images"] = images
		}
		p.mu.Lock()
		_, _ = p.piIn.Write(append(pi.Marshal(prompt), '\n'))
		p.mu.Unlock()
	}
}

// fetchContext pulls the per-turn fleet context (roster/memory/time/PR-state) so the
// Pi-backed agent has the same awareness a Claude one gets from the hook.
func (p *piRunner) fetchContext() string {
	u := p.contextURL + "?cwd=" + url.QueryEscape(p.workdir)
	resp, err := http.Get(u) //nolint:noctx // localhost, best-effort
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	return strings.TrimSpace(string(b))
}

func (p *piRunner) emit(evs []map[string]any) {
	if len(evs) == 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, e := range evs {
		p.out.Write(pi.Marshal(e))
		p.out.WriteByte('\n')
	}
	p.out.Flush()
}

func (p *piRunner) appendTranscript(msgs []map[string]any) {
	if len(msgs) == 0 {
		return
	}
	f, err := os.OpenFile(p.transcript, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	for _, m := range msgs {
		f.Write(pi.Marshal(m))
		f.Write([]byte{'\n'})
	}
}

// extractUserContent pulls plaintext + image blocks from a Claude user content value
// (a string, or an array of {type:text|image} blocks).
func extractUserContent(content any) (text string, images []map[string]any) {
	switch c := content.(type) {
	case string:
		return c, nil
	case []any:
		var b strings.Builder
		for _, blkAny := range c {
			blk, _ := blkAny.(map[string]any)
			switch blk["type"] {
			case "text":
				if s, _ := blk["text"].(string); s != "" {
					b.WriteString(s)
				}
			case "image":
				src, _ := blk["source"].(map[string]any)
				if src != nil {
					images = append(images, map[string]any{
						"type": "image", "data": src["data"], "mimeType": src["media_type"],
					})
				}
			}
		}
		return b.String(), images
	}
	return "", nil
}

// resolvePiBinary finds the pi CLI, preferring the global npm bin (bootstrap installs
// it there), then PATH.
func resolvePiBinary() string {
	for _, p := range []string{"/home/sprite/.npm-global/bin/pi", "/usr/local/bin/pi", "/usr/bin/pi"} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if p, err := exec.LookPath("pi"); err == nil {
		return p
	}
	return "pi"
}
