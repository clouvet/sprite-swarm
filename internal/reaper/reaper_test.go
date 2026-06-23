package reaper

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeRegistry struct {
	targets []string
	removed []string
}

func (f *fakeRegistry) ReapTargets(_ context.Context, _ time.Duration) ([]string, error) {
	return f.targets, nil
}
func (f *fakeRegistry) RemoveAgent(_ context.Context, id string) error {
	f.removed = append(f.removed, id)
	return nil
}

type fakeDestroyer struct {
	available bool
	destroyed []string
	failOn    map[string]bool
}

func (f *fakeDestroyer) Available() bool { return f.available }
func (f *fakeDestroyer) Destroy(_ context.Context, name string) error {
	if f.failOn[name] {
		return errors.New("destroy failed")
	}
	f.destroyed = append(f.destroyed, name)
	return nil
}

func TestReapOnceDestroysThenRemoves(t *testing.T) {
	reg := &fakeRegistry{targets: []string{"w1", "w2"}}
	dst := &fakeDestroyer{available: true}
	r := New(reg, dst, time.Minute, 5*time.Minute)

	r.reapOnce(context.Background())

	if len(dst.destroyed) != 2 || len(reg.removed) != 2 {
		t.Fatalf("expected 2 destroyed + 2 removed, got destroyed=%v removed=%v", dst.destroyed, reg.removed)
	}
}

func TestReapOnceKeepsBrainEntryWhenDestroyFails(t *testing.T) {
	reg := &fakeRegistry{targets: []string{"w1", "w2"}}
	dst := &fakeDestroyer{available: true, failOn: map[string]bool{"w1": true}}
	r := New(reg, dst, time.Minute, 5*time.Minute)

	r.reapOnce(context.Background())

	// w1's destroy failed → its brain entry must remain (retry next scan).
	for _, id := range reg.removed {
		if id == "w1" {
			t.Fatal("must not remove brain entry when sprite destroy failed")
		}
	}
	if len(dst.destroyed) != 1 || dst.destroyed[0] != "w2" {
		t.Fatalf("expected only w2 destroyed, got %v", dst.destroyed)
	}
}
