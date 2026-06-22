// Package process supervises the headless `claude` CLI process that backs a
// session, and detects terminal `claude` sessions for co-presence. Lifted from
// claude-hub; the project directory is now configurable.
package process

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ClaudeProcess is a detected terminal Claude process.
type ClaudeProcess struct {
	PID       int
	SessionID string // the .jsonl UUID it has open
}

// TerminalDetector scans /proc for terminal `claude` processes (i.e. not ours).
type TerminalDetector struct {
	projectDir string
	ownPIDs    map[int]bool
}

// NewTerminalDetector watches the given Claude projects directory.
func NewTerminalDetector(projectDir string) *TerminalDetector {
	return &TerminalDetector{
		projectDir: projectDir,
		ownPIDs:    make(map[int]bool),
	}
}

func (d *TerminalDetector) RegisterOwnPID(pid int)   { d.ownPIDs[pid] = true }
func (d *TerminalDetector) UnregisterOwnPID(pid int) { delete(d.ownPIDs, pid) }

// ScanForTerminalSessions returns terminal claude processes with a known session.
func (d *TerminalDetector) ScanForTerminalSessions() ([]ClaudeProcess, error) {
	procDir, err := os.Open("/proc")
	if err != nil {
		return nil, fmt.Errorf("failed to open /proc: %w", err)
	}
	defer procDir.Close()

	entries, err := procDir.Readdirnames(-1)
	if err != nil {
		return nil, fmt.Errorf("failed to read /proc: %w", err)
	}

	var processes []ClaudeProcess
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry)
		if err != nil || d.ownPIDs[pid] {
			continue
		}
		if !d.isClaudeProcess(pid) {
			continue
		}
		if sessionID := d.extractSessionID(pid); sessionID != "" {
			processes = append(processes, ClaudeProcess{PID: pid, SessionID: sessionID})
		}
	}
	return processes, nil
}

func (d *TerminalDetector) isClaudeProcess(pid int) bool {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return false
	}
	parts := strings.Split(string(data), "\x00")
	if len(parts) == 0 {
		return false
	}
	return filepath.Base(parts[0]) == "claude"
}

func (d *TerminalDetector) extractSessionID(pid int) string {
	fdDir := filepath.Join("/proc", strconv.Itoa(pid), "fd")
	fds, err := os.ReadDir(fdDir)
	if err != nil {
		return ""
	}
	for _, fd := range fds {
		target, err := os.Readlink(filepath.Join(fdDir, fd.Name()))
		if err != nil {
			continue
		}
		if strings.HasPrefix(target, d.projectDir) && strings.HasSuffix(target, ".jsonl") {
			return strings.TrimSuffix(filepath.Base(target), ".jsonl")
		}
	}
	return ""
}

// FindSessionProcess returns the terminal process for a session, or nil.
func (d *TerminalDetector) FindSessionProcess(sessionID string) (*ClaudeProcess, error) {
	processes, err := d.ScanForTerminalSessions()
	if err != nil {
		return nil, err
	}
	for i := range processes {
		if processes[i].SessionID == sessionID {
			return &processes[i], nil
		}
	}
	return nil, nil
}
