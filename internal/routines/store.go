package routines

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Store persists a sprite's custom routines and every routine's run-state to a local
// JSON file (per-sprite; not shared through the brain). Default routines come from
// code — only their run-state (last run/result/error, and a disable flag) is persisted,
// so editing a default's prompt in code takes effect on the next boot.
type Store struct {
	path string
	mu   sync.Mutex
	data persisted
}

// persisted is the on-disk shape. Custom holds whole custom tasks; Runs holds run-state
// for every task id (defaults and customs alike).
type persisted struct {
	Custom []*Task              `json:"custom"`
	Runs   map[string]*runState `json:"runs"`
}

// runState is the mutable, per-run record kept for each task id.
type runState struct {
	LastRun    time.Time `json:"last_run,omitempty"`
	LastResult string    `json:"last_result,omitempty"`
	LastError  string    `json:"last_error,omitempty"`
	LastStatus string    `json:"last_status,omitempty"`
	Disabled   bool      `json:"disabled,omitempty"` // human-disabled (applies to defaults)
}

// NewStore loads (or initializes) the routines store at dir/routines.json.
func NewStore(dir string) (*Store, error) {
	s := &Store{
		path: filepath.Join(dir, "routines.json"),
		data: persisted{Runs: map[string]*runState{}},
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	if b, err := os.ReadFile(s.path); err == nil {
		_ = json.Unmarshal(b, &s.data)
	}
	if s.data.Runs == nil {
		s.data.Runs = map[string]*runState{}
	}
	return s, nil
}

// save writes the store atomically. Caller holds s.mu.
func (s *Store) save() {
	b, err := json.MarshalIndent(&s.data, "", "  ")
	if err != nil {
		return
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, s.path)
}

// merged builds the current view of a task: the base definition with its run-state
// overlaid. Caller holds s.mu.
func (s *Store) merged(base Task) Task {
	if rs := s.data.Runs[base.ID]; rs != nil {
		base.LastRun = rs.LastRun
		base.LastResult = rs.LastResult
		base.LastError = rs.LastError
		base.LastStatus = rs.LastStatus
		if base.Kind == KindDefault {
			base.Enabled = !rs.Disabled
		}
	}
	return base
}

// List returns every task (defaults first, then custom by name) with run-state applied.
func (s *Store) List() []Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Task
	for _, d := range DefaultTasks() {
		out = append(out, s.merged(d))
	}
	customs := make([]Task, 0, len(s.data.Custom))
	for _, c := range s.data.Custom {
		customs = append(customs, s.merged(*c))
	}
	sort.SliceStable(customs, func(i, j int) bool { return customs[i].Name < customs[j].Name })
	return append(out, customs...)
}

// Get returns a single task by id.
func (s *Store) Get(id string) (Task, bool) {
	for _, t := range s.List() {
		if t.ID == id {
			return t, true
		}
	}
	return Task{}, false
}

// Create adds a custom task. name and prompt are required; intervalMin defaults to 60.
func (s *Store) Create(name, prompt string, intervalMin int) (Task, error) {
	name = strings.TrimSpace(name)
	prompt = strings.TrimSpace(prompt)
	if name == "" || prompt == "" {
		return Task{}, fmt.Errorf("name and prompt are required")
	}
	if intervalMin <= 0 {
		intervalMin = 60
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t := &Task{
		ID:          "custom-" + randHex(6),
		Name:        name,
		Prompt:      prompt,
		IntervalMin: intervalMin,
		Kind:        KindCustom,
		Enabled:     true,
	}
	s.data.Custom = append(s.data.Custom, t)
	s.save()
	return *t, nil
}

// Delete removes a custom task (and its run-state). Default tasks can't be deleted —
// disable them instead.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range DefaultTasks() {
		if d.ID == id {
			return fmt.Errorf("%q is a default routine and can't be deleted; disable it instead", id)
		}
	}
	idx := -1
	for i, c := range s.data.Custom {
		if c.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("no task %q", id)
	}
	s.data.Custom = append(s.data.Custom[:idx], s.data.Custom[idx+1:]...)
	delete(s.data.Runs, id)
	s.save()
	return nil
}

// Patch updates mutable fields of a task. Nil pointers leave a field unchanged. Enabled
// is stored as a disable-flag for defaults and on the task itself for customs.
func (s *Store) Patch(id string, name, prompt *string, intervalMin *int, enabled *bool) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Default task: only enable/disable is meaningful (name/prompt/interval live in code).
	for _, d := range DefaultTasks() {
		if d.ID == id {
			if enabled != nil {
				s.run(id).Disabled = !*enabled
			}
			s.save()
			return s.merged(d), nil
		}
	}
	for _, c := range s.data.Custom {
		if c.ID != id {
			continue
		}
		if name != nil && strings.TrimSpace(*name) != "" {
			c.Name = strings.TrimSpace(*name)
		}
		if prompt != nil && strings.TrimSpace(*prompt) != "" {
			c.Prompt = strings.TrimSpace(*prompt)
		}
		if intervalMin != nil && *intervalMin > 0 {
			c.IntervalMin = *intervalMin
		}
		if enabled != nil {
			c.Enabled = *enabled
		}
		s.save()
		return s.merged(*c), nil
	}
	return Task{}, fmt.Errorf("no task %q", id)
}

// run returns the (created-if-absent) run-state for id. Caller holds s.mu.
func (s *Store) run(id string) *runState {
	rs := s.data.Runs[id]
	if rs == nil {
		rs = &runState{}
		s.data.Runs[id] = rs
	}
	return rs
}

// setStatus records a transient status (e.g. running) without touching results.
func (s *Store) setStatus(id, status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.run(id).LastStatus = status
	s.save()
}

// recordRun stores the outcome of a run: result text on success, error string on
// failure, and the run timestamp either way.
func (s *Store) recordRun(id string, now time.Time, result, errStr string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rs := s.run(id)
	rs.LastRun = now
	rs.LastError = errStr
	if errStr != "" {
		rs.LastStatus = StatusError
	} else {
		rs.LastStatus = StatusOK
		rs.LastResult = result
	}
	s.save()
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "xxxxxxxxxxxx"[:n*2]
	}
	return hex.EncodeToString(b)
}
