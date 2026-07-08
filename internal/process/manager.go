package process

import (
	"fmt"
	"log"
	"sync"
	"time"
)

// Manager owns Claude processes, one per session. v2 allows MULTIPLE concurrent
// sessions on a sprite (was a singleton in claude-hub): a worker can run a
// dispatched task while a human attaches a separate chat, without one killing the
// other. Per-sprite isolation handles cross-sprite parallelism; within a sprite,
// each session gets its own process.
type Manager struct {
	processes   map[string]*HeadlessProcess
	graceTimers map[string]*time.Timer // per-session grace timers
	mu          sync.RWMutex
}

func NewManager() *Manager {
	return &Manager{
		processes:   make(map[string]*HeadlessProcess),
		graceTimers: make(map[string]*time.Timer),
	}
}

// Spawn starts (or returns the existing) process for a session. It does NOT kill
// other sessions' processes — concurrent sessions coexist.
func (m *Manager) Spawn(opts Options) (*HeadlessProcess, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.cancelGraceLocked(opts.SessionID)

	if existing, ok := m.processes[opts.SessionID]; ok {
		return existing, nil
	}

	hp, err := NewHeadlessProcess(opts)
	if err != nil {
		return nil, err
	}
	m.processes[opts.SessionID] = hp

	go func() {
		if err := hp.Wait(); err != nil {
			log.Printf("[%s] claude exited with error: %v", opts.SessionID, err)
		} else {
			log.Printf("[%s] claude exited normally", opts.SessionID)
		}
		m.mu.Lock()
		delete(m.processes, opts.SessionID)
		m.mu.Unlock()
	}()

	return hp, nil
}

func (m *Manager) Get(sessionID string) (*HeadlessProcess, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if hp, ok := m.processes[sessionID]; ok {
		return hp, nil
	}
	return nil, fmt.Errorf("no process for session %s", sessionID)
}

func (m *Manager) Kill(sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if hp, ok := m.processes[sessionID]; ok {
		if err := hp.Kill(); err != nil {
			return err
		}
		delete(m.processes, sessionID)
		return nil
	}
	return fmt.Errorf("no process for session %s", sessionID)
}

func (m *Manager) SendMessage(sessionID string, content interface{}) error {
	hp, err := m.Get(sessionID)
	if err != nil {
		return err
	}
	return hp.SendMessage(content)
}

// Grace durations are vars (not consts) so tests can shrink them.
var (
	// gracePeriod is how long a process survives after its last client leaves,
	// giving a quick refresh/reconnect time to reclaim it before we reap it.
	gracePeriod = 10 * time.Second
	// generatingRecheck is how often we re-check a process that was still
	// generating when its grace expired (see StartGracePeriod).
	generatingRecheck = 30 * time.Second
)

// StartGracePeriod reaps the session's process once no client is attached — but
// NOT while it's still generating. A background turn (you closed the browser /
// locked your phone mid-task) must run to completion: killing it would abort the
// work in flight. So if grace expires while generating we keep the process alive
// and re-check; only an idle, client-less process gets reaped (its transcript is
// on disk, so a later reconnect resumes it). Per-session, so grace on one session
// never affects another.
func (m *Manager) StartGracePeriod(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cancelGraceLocked(sessionID)

	log.Printf("[%s] starting %s grace period", sessionID, gracePeriod)
	m.armGraceLocked(sessionID, gracePeriod)
}

// armGraceLocked schedules a (re-)check after d. Caller must hold m.mu.
func (m *Manager) armGraceLocked(sessionID string, d time.Duration) {
	m.graceTimers[sessionID] = time.AfterFunc(d, func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if _, pending := m.graceTimers[sessionID]; !pending {
			return // cancelled by a reconnect
		}
		hp, ok := m.processes[sessionID]
		if !ok {
			delete(m.graceTimers, sessionID)
			return
		}
		if hp.IsGenerating {
			// Background turn still running with no one watching — let it finish.
			log.Printf("[%s] grace expired but still generating; keeping process alive", sessionID)
			m.armGraceLocked(sessionID, generatingRecheck)
			return
		}
		log.Printf("[%s] grace period expired, killing process", sessionID)
		_ = hp.Kill()
		delete(m.processes, sessionID)
		delete(m.graceTimers, sessionID)
	})
}

// CancelGracePeriod cancels a pending grace timer for a session.
func (m *Manager) CancelGracePeriod(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cancelGraceLocked(sessionID)
}

func (m *Manager) cancelGraceLocked(sessionID string) {
	if t, ok := m.graceTimers[sessionID]; ok {
		t.Stop()
		delete(m.graceTimers, sessionID)
	}
}

func (m *Manager) GetActiveGeneratingCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	count := 0
	for _, hp := range m.processes {
		if hp.IsGenerating {
			count++
		}
	}
	return count
}

func (m *Manager) GetProcessCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.processes)
}

// ActiveSessionIDs returns the session ids that currently have a live process.
func (m *Manager) ActiveSessionIDs() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ids := make([]string, 0, len(m.processes))
	for id := range m.processes {
		ids = append(ids, id)
	}
	return ids
}
