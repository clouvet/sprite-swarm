// Package keepalive holds a sprite awake while it has active work, using the
// Sprite Tasks API on the LOCAL management socket (/.sprite/api.sock).
//
// Why this exists: the platform pauses an idle sprite after a short window, and
// neither CPU work nor a background service prevents it — only "the current run"
// being held active does. An autonomous worker churning on a dispatched task with
// no attached session would otherwise suspend mid-task. The Tasks API
// (https://docs.sprites.dev/keeping-sprites-running) is the fix: while at least
// one task is live the sprite runs. It's the local socket, so a sprite holds
// ITSELF awake — no cross-sprite/OAuth issue, and identical on every sprite.
//
// We hold a task while the agent is non-idle (Claude generating or a client
// attached) and release it when idle, so the sprite stays up exactly as long as
// there's work and suspends (saving resources) once there isn't.
package keepalive

import (
	"bytes"
	"context"
	"log"
	"net"
	"net/http"
	"time"
)

const (
	socketPath  = "/.sprite/api.sock"
	taskName    = "sprite-agent"
	taskExpiry  = "2m"
	refreshEach = 30 * time.Second
)

// Run holds the sprite awake while active() reports work in progress, refreshing
// the task hold periodically and releasing it when idle. Blocks until ctx is done
// (run it in a goroutine). No-ops gracefully when the socket is absent (e.g. off-sprite).
func Run(ctx context.Context, active func() bool) {
	client := &http.Client{
		Timeout: 8 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
			},
		},
	}
	if _, err := net.Dial("unix", socketPath); err != nil {
		log.Printf("keepalive: local sprite socket unavailable (%v); not holding the sprite awake", err)
		return
	}
	log.Printf("keepalive: holding the sprite awake while working (Tasks API)")

	ticker := time.NewTicker(refreshEach)
	defer ticker.Stop()
	held := false
	step := func() {
		if active() {
			if err := hold(ctx, client); err != nil {
				log.Printf("keepalive: hold failed: %v", err)
			} else {
				held = true
			}
		} else if held {
			release(client)
			held = false
		}
	}
	step() // hold immediately if there's already work, don't wait a full tick
	for {
		select {
		case <-ctx.Done():
			if held {
				release(client)
			}
			return
		case <-ticker.C:
			step()
		}
	}
}

// hold upserts the keep-awake task (PUT /v1/tasks/<name>), refreshing its expiry.
func hold(ctx context.Context, client *http.Client) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut,
		"http://sprite/v1/tasks/"+taskName, bytes.NewReader([]byte(`{"expire":"`+taskExpiry+`"}`)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// release deletes the task so the sprite can pause once idle.
func release(client *http.Client) {
	req, err := http.NewRequest(http.MethodDelete, "http://sprite/v1/tasks/"+taskName, nil)
	if err != nil {
		return
	}
	if resp, err := client.Do(req); err == nil {
		resp.Body.Close()
	}
}
