package process

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/clouvet/sprite-swarm/pkg/claude"
)

// Options configures how a headless Claude process is launched: deterministic
// session id, token-level streaming, and the fleet's permission posture.
type Options struct {
	SessionID      string // deterministic; also the transcript filename
	CWD            string
	ProjectsDir    string // used to decide --resume vs --session-id
	DangerousSkip  bool   // --dangerously-skip-permissions (fleet default); else --permission-mode
	PermissionMode string // --permission-mode when not skipping (e.g. acceptEdits, plan)
	SettingsPath   string // --settings <file> when non-empty
	MCPConfigPath  string // --mcp-config <file> when non-empty
	AppendSystem   string // --append-system-prompt when non-empty (fleet affordance, DESIGN §5)
	Model          string // --model <model> when non-empty; "" uses the CLI default
	// ExtraEnv is "NAME=VALUE" pairs injected into the Claude process environment
	// (worker-scoped secrets), so tools/apps the agent runs inherit them.
	ExtraEnv []string
}

// HeadlessProcess is a supervised, long-lived `claude` process driven over
// stream-json stdin/stdout. One process serves all turns of one session.
type HeadlessProcess struct {
	SessionID    string
	CWD          string
	Model        string // model this process was launched with ("" = CLI default)
	Cmd          *exec.Cmd
	Stdin        io.WriteCloser
	StartedAt    time.Time
	IsGenerating bool

	OutputChan chan *claude.StreamMessage
	ErrorChan  chan error

	transcript   string        // on-disk transcript path (for resume-failure cleanup)
	resumeFailed bool          // set (under mu) if claude reported the session isn't resumable
	stderrDone   chan struct{} // closed when readStderr drains (so resumeFailed is settled)

	ctx    context.Context
	cancel context.CancelFunc
	mu     sync.RWMutex
}

// buildArgs assembles the claude CLI invocation. If the transcript already
// exists we resume it (terminal/web co-presence, restart recovery); otherwise
// we start a new session with the deterministic id.
func buildArgs(opts Options) []string {
	args := []string{
		"--print",
		"--verbose",
		"--output-format", "stream-json",
		"--input-format", "stream-json",
		"--include-partial-messages",
	}
	// The fleet runs with --dangerously-skip-permissions by default: every sprite
	// is an identical, isolated microVM doing autonomous work, and a capable fleet
	// shouldn't stall on permission prompts. This is fleet-wide (no home/worker
	// distinction — every sprite is the same). Set the scoped path explicitly to
	// opt back into --permission-mode. --settings is kept either way (it carries
	// the per-turn fleet-context hook); under skip its allow/deny are moot.
	if opts.DangerousSkip {
		args = append(args, "--dangerously-skip-permissions")
	} else {
		mode := opts.PermissionMode
		if mode == "" {
			mode = "acceptEdits"
		}
		args = append(args, "--permission-mode", mode)
	}
	if opts.SettingsPath != "" {
		args = append(args, "--settings", opts.SettingsPath)
	}
	if opts.MCPConfigPath != "" {
		args = append(args, "--mcp-config", opts.MCPConfigPath)
	}
	if opts.AppendSystem != "" {
		args = append(args, "--append-system-prompt", opts.AppendSystem)
	}
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}

	transcript := opts.ProjectsDir + "/" + opts.SessionID + ".jsonl"
	if resumableTranscript(transcript) {
		args = append(args, "--resume", opts.SessionID)
	} else {
		args = append(args, "--session-id", opts.SessionID)
	}
	return args
}

// resumableTranscript reports whether the on-disk transcript holds a real
// conversation we can hand to `claude --resume`. A file that merely EXISTS is
// not enough: interrupting the first turn of a brand-new chat (stop the stream
// before Claude persists anything) can leave an empty or header-only .jsonl,
// and resuming that makes Claude exit with "No conversation found with session
// ID" — crash-looping the session on every subsequent message. We treat a
// transcript as resumable only once it contains at least one user/assistant
// message line; otherwise we start a fresh session under the same id.
func resumableTranscript(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false // no file (or unreadable) → start a fresh session
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		var line struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			continue
		}
		if line.Type == "user" || line.Type == "assistant" {
			return true
		}
	}
	// A line longer than the buffer only happens once there's a substantial
	// (assistant) turn on disk — treat that as resumable rather than nuking it.
	if errors.Is(scanner.Err(), bufio.ErrTooLong) {
		return true
	}
	return false
}

// NewHeadlessProcess starts a headless Claude process for a session.
func NewHeadlessProcess(opts Options) (*HeadlessProcess, error) {
	ctx, cancel := context.WithCancel(context.Background())

	cwd := opts.CWD
	if cwd == "" {
		cwd = "/home/sprite"
	}

	args := buildArgs(opts)
	execCmd := exec.CommandContext(ctx, "claude", args...)
	execCmd.Dir = cwd
	execCmd.Env = append(os.Environ(), opts.ExtraEnv...)

	stdin, err := execCmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := execCmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := execCmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}

	if err := execCmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start claude: %w", err)
	}

	hp := &HeadlessProcess{
		SessionID:  opts.SessionID,
		CWD:        cwd,
		Model:      opts.Model,
		Cmd:        execCmd,
		Stdin:      stdin,
		StartedAt:  time.Now(),
		OutputChan: make(chan *claude.StreamMessage, 256),
		ErrorChan:  make(chan error, 10),
		transcript: opts.ProjectsDir + "/" + opts.SessionID + ".jsonl",
		stderrDone: make(chan struct{}),
		ctx:        ctx,
		cancel:     cancel,
	}

	go hp.readStdout(stdout)
	go hp.readStderr(stderr)

	log.Printf("[%s] started headless claude (pid %d): %s", opts.SessionID, execCmd.Process.Pid, strings.Join(args, " "))
	return hp, nil
}

// SendMessage writes a user turn to Claude over stream-json stdin.
func (hp *HeadlessProcess) SendMessage(content interface{}) error {
	hp.mu.Lock()
	defer hp.mu.Unlock()

	msg := map[string]interface{}{
		"type": "user",
		"message": map[string]interface{}{
			"role":    "user",
			"content": content,
		},
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal message: %w", err)
	}
	hp.IsGenerating = true
	if _, err := hp.Stdin.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("write stdin: %w", err)
	}
	log.Printf("[%s] sent message to claude", hp.SessionID)
	return nil
}

func (hp *HeadlessProcess) Kill() error {
	hp.mu.Lock()
	defer hp.mu.Unlock()
	log.Printf("[%s] killing headless claude", hp.SessionID)
	hp.cancel()
	if hp.Cmd.Process != nil {
		return hp.Cmd.Process.Kill()
	}
	return nil
}

func (hp *HeadlessProcess) Wait() error { return hp.Cmd.Wait() }

func (hp *HeadlessProcess) readStdout(stdout io.ReadCloser) {
	defer close(hp.OutputChan)

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var msg claude.StreamMessage
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			log.Printf("[%s] parse stdout: %v", hp.SessionID, err)
			continue
		}
		if msg.Type == "result" {
			hp.mu.Lock()
			hp.IsGenerating = false
			hp.mu.Unlock()
		}
		select {
		case hp.OutputChan <- &msg:
		case <-hp.ctx.Done():
			return
		}
	}
	if err := scanner.Err(); err != nil {
		select {
		case hp.ErrorChan <- fmt.Errorf("stdout scanner: %w", err):
		case <-hp.ctx.Done():
		}
	}
}

func (hp *HeadlessProcess) readStderr(stderr io.ReadCloser) {
	defer close(hp.stderrDone)
	scanner := bufio.NewScanner(stderr)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		log.Printf("[%s] claude stderr: %s", hp.SessionID, line)
		// Guard #1 keeps us from resuming a stub, but a transcript that has real
		// turns yet is otherwise unresumable (corruption, truncation) still trips
		// this. Flag it so the exit path can discard the poisoned file.
		if strings.Contains(line, "No conversation found with session ID") {
			hp.mu.Lock()
			hp.resumeFailed = true
			hp.mu.Unlock()
		}
	}
}

// DiscardIfUnresumable removes the transcript when claude reported this session
// could not be resumed ("No conversation found ..."). Left in place, the stale
// .jsonl would poison every future spawn — buildArgs would keep choosing
// --resume and claude would keep exiting 1. Dropping it lets the next message
// start a clean session under the same id, so the chat recovers instead of
// crash-looping. Call after Wait() returns and stderr has drained.
func (hp *HeadlessProcess) DiscardIfUnresumable() {
	// stderr may still be draining when Wait() returns; wait briefly so the
	// resumeFailed flag is settled before we read it.
	select {
	case <-hp.stderrDone:
	case <-time.After(500 * time.Millisecond):
	}
	hp.mu.RLock()
	failed, path := hp.resumeFailed, hp.transcript
	hp.mu.RUnlock()
	if !failed || path == "" {
		return
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		log.Printf("[%s] failed to discard unresumable transcript %s: %v", hp.SessionID, path, err)
		return
	}
	log.Printf("[%s] discarded unresumable transcript; next message starts a fresh session", hp.SessionID)
}

func (hp *HeadlessProcess) Done() <-chan struct{} { return hp.ctx.Done() }
