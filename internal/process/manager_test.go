package process

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

// fakeProcess builds a HeadlessProcess that's safe to Kill without a real
// `claude` subprocess (Cmd has a nil Process, so Kill won't touch the OS).
func fakeProcess(id string) *HeadlessProcess {
	ctx, cancel := context.WithCancel(context.Background())
	return &HeadlessProcess{SessionID: id, Cmd: &exec.Cmd{}, ctx: ctx, cancel: cancel}
}

// setGenerating flips IsGenerating under the manager lock, the same lock the
// grace timer holds when it reads the field — keeps the test race-free.
func (m *Manager) setGenerating(hp *HeadlessProcess, v bool) {
	m.mu.Lock()
	hp.IsGenerating = v
	m.mu.Unlock()
}

func (m *Manager) has(id string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.processes[id]
	return ok
}

func withShortGrace(t *testing.T) {
	t.Helper()
	origGrace, origRecheck := gracePeriod, generatingRecheck
	gracePeriod, generatingRecheck = 20*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { gracePeriod, generatingRecheck = origGrace, origRecheck })
}

// An idle, client-less process is reaped once grace expires.
func TestGracePeriodReapsIdleProcess(t *testing.T) {
	withShortGrace(t)
	m := NewManager()
	m.processes["s"] = fakeProcess("s")

	m.StartGracePeriod("s")
	time.Sleep(80 * time.Millisecond)

	if m.has("s") {
		t.Fatal("idle process should have been reaped after grace expired")
	}
}

// A reconnect within the grace window cancels the reap.
func TestCancelGracePeriodKeepsProcess(t *testing.T) {
	withShortGrace(t)
	m := NewManager()
	m.processes["s"] = fakeProcess("s")

	m.StartGracePeriod("s")
	m.CancelGracePeriod("s")
	time.Sleep(80 * time.Millisecond)

	if !m.has("s") {
		t.Fatal("process should survive when grace is cancelled by a reconnect")
	}
}

// The core durability guarantee: a background turn still generating when grace
// expires is NOT killed — it keeps running — and is only reaped once it goes idle.
func TestGracePeriodKeepsGeneratingProcessAlive(t *testing.T) {
	withShortGrace(t)
	m := NewManager()
	hp := fakeProcess("s")
	hp.IsGenerating = true
	m.processes["s"] = hp

	m.StartGracePeriod("s")
	time.Sleep(100 * time.Millisecond) // several grace + recheck cycles

	if !m.has("s") {
		t.Fatal("generating process must survive grace expiry (background turn aborted otherwise)")
	}

	// Turn finishes -> next recheck should reap it.
	m.setGenerating(hp, false)
	time.Sleep(80 * time.Millisecond)

	if m.has("s") {
		t.Fatal("process should be reaped once it finishes generating with no client")
	}
}
