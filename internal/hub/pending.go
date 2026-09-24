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
// give up on it. A message is redelivered only when the subprocess died before its
// turn landed in the transcript (compaction/crash/intentional kill); a genuinely
// poisonous message that keeps killing the process must not loop forever. var (not
// const) so tests can shrink it.
var maxSendAttempts = 3

// pendingMsg is one accepted user turn awaiting confirmed processing by claude.
//
// Issue #95: a message typed while a turn is generating was written straight to the
// claude subprocess's stdin buffer; if the process then exited to compact, the
// bytes died with it and the instruction was silently lost. We keep every unconfirmed
// turn in this persisted queue.
//
// Mid-turn steering: unlike the original one-at-a-time design, we hand a message to
// the live subprocess AS SOON AS IT ARRIVES — even while a turn is generating — so
// claude picks it up within the running turn (it reads stream-json stdin between
// steps) instead of it waiting for the turn boundary. Durability is preserved the
// same way: the message stays in this queue until the transcript confirms it was
// processed, and a process death marks every unconfirmed message undelivered so it
// replays onto the fresh --resume process (deduped against the transcript).
type pendingMsg struct {
	ID        string      `json:"id"`
	Content   interface{} `json:"content"`   // string, or a content-block array (with attachments)
	Text      string      `json:"text"`      // plaintext, to dedup against the transcript on replay
	Delivered bool        `json:"delivered"` // handed to a live subprocess; cleared on its death so it replays
	Attempts  int         `json:"attempts"`  // delivery attempts, to bound crash-loop replays
}

// sessionPending is one session's ordered queue of unconfirmed messages.
type sessionPending struct {
	mu   sync.Mutex
	msgs []pendingMsg
}

// pendingStore persists per-session pending input under dir/<sessionID>.json. After a
// full sprite-agent restart nothing is "delivered" — load() clears the flag so every
// queued message is re-delivered, with a transcript check so one that was actually
// processed before the exit isn't sent twice.
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
// all we need — ids only match a message to its persisted record; load() bumps the
// counter past any ids restored from disk so a restart can't collide.
func (p *pendingStore) nextID() string { return strconv.FormatUint(p.seq.Add(1), 10) }

func (p *pendingStore) file(id string) string { return filepath.Join(p.dir, id+".json") }

// persistLocked writes sp.msgs to disk (or removes the file when empty). Caller holds
// sp.mu. Best-effort: a failure never blocks delivery.
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

// enqueue appends a message to the session's queue and persists it. A rapid
// double-enqueue — a double-submit, or a client re-sending the same turn on
// reconnect — is dropped: if an identical, still-unconfirmed message is already
// queued, adding a second would just hand claude the same turn twice. Only the live
// queue is checked (everything in it is unconfirmed), never the transcript, so a user
// who deliberately repeats a message after the first one has been processed is
// unaffected. Empty-text turns (image-only) are never deduped against each other.
func (p *pendingStore) enqueue(id string, m pendingMsg) {
	sp := p.sess(id)
	sp.mu.Lock()
	defer sp.mu.Unlock()
	if m.Text != "" {
		for i := range sp.msgs {
			if sp.msgs[i].Text == m.Text {
				return // identical turn already queued and not yet confirmed — skip
			}
		}
	}
	sp.msgs = append(sp.msgs, m)
	_ = p.persistLocked(id, sp)
}

// confirm removes every DELIVERED message whose turn now appears in the transcript
// (isProcessed reports true). Called when a turn produces a result. Only delivered
// messages are eligible, so a queued-but-not-yet-sent message is never dropped by a
// coincidental text match.
func (p *pendingStore) confirm(id string, isProcessed func(pendingMsg) bool) {
	sp := p.sess(id)
	sp.mu.Lock()
	defer sp.mu.Unlock()
	kept := sp.msgs[:0]
	changed := false
	for _, m := range sp.msgs {
		if m.Delivered && isProcessed(m) {
			changed = true
			continue // processed — drop it
		}
		kept = append(kept, m)
	}
	sp.msgs = kept
	if changed {
		_ = p.persistLocked(id, sp)
	}
}

// resetDelivery reconciles the queue after the subprocess died or was killed
// (crash, compaction, model-change, interrupt, restart). It confirms-on-death: a
// delivered message whose turn already reached the transcript (isProcessed) is
// DROPPED — claude ran it, so replaying it would re-feed the same instruction, the
// "my messages keep getting replayed" loop — while any other delivered message is
// marked undelivered so the next deliver() replays it onto the fresh process. Doing
// the transcript check here (right after the killed process has flushed its writes),
// not only lazily in deliver(), closes the window where a kill re-sent a turn claude
// had already processed. isProcessed may be nil, in which case nothing is dropped.
func (p *pendingStore) resetDelivery(id string, isProcessed func(pendingMsg) bool) {
	sp := p.sess(id)
	sp.mu.Lock()
	defer sp.mu.Unlock()
	kept := sp.msgs[:0]
	changed := false
	for _, m := range sp.msgs {
		if m.Delivered {
			if isProcessed != nil && isProcessed(m) {
				changed = true // already ran before the death — drop, don't replay
				continue
			}
			m.Delivered = false
			changed = true
		}
		kept = append(kept, m)
	}
	sp.msgs = kept
	if changed {
		_ = p.persistLocked(id, sp)
	}
}

// pending reports how many messages are queued (test/inspection helper).
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

// deliver hands every not-yet-delivered message to the subprocess, in order —
// including while a turn is generating, so claude picks it up within the running turn
// (mid-turn steering). This is the deliberate change from the original design, which
// held all but one message until the turn boundary.
//
//   - deliver writes a message to the subprocess (spawning/respawning as needed).
//   - alreadyProcessed reports whether a previously-delivered message (Attempts>0, i.e.
//     a replay after a death) already landed in the transcript, so it isn't re-run.
//     It is NOT consulted on a first delivery — a brand-new message can't have been
//     processed yet, and a coincidental match with an old identical turn must not drop it.
//   - giveUp is called when a message exceeds maxSendAttempts and is dropped.
//
// Callbacks run under the per-session lock, so deliveries for one session serialize
// (preserving stdin order) but never block other sessions. A deliver error leaves the
// message undelivered at the front of the remaining work; a later deliver() retries.
func (p *pendingStore) deliver(id string, deliver func(pendingMsg) error, alreadyProcessed func(pendingMsg) bool, giveUp func(pendingMsg)) error {
	sp := p.sess(id)
	sp.mu.Lock()
	defer sp.mu.Unlock()
	i := 0
	for i < len(sp.msgs) {
		if sp.msgs[i].Delivered {
			i++
			continue
		}
		m := sp.msgs[i]
		// A replay (previously delivered, then a death reset it) that already reached
		// the transcript must not be sent again.
		if m.Attempts > 0 && alreadyProcessed != nil && alreadyProcessed(m) {
			sp.removeLocked(m.ID)
			_ = p.persistLocked(id, sp)
			continue // slice shifted; same index is the next message
		}
		if m.Attempts >= maxSendAttempts {
			sp.removeLocked(m.ID)
			_ = p.persistLocked(id, sp)
			if giveUp != nil {
				giveUp(m)
			}
			continue
		}
		sp.msgs[i].Attempts++
		_ = p.persistLocked(id, sp)
		if err := deliver(sp.msgs[i]); err != nil {
			return err // stays undelivered (attempt counted); a later deliver retries
		}
		sp.msgs[i].Delivered = true
		_ = p.persistLocked(id, sp)
		i++
	}
	return nil
}

// load restores persisted queues at startup so messages that were pending when the
// process exited are replayed on the next deliver. Delivered is cleared (nothing is
// in flight after a full restart). It bumps the id counter past any restored ids.
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
		for i := range msgs {
			msgs[i].Delivered = false // nothing is in flight after a restart
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
