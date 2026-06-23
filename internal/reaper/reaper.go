// Package reaper destroys spawned workers that are done or idle and cleans up
// their brain entries, so the fleet doesn't accumulate zombie sprites. It runs
// only on agents that can spawn (i.e. hold a sprites token); home is never
// reaped (the policy lives in fleet.ReapTargets).
package reaper

import (
	"context"
	"log"
	"time"
)

// Registry is the brain side: which agents to reap, and removing their entries.
type Registry interface {
	ReapTargets(ctx context.Context, deadReapAfter time.Duration) ([]string, error)
	RemoveAgent(ctx context.Context, id string) error
}

// Destroyer is the platform side: destroy a sprite by name.
type Destroyer interface {
	Available() bool
	Destroy(ctx context.Context, name string) error
}

// Reaper periodically reaps reapable workers.
type Reaper struct {
	reg           Registry
	dst           Destroyer
	interval      time.Duration
	deadReapAfter time.Duration
}

// New builds a Reaper. interval is the scan cadence; deadReapAfter is how long a
// worker's heartbeat must be stale before its (crashed) sprite is cleaned up.
func New(reg Registry, dst Destroyer, interval, deadReapAfter time.Duration) *Reaper {
	if interval <= 0 {
		interval = time.Minute
	}
	if deadReapAfter <= 0 {
		deadReapAfter = 5 * time.Minute
	}
	return &Reaper{reg: reg, dst: dst, interval: interval, deadReapAfter: deadReapAfter}
}

// Run scans on an interval until ctx is cancelled. Only runs if destroying is
// available (a sprites token is present).
func (r *Reaper) Run(ctx context.Context) {
	if r.dst == nil || !r.dst.Available() {
		log.Printf("reaper: disabled (no spawn capability)")
		return
	}
	log.Printf("reaper: enabled (interval=%s, dead-reap-after=%s)", r.interval, r.deadReapAfter)
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.reapOnce(ctx)
		}
	}
}

// reapOnce destroys each reap target (sprite first, then its brain entry).
func (r *Reaper) reapOnce(ctx context.Context) {
	scanCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	targets, err := r.reg.ReapTargets(scanCtx, r.deadReapAfter)
	if err != nil {
		log.Printf("reaper: scan failed: %v", err)
		return
	}
	for _, id := range targets {
		if err := r.dst.Destroy(scanCtx, id); err != nil {
			log.Printf("reaper: destroy %s failed: %v (leaving brain entry for retry)", id, err)
			continue
		}
		if err := r.reg.RemoveAgent(scanCtx, id); err != nil {
			log.Printf("reaper: remove brain entry %s failed: %v", id, err)
			continue
		}
		log.Printf("reaper: reaped %s (destroyed sprite + removed brain entry)", id)
	}
}
