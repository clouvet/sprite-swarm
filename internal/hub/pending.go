package hub

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// maxSendAttempts caps how many times a queued message is (re)delivered before we
// give up on it. A message is redelivered only when the subprocess died before
// acking it (compaction/crash); a genuinely poisonous message that keeps killing
// the process must not loop forever. var (not const) so tests can shrink it.
var maxSendAttempts = 3

// pendingMsg is one accepted user turn awaiting confirmed processing by claude.
//
// Issue #95: a message typed while a turn is generating was written straight to the
// claude subprocess's stdin buffer; if the process then exited to compact, the
// bytes died with it and the instruction was silently lost. Instead we keep every
// unconfirmed turn in this persisted queue and only hand ONE at a time to the
// subprocess, so a death replays the queue rather than dropping it.
type pendingMsg struct {
	ID       string      `json:"id"`
	Content  interface{} `json:"content"`  // string, or a content-block array (with attachments)
	Text     string      `json:"text"`     // plaintext, to dedup against the transcript on replay
	Sent     bool        `json:"sent"`     // handed to a subprocess at least once (which may have since died)
	Attempts int         `json:"attempts"` // delivery attempts, to bound crash-loop replays
}

// sessionPending is one session's ordered queue plus the id of the message that is
// currently handed to the subprocess and awaiting its result.
type sessionPending struct {
	mu       sync.Mutex
	msgs     []pendingMsg
	inFlight string
}

// pendingStore persists per-session pending input under dir/<sessionID>.json. Only
// the queue is persisted (not inFlight): after a full sprite-agent restart nothing
// is "in flight" — every queued message is re-pumped, with a transcript check so
// one that was actually processed before the exit isn't sent twice.
type pendingStore struct {
	dir string
	mu  sync.Mutex // guards m
	m   map[string]*sessionPending
	seq atomic.Uint64
}

func newPendingStore(dir string) *pendingStore {
	return &pendingStore{dir: dir, m: make(map[string]*sessionPending)}
}

func (p *pendingStore) sess(id string) *sessionPending {
	p.mu.Lock()
	defer p.mu.Unlock()
	sp := p.m[id]
	if sp == nil {
		sp = &sessionPending{}
		p.m[id] = sp
	}
	return sp
}

// nextID returns a run-unique id for a queued message. Uniqueness within a run is
// all we need — ids are only used to match ack/inFlight; load() bumps the counter
// past any ids restored from disk so a restart can't collide.
func (p *pendingStore) nextID() string { return strconv.FormatUint(p.seq.Add(1), 10) }

func (p *pendingStore) file(id string) string { return filepath.Join(p.dir, id+".json") }

// persistLocked writes sp.msgs to disk (or removes the file when empty). Caller
// holds sp.mu. Best-effort: a failure never blocks delivery.
func (p *pendingStore) persistLocked(id string, sp *sessionPending) error {
	if p.dir == "" {
		return nil
	}
	if len(sp.msgs) == 0 {
		if err := os.Remove(p.file(id)); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(p.dir, 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(sp.msgs)
	if err != nil {
		return err
	}
	return os.WriteFile(p.file(id), data, 0o600)
}

// enqueue appends a message to the session's queue and persists it.
func (p *pendingStore) enqueue(id string, m pendingMsg) {
	sp := p.sess(id)
	sp.mu.Lock()
	defer sp.mu.Unlock()
	sp.msgs = append(sp.msgs, m)
	_ = p.persistLocked(id, sp)
}

// ackInFlight removes whatever message is in flight (its turn just produced a
// result) and clears the in-flight marker. Persists.
func (p *pendingStore) ackInFlight(id string) {
	sp := p.sess(id)
	sp.mu.Lock()
	defer sp.mu.Unlock()
	if sp.inFlight == "" {
		return
	}
	sp.removeLocked(sp.inFlight)
	sp.inFlight = ""
	_ = p.persistLocked(id, sp)
}

// clearInFlight marks that no message is being processed (the subprocess died). The
// message stays queued so pump can replay it onto a fresh --resume process.
func (p *pendingStore) clearInFlight(id string) {
	sp := p.sess(id)
	sp.mu.Lock()
	sp.inFlight = ""
	sp.mu.Unlock()
}

// pending reports whether the session has queued messages (test/inspection helper).
func (p *pendingStore) pending(id string) int {
	sp := p.sess(id)
	sp.mu.Lock()
	defer sp.mu.Unlock()
	return len(sp.msgs)
}

func (sp *sessionPending) removeLocked(msgID string) {
	for i := range sp.msgs {
		if sp.msgs[i].ID == msgID {
			sp.msgs = append(sp.msgs[:i], sp.msgs[i+1:]...)
			return
		}
	}
}

// pump delivers the next queued message if the session is idle (nothing in flight).
// Exactly ONE message is in flight at a time — the rest stay in Go, never shoved
// into the subprocess's stdin buffer where a compaction would drop them.
//
//   - deliver writes a message to the subprocess (spawning/respawning as needed).
//   - alreadyProcessed reports whether a previously-sent message already landed in
//     the transcript, so a replay after a death doesn't re-run it (may be nil).
//   - giveUp is called when a message exceeds maxSendAttempts and is dropped.
//
// Callbacks run under the per-session lock, so pumps for one session serialize but
// never block other sessions.
func (p *pendingStore) pump(id string, deliver func(pendingMsg) error, alreadyProcessed func(pendingMsg) bool, giveUp func(pendingMsg)) error {
	sp := p.sess(id)
	sp.mu.Lock()
	defer sp.mu.Unlock()
	if sp.inFlight != "" {
		return nil // a turn is running; the result handler pumps the next
	}
	for len(sp.msgs) > 0 {
		front := sp.msgs[0]
		if front.Sent && alreadyProcessed != nil && alreadyProcessed(front) {
			// Processed during a death window but never acked — drop, don't re-run.
			sp.removeLocked(front.ID)
			_ = p.persistLocked(id, sp)
			continue
		}
		if front.Attempts >= maxSendAttempts {
			sp.removeLocked(front.ID)
			_ = p.persistLocked(id, sp)
			if giveUp != nil {
				giveUp(front)
			}
			continue
		}
		sp.msgs[0].Attempts++
		_ = p.persistLocked(id, sp)
		if err := deliver(sp.msgs[0]); err != nil {
			return err // stays queued (front, attempt counted); a later pump retries
		}
		sp.msgs[0].Sent = true
		sp.inFlight = front.ID
		_ = p.persistLocked(id, sp)
		return nil
	}
	return nil
}

// load restores persisted queues at startup so messages that were pending when the
// process exited are replayed on the next pump. It bumps the id counter past any
// restored ids to avoid collisions with new messages.
func (p *pendingStore) load() {
	if p.dir == "" {
		return
	}
	entries, err := os.ReadDir(p.dir)
	if err != nil {
		return
	}
	var maxID uint64
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(p.dir, name))
		if err != nil {
			continue
		}
		var msgs []pendingMsg
		if json.Unmarshal(data, &msgs) != nil || len(msgs) == 0 {
			continue
		}
		id := strings.TrimSuffix(name, ".json")
		p.mu.Lock()
		p.m[id] = &sessionPending{msgs: msgs}
		p.mu.Unlock()
		for _, msg := range msgs {
			if n, err := strconv.ParseUint(msg.ID, 10, 64); err == nil && n > maxID {
				maxID = n
			}
		}
	}
	if maxID > p.seq.Load() {
		p.seq.Store(maxID)
	}
}
