// Package secret holds a worker-scoped, in-memory set of environment variables
// (secrets) that the harness injects into every Claude process's environment at
// spawn. Values live only in RAM — never written to disk or the fleet brain — and
// are listable by name but never readable back through the API. It's the harness
// equivalent of an in-memory .env.local for a dev session on a worker.
package secret

import (
	"sort"
	"strings"
	"sync"
)

type Store struct {
	mu sync.RWMutex
	m  map[string]string
}

func NewStore() *Store { return &Store{m: map[string]string{}} }

// Set upserts a variable, overwriting any existing value.
func (s *Store) Set(name, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[name] = value
}

func (s *Store) Delete(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, name)
}

// Names returns the variable names, sorted. Never the values.
func (s *Store) Names() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	names := make([]string, 0, len(s.m))
	for k := range s.m {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// Env returns the variables as "NAME=VALUE" strings for process injection.
func (s *Store) Env() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	env := make([]string, 0, len(s.m))
	for k, v := range s.m {
		env = append(env, k+"="+v)
	}
	return env
}

// Mask replaces occurrences of any secret value in b with a redaction marker —
// best-effort, so a value doesn't surface verbatim in the chat stream. It defeats
// casual/accidental exposure (an app logging the key, an idle `printenv`), NOT an
// agent that deliberately transforms the value to evade the match; that's inherent
// once the value is in the process environment the agent can read.
func (s *Store) Mask(b []byte) []byte {
	s.mu.RLock()
	var vals []string
	for _, v := range s.m {
		if len(v) >= 6 { // short values would cause noisy false matches
			vals = append(vals, v)
		}
	}
	s.mu.RUnlock()
	if len(vals) == 0 {
		return b
	}
	out := string(b)
	changed := false
	for _, v := range vals {
		if strings.Contains(out, v) {
			out = strings.ReplaceAll(out, v, "••••••")
			changed = true
		}
	}
	if !changed {
		return b
	}
	return []byte(out)
}
