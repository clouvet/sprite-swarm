// Package reaper destroys spawned workers that have been explicitly marked done
// and cleans up the brain entries of workers whose sprite is actually gone, so the
// fleet doesn't accumulate zombie sprites. There is no idle-based auto-reaping —
// teardown is explicit (POST /api/fleet/destroy, or a worker's own /api/fleet/done);
// a suspended/idle worker is left alone. It runs only on agents that can spawn
// (i.e. hold a sprites token); home is never reaped (policy in fleet.ReapTargets).
package reaper

import (
	"context"
	"log"
	"time"
)

// Registry is the brain side: which agents to reap/clean, and removing entries.
type Registry interface {
	ReapTargets(ctx context.Context) ([]string, error)
	StaleWorkers(ctx context.Context, staleAfter time.Duration) ([]string, error)
	RemoveAgent(ctx context.Context, id string) error
}

// Destroyer is the platform side: destroy a sprite, and check whether it exists.
type Destroyer interface {
	Available() bool
	Destroy(ctx context.Context, name string) error
	Exists(ctx context.Context, name string) (bool, error)
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

// reapOnce destroys explicitly-done workers, and separately cleans the brain
// entries of workers whose sprite is actually gone. A stale heartbeat alone is NOT
// destroyed — a suspended (idle/awaiting-follow-up) worker is kept alive.
func (r *Reaper) reapOnce(ctx context.Context) {
	scanCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// 1. Explicit done → destroy the sprite + remove its brain entry.
	targets, err := r.reg.ReapTargets(scanCtx)
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
		log.Printf("reaper: reaped %s (done → destroyed sprite + removed brain entry)", id)
	}

	// 2. Stale heartbeat → only clean the brain entry if the sprite is truly gone.
	//    A suspended worker still exists and is left untouched (durable workspace).
	stale, err := r.reg.StaleWorkers(scanCtx, r.deadReapAfter)
	if err != nil {
		return
	}
	for _, id := range stale {
		exists, err := r.dst.Exists(scanCtx, id)
		if err != nil || exists {
			continue // exists (suspended) or unknown → keep it
		}
		if err := r.reg.RemoveAgent(scanCtx, id); err == nil {
			log.Printf("reaper: cleaned orphaned brain entry %s (sprite gone)", id)
		}
	}
}
