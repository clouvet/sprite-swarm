package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// SessionMeta is lightweight metadata for the session list in the UI. The
// transcript itself (the source of truth) lives in Claude's .jsonl; this is
// just titles/timestamps. Persisted as JSON so the list survives restarts.
type SessionMeta struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	LastMessage   string `json:"lastMessage"`
	CreatedAt     int64  `json:"createdAt"`
	LastMessageAt int64  `json:"lastMessageAt"`
}

// metaStore is an in-memory, JSON-file-backed session metadata store.
type metaStore struct {
	path string
	mu   sync.Mutex
	byID map[string]*SessionMeta
}

func newMetaStore(path string) *metaStore {
	s := &metaStore{path: path, byID: make(map[string]*SessionMeta)}
	s.load()
	return s
}

func (s *metaStore) load() {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var list []*SessionMeta
	if json.Unmarshal(data, &list) != nil {
		return
	}
	for _, m := range list {
		s.byID[m.ID] = m
	}
}

// saveLocked persists the store; caller holds the lock.
func (s *metaStore) saveLocked() {
	list := make([]*SessionMeta, 0, len(s.byID))
	for _, m := range s.byID {
		list = append(list, m)
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return
	}
	if data, err := json.MarshalIndent(list, "", "  "); err == nil {
		tmp := s.path + ".tmp"
		if os.WriteFile(tmp, data, 0o644) == nil {
			_ = os.Rename(tmp, s.path)
		}
	}
}

// List returns sessions, newest activity first.
func (s *metaStore) List() []*SessionMeta {
	s.mu.Lock()
	defer s.mu.Unlock()
	list := make([]*SessionMeta, 0, len(s.byID))
	for _, m := range s.byID {
		cp := *m
		list = append(list, &cp)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].LastMessageAt > list[j].LastMessageAt })
	return list
}

// Create makes a new session with a generated UUID id (used as --session-id).
func (s *metaStore) Create(name string) *SessionMeta {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UnixMilli()
	if name == "" {
		name = "New chat"
	}
	m := &SessionMeta{ID: newUUID(), Name: name, CreatedAt: now, LastMessageAt: now}
	s.byID[m.ID] = m
	s.saveLocked()
	return m
}

// Delete removes a session's metadata (transcript is left on disk).
func (s *metaStore) Delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.byID, id)
	s.saveLocked()
}

// Touch updates last-message preview/time, creating the entry if missing.
func (s *metaStore) Touch(id, preview string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.byID[id]
	if m == nil {
		now := time.Now().UnixMilli()
		m = &SessionMeta{ID: id, Name: "New chat", CreatedAt: now}
		s.byID[id] = m
	}
	if preview != "" {
		m.LastMessage = preview
	}
	m.LastMessageAt = time.Now().UnixMilli()
	s.saveLocked()
}
