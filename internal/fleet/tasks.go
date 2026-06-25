package fleet

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"path"
	"sort"
	"strings"
	"time"
)

// taskPollInterval is the BACKSTOP cadence for draining the inbox. Delivery is
// normally instant: the assigner nudges the target directly (see dispatch), the
// target drains on boot, and dedup makes a redundant drain harmless. This slow
// poll only catches a task whose nudge was missed (e.g. a transient network
// blip while the target was up), so it can be lazy.
const taskPollInterval = 60 * time.Second

// nudgeTimeout bounds the direct "drain now" call to a peer.
const nudgeTimeout = 10 * time.Second

// Task is a unit of work one agent assigns to another (P2.1 dispatch).
//
// Delivery: the assigner writes the task to the brain (the durable, visible
// record) and then nudges the target directly so it drains immediately — sprite
// URLs are reachable cross-sprite with the sprites token as a Bearer (an earlier
// "OAuth-gated, no direct call" assumption turned out to be wrong). The target
// always injects from the brain locally (so the task materializes in its own
// transcript) and dedups by id, so a nudge, a boot drain, and the backstop poll
// can all fire without double-injecting.
type Task struct {
	ID        string `json:"id"`
	From      string `json:"from"`
	To        string `json:"to"`
	Task      string `json:"task"`
	Kind      string `json:"kind"`       // "task" = work to execute; "note" = informational, do NOT execute
	SessionID string `json:"session_id"` // session the target should inject the task into
	CreatedAt int64  `json:"created_at"`
}

// Task kinds. A "note" is the guardrail against the report-as-work-order trap:
// informational sends are delivered as an FYI the target must not execute.
const (
	KindTask = "task"
	KindNote = "note"
)

// Tasks are append-only under a dedicated prefix (collision-proof per-key), so
// many assigners never clobber each other (DESIGN §4.1 pattern 2). They live
// outside fleet/<id>/ so they never collide with per-agent coordination keys.
func taskPrefix(to string) string     { return path.Join("fleet", "tasks", to) + "/" }
func taskKey(to, id string) string    { return path.Join("fleet", "tasks", to, id+".json") }
func seenTasksKey(self string) string { return path.Join("fleet", self, "seen-tasks.json") }

// Dispatch records a task assigned by this agent to target, returning it (with a
// fresh session id the target will inject into). kind is "task" (execute) or
// "note" (informational); empty defaults to task. RosterProvider/Dispatcher hook.
func (s *Service) Dispatch(ctx context.Context, target, task, kind string) (interface{}, error) {
	return s.dispatch(ctx, target, task, kind)
}

func (s *Service) dispatch(ctx context.Context, target, task, kind string) (Task, error) {
	if kind != KindNote {
		kind = KindTask
	}
	now := s.now()
	t := Task{
		// id is timestamp-prefixed for natural ordering + a uuid for uniqueness.
		ID:        timestampID(now) + "-" + newUUID(),
		From:      s.id,
		To:        target,
		Task:      task,
		Kind:      kind,
		SessionID: newUUID(),
		CreatedAt: now.Unix(),
	}
	data, _ := json.Marshal(t)
	if err := s.brain.Put(ctx, taskKey(target, t.ID), data); err != nil {
		return Task{}, err
	}
	// The brain holds the durable record; nudge the target so it drains NOW
	// instead of waiting for its backstop poll. Best-effort, in the background:
	// if the nudge fails (target down/suspended), the task still lands via the
	// target's boot drain or next poll. A nudge to a suspended sprite wakes it.
	go s.nudge(context.Background(), target)
	return t, nil
}

// nudge tells target to drain its inbox immediately via a direct sprite-to-sprite
// call (its roster .sprites.app URL + Bearer token — the verified path). Content-
// free and idempotent: worst case the target does a redundant, dedup-safe drain.
func (s *Service) nudge(ctx context.Context, target string) {
	url := s.agentURL(ctx, target)
	if url == "" {
		return
	}
	tok := s.GetSecret(ctx, SecretSpritesAPIToken)
	if tok == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, nudgeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(url, "/")+"/api/fleet/nudge", nil)
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("fleet: nudge %s failed (%v); task will deliver via poll", target, err)
		return
	}
	resp.Body.Close()
}

// agentURL looks up a peer's session-service URL from the roster.
func (s *Service) agentURL(ctx context.Context, id string) string {
	roster, err := s.roster(ctx)
	if err != nil {
		return ""
	}
	for _, e := range roster {
		if e.ID == id {
			return e.URL
		}
	}
	return ""
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

// StartTaskPolling wires the task inbox: it registers the inject function, drains
// once immediately (so a freshly-booted or just-nudged agent picks up tasks that
// arrived while it was down), then keeps a slow backstop poll running. Seen task
// ids persist so a restart never re-injects.
func (s *Service) StartTaskPolling(ctx context.Context, inject func(sessionID, task, kind string) error) {
	s.taskMu.Lock()
	s.injectFn = inject
	s.seen = s.loadSeen(ctx)
	s.taskMu.Unlock()

	s.DrainInbox(ctx) // catch anything waiting right now

	go func() {
		ticker := time.NewTicker(taskPollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				pollCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
				s.DrainInbox(pollCtx)
				cancel()
			}
		}
	}()
}

// DrainInbox injects every unseen task addressed to this agent. It's the single
// delivery path — called on boot, on the backstop tick, and on demand when a peer
// nudges (POST /api/fleet/nudge). Serialized so concurrent triggers can't double-
// inject; dedup via the persisted seen set makes repeats harmless.
func (s *Service) DrainInbox(ctx context.Context) error {
	s.taskMu.Lock()
	defer s.taskMu.Unlock()
	if s.injectFn == nil {
		return nil // polling not started yet
	}
	tasks, err := s.Inbox(ctx, s.id)
	if err != nil {
		return err
	}
	changed := false
	for _, t := range tasks {
		if s.seen[t.ID] {
			continue
		}
		if err := s.injectFn(t.SessionID, t.Task, t.Kind); err != nil {
			log.Printf("fleet: inject task %s failed: %v", t.ID, err)
			continue
		}
		log.Printf("fleet: accepted %s %s from %s -> session %s", t.Kind, t.ID, t.From, t.SessionID)
		s.seen[t.ID] = true
		changed = true
	}
	if changed {
		s.saveSeen(ctx, s.seen)
	}
	return nil
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
