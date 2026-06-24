package fleet

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/clouvet/sprite-agent/internal/config"
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
	started  int64
	now      func() time.Time

	mu              sync.Mutex
	phase           string                // current free-text phase, refreshed into status
	idleProbe       func() bool           // reports whether the agent is currently idle (no clients, not generating)
	idleReapAfter   time.Duration         // a worker self-declares reapable after idle this long (0 = disabled)
	idleSince       time.Time             // when the current idle stretch began (zero = not idle)
	manualReapable  bool                  // set via MarkReapable (e.g. work done / PR merged)
	attendanceProbe func() (bool, string) // reports whether a human is attached + to which session
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
		now:      time.Now,
		phase:    "idle",
	}
}

// SetIdleReaping wires an idle probe + threshold so a worker self-declares
// reapable after being idle (no clients, not generating) for `after`. after<=0
// disables idle self-reaping. Home agents never self-declare reapable.
func (s *Service) SetIdleReaping(probe func() bool, after time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.idleProbe = probe
	s.idleReapAfter = after
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

// computeReapable decides whether this agent should advertise Reapable now.
func (s *Service) computeReapable(now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.manualReapable {
		return true
	}
	if s.role == "home" || s.idleReapAfter <= 0 || s.idleProbe == nil {
		s.idleSince = time.Time{}
		return false
	}
	if s.idleProbe() {
		if s.idleSince.IsZero() {
			s.idleSince = now
		}
		return now.Sub(s.idleSince) >= s.idleReapAfter
	}
	s.idleSince = time.Time{}
	return false
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
		Reapable:  s.computeReapable(now),
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

// ReapTargets returns the ids the reaper should destroy now (pure policy).
func (s *Service) ReapTargets(ctx context.Context, deadReapAfter time.Duration) ([]string, error) {
	roster, err := s.roster(ctx)
	if err != nil {
		return nil, err
	}
	return ReapTargets(roster, s.now(), deadReapAfter), nil
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
				// Refresh status too, so idle→reapable propagates to the roster.
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
