package fleet

import (
	"context"
	"encoding/json"
	"log"
	"path"
	"sort"
	"strings"
	"time"
)

// taskPollInterval is how often an agent checks its task inbox.
const taskPollInterval = 10 * time.Second

// Task is a unit of work one agent assigns to another (P2.1 dispatch).
//
// DESIGN §10 wants delivery via the target's session API with the assignment
// mirrored to S3 as visible fleet state. Because private sprite URLs are
// OAuth-gated (no direct cross-sprite session call), the brain IS the delivery
// channel here: the assigner writes the task; the target polls its inbox and
// injects the task into its own session locally, so it still materializes in the
// target's transcript (seam #2 holds — see BUILD_REPORT for the rationale).
type Task struct {
	ID        string `json:"id"`
	From      string `json:"from"`
	To        string `json:"to"`
	Task      string `json:"task"`
	SessionID string `json:"session_id"` // session the target should inject the task into
	CreatedAt int64  `json:"created_at"`
}

// Tasks are append-only under a dedicated prefix (collision-proof per-key), so
// many assigners never clobber each other (DESIGN §4.1 pattern 2). They live
// outside fleet/<id>/ so they never collide with per-agent coordination keys.
func taskPrefix(to string) string     { return path.Join("fleet", "tasks", to) + "/" }
func taskKey(to, id string) string    { return path.Join("fleet", "tasks", to, id+".json") }
func seenTasksKey(self string) string { return path.Join("fleet", self, "seen-tasks.json") }

// Dispatch records a task assigned by this agent to target, returning it (with a
// fresh session id the target will inject into). RosterProvider/Dispatcher hook.
func (s *Service) Dispatch(ctx context.Context, target, task string) (interface{}, error) {
	return s.dispatch(ctx, target, task)
}

func (s *Service) dispatch(ctx context.Context, target, task string) (Task, error) {
	now := s.now()
	t := Task{
		// id is timestamp-prefixed for natural ordering + a uuid for uniqueness.
		ID:        timestampID(now) + "-" + newUUID(),
		From:      s.id,
		To:        target,
		Task:      task,
		SessionID: newUUID(),
		CreatedAt: now.Unix(),
	}
	data, _ := json.Marshal(t)
	if err := s.brain.Put(ctx, taskKey(target, t.ID), data); err != nil {
		return Task{}, err
	}
	return t, nil
}

// Inbox lists tasks addressed to id (visible fleet state), oldest first.
func (s *Service) Inbox(ctx context.Context, id string) ([]Task, error) {
	keys, err := s.brain.List(ctx, taskPrefix(id))
	if err != nil {
		return nil, err
	}
	sort.Strings(keys) // timestamp-prefixed ids → chronological
	var tasks []Task
	for _, k := range keys {
		if !strings.HasSuffix(k, ".json") {
			continue
		}
		if data, err := s.brain.Get(ctx, k); err == nil {
			var t Task
			if json.Unmarshal(data, &t) == nil {
				tasks = append(tasks, t)
			}
		}
	}
	return tasks, nil
}

// StartTaskPolling polls this agent's inbox and injects unseen tasks via inject.
// Seen task ids persist to this agent's own prefix so a restart doesn't re-inject.
func (s *Service) StartTaskPolling(ctx context.Context, inject func(sessionID, task string) error) {
	seen := s.loadSeen(ctx)
	go func() {
		ticker := time.NewTicker(taskPollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				pollCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
				tasks, err := s.Inbox(pollCtx, s.id)
				if err != nil {
					cancel()
					continue
				}
				changed := false
				for _, t := range tasks {
					if seen[t.ID] {
						continue
					}
					if err := inject(t.SessionID, t.Task); err != nil {
						log.Printf("fleet: inject task %s failed: %v", t.ID, err)
						continue
					}
					log.Printf("fleet: accepted task %s from %s -> session %s", t.ID, t.From, t.SessionID)
					seen[t.ID] = true
					changed = true
				}
				if changed {
					s.saveSeen(pollCtx, seen)
				}
				cancel()
			}
		}
	}()
}

func (s *Service) loadSeen(ctx context.Context) map[string]bool {
	seen := map[string]bool{}
	if data, err := s.brain.Get(ctx, seenTasksKey(s.id)); err == nil {
		var ids []string
		if json.Unmarshal(data, &ids) == nil {
			for _, id := range ids {
				seen[id] = true
			}
		}
	}
	return seen
}

func (s *Service) saveSeen(ctx context.Context, seen map[string]bool) {
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	data, _ := json.Marshal(ids)
	if err := s.brain.Put(ctx, seenTasksKey(s.id), data); err != nil {
		log.Printf("fleet: persist seen-tasks failed: %v", err)
	}
}

// timestampID returns a sortable second-resolution id prefix.
func timestampID(t time.Time) string {
	return t.UTC().Format("20060102T150405")
}
