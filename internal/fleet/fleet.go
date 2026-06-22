package fleet

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"time"

	"github.com/clouvet/sprite-agent/internal/config"
)

// heartbeatInterval is how often a running agent refreshes its heartbeat.
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
}

// New builds a Service backed by the configured S3/Tigris brain.
func New(cfg config.Config) (*Service, error) {
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
		artifact: cfg.ArtifactRef,
		now:      time.Now,
	}
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
	st := Status{
		ID:        s.id,
		Role:      s.role,
		Phase:     phase,
		URL:       s.url,
		Artifact:  s.artifact,
		StartedAt: s.started,
		UpdatedAt: s.now().Unix(),
	}
	data, _ := json.Marshal(st)
	return s.brain.Put(ctx, statusKey(s.id), data)
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
