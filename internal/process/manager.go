package process

import (
	"fmt"
	"log"
	"sync"
	"time"
)

// Manager owns Claude processes, one per session, enforcing a singleton: only
// one process runs at a time (per-sprite isolation handles parallelism across
// sprites, not within one). Lifted from claude-hub.
type Manager struct {
	processes      map[string]*HeadlessProcess
	mu             sync.RWMutex
	graceTimer     *time.Timer
	graceSessionID string
}

func NewManager() *Manager {
	return &Manager{processes: make(map[string]*HeadlessProcess)}
}

// Spawn starts (or returns the existing) process for a session, killing any
// process for a different session first.
func (m *Manager) Spawn(opts Options) (*HeadlessProcess, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.cancelGracePeriodLocked()

	if existing, ok := m.processes[opts.SessionID]; ok {
		return existing, nil
	}

	for existingID, existingHP := range m.processes {
		log.Printf("[%s] killing existing process %s to enforce singleton", opts.SessionID, existingID)
		if err := existingHP.Kill(); err != nil {
			log.Printf("[%s] error killing existing process: %v", existingID, err)
		}
		delete(m.processes, existingID)
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

// StartGracePeriod kills the session's process if no client reconnects in 10s.
func (m *Manager) StartGracePeriod(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cancelGracePeriodLocked()

	log.Printf("[%s] starting 10s grace period", sessionID)
	m.graceSessionID = sessionID
	m.graceTimer = time.AfterFunc(10*time.Second, func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.graceSessionID == sessionID {
			if hp, ok := m.processes[sessionID]; ok {
				log.Printf("[%s] grace period expired, killing process", sessionID)
				_ = hp.Kill()
				delete(m.processes, sessionID)
			}
			m.graceSessionID = ""
			m.graceTimer = nil
		}
	})
}

func (m *Manager) CancelGracePeriod() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cancelGracePeriodLocked()
}

func (m *Manager) cancelGracePeriodLocked() {
	if m.graceTimer != nil {
		m.graceTimer.Stop()
		m.graceTimer = nil
		m.graceSessionID = ""
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
