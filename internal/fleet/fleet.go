package fleet

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/clouvet/sprite-swarm/internal/config"
)

// heartbeatInterval is how often a running agent refreshes its heartbeat + status.
const heartbeatInterval = 30 * time.Second

// Service is the per-agent fleet brain client: it registers this agent and
// derives the roster. It depends only on the Brain interface, so the same logic
// runs against S3/Tigris in production and an in-memory fake in tests.
type Service struct {
	brain    Brain
	id       string
	role     string
	url      string
	artifact string
	build    string // short hash of the running binary (immutable after construction)
	started  int64
	now      func() time.Time

	mu              sync.Mutex
	phase           string                // current free-text phase, refreshed into status
	manualReapable  bool                  // set via MarkReapable (e.g. work done / PR merged)
	attendanceProbe func() (bool, string) // reports whether a human is attached + to which session

	taskMu   sync.Mutex                                // serializes inbox drains (one at a time)
	seen     map[string]bool                           // task ids already injected (loaded once, persisted on change)
	injectFn func(sessionID, task, kind string) error // delivers a task/note into a local session
	busy     func() bool                              // reports if a session is generating (serialize dispatched work)
}

// New builds a Service backed by the brain. Prefers the gateway connector
// (token-free, sprite-identity authed); falls back to direct S3 credentials.
func New(cfg config.Config) (*Service, error) {
	if cfg.Brain.UsesGateway() {
		return newService(newConnectorBrain(cfg.Brain.GatewayURL), cfg), nil
	}
	brain, err := newS3Brain(context.Background(), cfg.Brain)
	if err != nil {
		return nil, err
	}
	return newService(brain, cfg), nil
}

// newService is the dependency-injected constructor (used by tests with a fake).
// Brain exposes the underlying brain store (used by memsync to mirror the
// markdown fleet-memory directory).
func (s *Service) Brain() Brain { return s.brain }

func newService(brain Brain, cfg config.Config) *Service {
	role := "worker"
	if r := strings.TrimSpace(envRole()); r != "" {
		role = r
	}
	return &Service{
		brain:    brain,
		id:       cfg.AgentID,
		role:     role,
		url:      cfg.PublicURL,
		artifact: cfg.ArtifactRef,
		build:    computeBuild(),
		now:      time.Now,
		phase:    "idle",
	}
}

// Build is the short hash of this agent's running binary.
func (s *Service) Build() string { return s.build }

// computeBuild hashes the running binary so peers can tell who's on which build
// (staleness) and self-update can no-op when already current. "unknown" if the
// binary can't be read (it still works, just won't dedup the update).
func computeBuild() string {
	self, err := os.Executable()
	if err != nil {
		return "unknown"
	}
	return hashFile(self)
}

// hashFile returns the first 12 hex chars of a file's sha256, or "" on error.
func hashFile(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// SetAttendanceProbe wires a probe reporting whether a human is attached to this
// agent and to which session (presence signal, §2.4), written into status.
func (s *Service) SetAttendanceProbe(probe func() (bool, string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attendanceProbe = probe
}

// MarkReapable flags this agent as done so a reaper can destroy it (e.g. after
// its PR merged). A no-op effect on home (ReapTargets protects home).
func (s *Service) MarkReapable(ctx context.Context) error {
	s.mu.Lock()
	s.manualReapable = true
	s.mu.Unlock()
	return s.writeStatus(ctx, "done")
}

// computeReapable reports whether this agent has been explicitly marked done
// (via MarkReapable). There is no idle-based auto-reaping — teardown is explicit.
func (s *Service) computeReapable() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.manualReapable
}

// Register writes this agent's status + initial heartbeat (boot self-registration).
func (s *Service) Register(ctx context.Context) error {
	s.started = s.now().Unix()
	if err := s.writeStatus(ctx, "idle"); err != nil {
		return err
	}
	return s.writeHeartbeat(ctx)
}

// UpdatePhase records a new free-text phase for the fleet view.
func (s *Service) UpdatePhase(ctx context.Context, phase string) error {
	return s.writeStatus(ctx, phase)
}

func (s *Service) writeStatus(ctx context.Context, phase string) error {
	if phase != "" {
		s.mu.Lock()
		s.phase = phase
		s.mu.Unlock()
	}
	now := s.now()
	s.mu.Lock()
	curPhase := s.phase
	probe := s.attendanceProbe
	s.mu.Unlock()
	present, presentSession := false, ""
	if probe != nil {
		present, presentSession = probe()
	}
	st := Status{
		ID:        s.id,
		Role:      s.role,
		Phase:     curPhase,
		URL:       s.url,
		Artifact:  s.artifact,
		Build:     s.build,
		Reapable:  s.computeReapable(),
		Present:   present,
		Session:   presentSession,
		StartedAt: s.started,
		UpdatedAt: now.Unix(),
	}
	data, _ := json.Marshal(st)
	return s.brain.Put(ctx, statusKey(s.id), data)
}

// RemoveAgent deletes an agent's COORDINATION entry (status + heartbeat) so it
// drops out of the roster after its sprite is destroyed.
//
// It deletes only the two known Layer-1 keys, NOT a blanket fleet/<id>/* prefix.
// Durable shared memory (DESIGN §4 Layer 2) must outlive the sprite ("what they
// learn persists… institutional memory lives in S3, not in any one sprite",
// §2.3) and is keyed under a separate top-level prefix (fleet/memory/…, §4.1),
// so reaping a worker never touches it. Scoping the delete to the coordination
// keys keeps that guarantee even if something else is later written under the
// per-agent prefix.
func (s *Service) RemoveAgent(ctx context.Context, id string) error {
	for _, k := range []string{statusKey(id), heartbeatKey(id)} {
		if err := s.brain.Delete(ctx, k); err != nil {
			return err
		}
	}
	return nil
}

// AgentPresent reports whether an agent exists in the roster and whether a human
// is currently attached to it (presence / the DEFER signal, §2.4). Used to guard
// explicit teardown so we don't kill a session someone is actively steering.
func (s *Service) AgentPresent(ctx context.Context, id string) (exists, present bool, err error) {
	roster, err := s.roster(ctx)
	if err != nil {
		return false, false, err
	}
	for _, e := range roster {
		if e.ID == id {
			return true, e.Present, nil
		}
	}
	return false, false, nil
}

// ReapTargets returns the ids to destroy now (explicit done only; pure policy).
func (s *Service) ReapTargets(ctx context.Context) ([]string, error) {
	roster, err := s.roster(ctx)
	if err != nil {
		return nil, err
	}
	return ReapTargets(roster), nil
}

// StaleWorkers returns non-home workers with a stale heartbeat — brain-cleanup
// candidates only if their sprite turns out to be gone (the reaper verifies).
func (s *Service) StaleWorkers(ctx context.Context, staleAfter time.Duration) ([]string, error) {
	roster, err := s.roster(ctx)
	if err != nil {
		return nil, err
	}
	return StaleWorkers(roster, s.now(), staleAfter), nil
}

func (s *Service) writeHeartbeat(ctx context.Context) error {
	data, _ := json.Marshal(Heartbeat{TS: s.now().Unix()})
	return s.brain.Put(ctx, heartbeatKey(s.id), data)
}

// StartHeartbeat refreshes this agent's heartbeat on an interval until ctx is
// cancelled, so a crashed sprite stops looking alive (DESIGN §4 Layer 1).
func (s *Service) StartHeartbeat(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(heartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				hbCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
				if err := s.writeHeartbeat(hbCtx); err != nil {
					log.Printf("fleet: heartbeat failed: %v", err)
				}
				// Refresh status too, so presence/phase changes propagate to the roster.
				if err := s.writeStatus(hbCtx, ""); err != nil {
					log.Printf("fleet: status refresh failed: %v", err)
				}
				cancel()
			}
		}
	}()
}

// Roster lists every agent in the brain (RosterProvider for the HTTP layer).
func (s *Service) Roster(ctx context.Context) (interface{}, error) {
	return s.roster(ctx)
}

// roster derives the typed roster: LIST fleet/, read each agent's status +
// heartbeat, then merge via the pure BuildRoster.
func (s *Service) roster(ctx context.Context) ([]RosterEntry, error) {
	keys, err := s.brain.List(ctx, "fleet/")
	if err != nil {
		return nil, err
	}

	statuses := make(map[string]Status)
	heartbeats := make(map[string]Heartbeat)
	for _, key := range keys {
		id := agentIDFromKey(key)
		if id == "" {
			continue
		}
		switch {
		case strings.HasSuffix(key, "/status.json"):
			if data, err := s.brain.Get(ctx, key); err == nil {
				var st Status
				if json.Unmarshal(data, &st) == nil {
					statuses[id] = st
				}
			}
		case strings.HasSuffix(key, "/heartbeat.json"):
			if data, err := s.brain.Get(ctx, key); err == nil {
				var hb Heartbeat
				if json.Unmarshal(data, &hb) == nil {
					heartbeats[id] = hb
				}
			}
		}
	}
	return BuildRoster(statuses, heartbeats, s.now()), nil
}
