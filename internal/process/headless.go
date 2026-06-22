package process

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/clouvet/sprite-agent/pkg/claude"
)

// Options configures how a headless Claude process is launched (DESIGN §3.1):
// scoped permissions instead of a blanket skip, deterministic session id, and
// token-level streaming.
type Options struct {
	SessionID      string // deterministic; also the transcript filename
	CWD            string
	ProjectsDir    string // used to decide --resume vs --session-id
	PermissionMode string // --permission-mode (e.g. acceptEdits, plan)
	SettingsPath   string // --settings <file> when non-empty
	MCPConfigPath  string // --mcp-config <file> when non-empty
	AppendSystem   string // --append-system-prompt when non-empty (fleet affordance, DESIGN §5)
}

// HeadlessProcess is a supervised, long-lived `claude` process driven over
// stream-json stdin/stdout. One process serves all turns of one session.
type HeadlessProcess struct {
	SessionID    string
	CWD          string
	Cmd          *exec.Cmd
	Stdin        io.WriteCloser
	StartedAt    time.Time
	IsGenerating bool

	OutputChan chan *claude.StreamMessage
	ErrorChan  chan error

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
	mode := opts.PermissionMode
	if mode == "" {
		mode = "acceptEdits"
	}
	args = append(args, "--permission-mode", mode)
	if opts.SettingsPath != "" {
		args = append(args, "--settings", opts.SettingsPath)
	}
	if opts.MCPConfigPath != "" {
		args = append(args, "--mcp-config", opts.MCPConfigPath)
	}
	if opts.AppendSystem != "" {
		args = append(args, "--append-system-prompt", opts.AppendSystem)
	}

	transcript := opts.ProjectsDir + "/" + opts.SessionID + ".jsonl"
	if _, err := os.Stat(transcript); err == nil {
		args = append(args, "--resume", opts.SessionID)
	} else {
		args = append(args, "--session-id", opts.SessionID)
	}
	return args
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
	execCmd.Env = os.Environ()

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
		Cmd:        execCmd,
		Stdin:      stdin,
		StartedAt:  time.Now(),
		OutputChan: make(chan *claude.StreamMessage, 256),
		ErrorChan:  make(chan error, 10),
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
	scanner := bufio.NewScanner(stderr)
	for scanner.Scan() {
		if line := scanner.Text(); line != "" {
			log.Printf("[%s] claude stderr: %s", hp.SessionID, line)
		}
	}
}

func (hp *HeadlessProcess) Done() <-chan struct{} { return hp.ctx.Done() }
