// Package session tracks per-session state for the session service. Lifted from
// claude-hub. With deterministic --session-id the web session id and the Claude
// session id are the same value, so ClaudeUUID == ID from creation.
package session

import (
	"encoding/json"
	"sync"
	"time"
)

// State is the lifecycle state of a session.
type State int

const (
	StateIdle State = iota
	StateWebOnly
	StateTerminalOnly
	StateTransitioning
)

// StreamingState tracks in-flight streaming so reconnecting clients can catch up.
type StreamingState struct {
	ActiveContentBlocks []json.RawMessage
	IsGenerating        bool
}

// Session represents a Claude Code session served to one or more clients.
type Session struct {
	ID               string
	ClaudeUUID       string // the <id>.jsonl transcript filename (== ID under deterministic session-id)
	CWD              string
	State            State
	LastActivity     time.Time
	ConnectedClients int
	Streaming        StreamingState

	mu sync.RWMutex
}

// NewSession creates a session whose Claude UUID equals its id (deterministic).
func NewSession(id, cwd string) *Session {
	return &Session{
		ID:           id,
		ClaudeUUID:   id,
		State:        StateIdle,
		LastActivity: time.Now(),
		CWD:          cwd,
	}
}

func (s *Session) UpdateActivity() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.LastActivity = time.Now()
}

func (s *Session) GetState() State {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.State
}

func (s *Session) SetState(state State) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.State = state
}

func (s *Session) SetClaudeUUID(uuid string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ClaudeUUID = uuid
}

func (s *Session) IncrementClients() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ConnectedClients++
}

func (s *Session) DecrementClients() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ConnectedClients > 0 {
		s.ConnectedClients--
	}
}

func (s *Session) GetClientCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ConnectedClients
}

func (s *Session) SetGenerating(generating bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Streaming.IsGenerating = generating
	if !generating {
		s.Streaming.ActiveContentBlocks = nil
	}
}

func (s *Session) IsGenerating() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Streaming.IsGenerating
}

func (s *Session) AddContentBlock(block json.RawMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Streaming.ActiveContentBlocks = append(s.Streaming.ActiveContentBlocks, block)
}

func (s *Session) ClearContentBlocks() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Streaming.ActiveContentBlocks = nil
}

func (s *Session) GetStreamingState() StreamingState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	blocks := make([]json.RawMessage, len(s.Streaming.ActiveContentBlocks))
	copy(blocks, s.Streaming.ActiveContentBlocks)
	return StreamingState{
		ActiveContentBlocks: blocks,
		IsGenerating:        s.Streaming.IsGenerating,
	}
}
