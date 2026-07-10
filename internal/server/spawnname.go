package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"time"
)

// maxWorkerSlug caps the descriptive part of a worker name so wk-<slug> stays
// readable (and within sprite-name limits).
const maxWorkerSlug = 32

// workerSlug turns a free-text task label ("PostHog integration") into a worker
// name segment ("posthog-integration"): lowercased, runs of non-alphanumerics
// folded to a single hyphen, trimmed, and length-capped. Returns "" when the
// label has no usable characters, so the caller falls back to the random scheme.
func workerSlug(label string) string {
	var b strings.Builder
	prevHyphen := false
	for _, r := range strings.ToLower(label) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevHyphen = false
		default:
			if b.Len() > 0 && !prevHyphen {
				b.WriteByte('-')
				prevHyphen = true
			}
		}
	}
	slug := strings.Trim(b.String(), "-")
	if len(slug) > maxWorkerSlug {
		slug = strings.Trim(slug[:maxWorkerSlug], "-")
	}
	return slug
}

// uniqueWorkerName returns name, or name-<hex> when a sprite already holds it, so
// two similarly-described workers don't collide. On a lookup error it returns the
// clean name (a genuine duplicate then fails loudly at create, which is rare).
func (s *Server) uniqueWorkerName(name string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if exists, err := s.spawner.Exists(ctx, name); err == nil && exists {
		return name + "-" + shortHex()
	}
	return name
}

// shortHex is 4 hex chars for disambiguating a colliding worker name.
func shortHex() string {
	var b [2]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "00"
	}
	return hex.EncodeToString(b[:])
}
