// Package claudecli manages the Claude Code CLI (`claude`) that every sprite runs the
// agent on. The CLI is installed and versioned OUTSIDE the sprite-agent binary (a self
// contained install under ~/.local), so a fleet binary roll never moves it — the model
// a chat can reach is gated by this CLI's version (e.g. a newer Opus alias resolves only
// on a new enough CLI). This package lets a sprite read and update that CLI, so the
// fleet can be kept current both on demand and automatically.
//
// Updating is always safe against live turns: `claude update` replaces the on-disk
// install, but a running `claude --resume` process keeps the binary it already mapped.
// Only NEW spawns pick up the update, so a mid-conversation update never disrupts an
// in-flight turn — it just means the next turn runs on the newer CLI.
package claudecli

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// updateTimeout caps a `claude update`: it can fetch a release over the network, but it
// must never wedge boot or a fleet roll. versionTimeout caps the quick `--version` read.
const (
	updateTimeout  = 3 * time.Minute
	versionTimeout = 15 * time.Second
)

// verRe pulls a semver (e.g. "2.1.281") out of `claude --version` output, which reads
// like "2.1.281 (Claude Code)".
var verRe = regexp.MustCompile(`\d+\.\d+\.\d+`)

// Version returns the installed Claude Code CLI version (e.g. "2.1.281"), or "" if it
// can't be determined (CLI missing, or output unrecognized).
func Version(ctx context.Context) string {
	out, err := run(ctx, versionTimeout, "--version")
	if err != nil {
		return ""
	}
	return verRe.FindString(out)
}

// Update runs `claude update` and reports the version before and after. It is
// best-effort: on failure (offline, no CLI, install error) it returns the error with
// whatever version was readable, so callers log rather than fail. after == before means
// the CLI was already current.
func Update(ctx context.Context) (before, after string, err error) {
	before = Version(ctx)
	if _, err = run(ctx, updateTimeout, "update"); err != nil {
		return before, before, err
	}
	return before, Version(ctx), nil
}

// StartAutoUpdate keeps the CLI current with no operator action: it updates once shortly
// after boot, then on the given interval, best-effort, while the process runs. A sprite
// suspend simply pauses the ticker (no keepalive fighting the platform) and it resumes on
// wake. Each update affects only subsequent claude spawns, never an in-flight turn.
// Returns immediately; cancel ctx to stop.
func StartAutoUpdate(ctx context.Context, interval time.Duration) {
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(30 * time.Second): // let boot settle before the first check
		}
		updateOnce(ctx)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				updateOnce(ctx)
			}
		}
	}()
}

func updateOnce(ctx context.Context) {
	before, after, err := Update(ctx)
	switch {
	case err != nil:
		log.Printf("claude CLI auto-update: %v (still %s)", err, before)
	case after != before:
		log.Printf("claude CLI auto-update: %s -> %s", before, after)
	default:
		log.Printf("claude CLI auto-update: already current (%s)", after)
	}
}

func run(ctx context.Context, timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "claude", args...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("claude %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(out.String()))
	}
	return out.String(), nil
}
