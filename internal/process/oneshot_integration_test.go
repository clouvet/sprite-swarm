//go:build integration

// Integration test for the one-process-per-turn contract. Spawns a REAL `claude`, so it
// is build-tagged and run explicitly: `go test -tags=integration ./internal/process/`.
package process

import (
	"crypto/rand"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/clouvet/sprite-swarm/internal/config"
)

func newUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// A delivered message must run as one turn and then the process must EXIT (SendMessage
// closes stdin), firing onExit — that exit is what the hub uses to drive the next turn.
// A second turn must then run as a fresh process that resumes the first.
func TestOneShotTurnExitsThenNextTurnRuns(t *testing.T) {
	if os.Getenv("HOME") == "" {
		t.Skip("needs HOME for claude")
	}
	m := NewManager()
	exited := make(chan string, 4)
	m.SetOnExit(func(id string) { exited <- id })

	sid := newUUID()
	tmp := t.TempDir()
	opts := Options{
		SessionID: sid,
		CWD:       tmp,
		// Must be where claude actually writes (HOME-based, slug of CWD) so buildArgs
		// detects turn 1's transcript and uses --resume for turn 2 — exactly what the hub
		// passes (sessionProjectsDir). Empty on turn 1 → --session-id; present on turn 2.
		ProjectsDir:   config.ProjectsDirFor(tmp),
		DangerousSkip: true,
		Model:         "claude-haiku-4-5-20251001",
	}

	runTurn := func(label, prompt string) {
		hp, created, err := m.Spawn(opts)
		if err != nil || !created {
			t.Fatalf("%s: spawn err=%v created=%v", label, err, created)
		}
		gotResult := make(chan bool, 1)
		go func() {
			for msg := range hp.OutputChan { // drain so claude isn't blocked on the channel
				if msg.Type == "result" {
					select {
					case gotResult <- true:
					default:
					}
				}
			}
		}()
		if err := m.SendMessage(sid, prompt); err != nil {
			t.Fatalf("%s: send: %v", label, err)
		}
		select {
		case <-gotResult:
		case <-time.After(120 * time.Second):
			t.Fatalf("%s: no result event", label)
		}
		select {
		case id := <-exited:
			if id != sid {
				t.Fatalf("%s: onExit for %q, want %q", label, id, sid)
			}
		case <-time.After(30 * time.Second):
			t.Fatalf("%s: process did NOT exit after the turn — stdin close / one-shot broken", label)
		}
		if m.GetProcessCount() != 0 {
			t.Fatalf("%s: a process is still mapped after exit; per-turn contract broken", label)
		}
	}

	runTurn("turn1", "Reply with exactly: ONE")
	runTurn("turn2", "Reply with exactly: TWO") // fresh process; resumes turn1's transcript
}
