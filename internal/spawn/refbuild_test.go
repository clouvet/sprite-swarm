package spawn

import (
	"strings"
	"testing"

	"github.com/clouvet/sprite-swarm/internal/config"
)

// TestRefArtifactKeyNeverClobbersFleet is the safety invariant: a ref build must be
// staged under a key DISTINCT from the shared fleet artifact, or spawning an
// experimental branch would poison the whole fleet's self-update.
func TestRefArtifactKeyNeverClobbersFleet(t *testing.T) {
	refs := []string{"main", "experimental/pi-runtime", "feature/x", "v0.1.5", "weird ref/with spaces"}
	seen := map[string]bool{}
	for _, r := range refs {
		k := refArtifactKey(r)
		if k == config.ArtifactKey {
			t.Fatalf("refArtifactKey(%q) = %q collides with the shared fleet ArtifactKey", r, k)
		}
		if !strings.HasPrefix(k, "fleet/artifacts/ref-") {
			t.Errorf("refArtifactKey(%q) = %q, want a ref- namespaced key", r, k)
		}
		seen[k] = true
	}
	if len(seen) != len(refs) {
		t.Errorf("expected distinct keys per ref, got collisions: %v", seen)
	}
}

// TestRefSpawnPinsBuild verifies a ref spawn bakes in SPRITE_AGENT_BOOT_UPDATE=0 so
// the experimental sprite can't drift to the fleet build, and records the ref.
func TestRefSpawnPinsBuild(t *testing.T) {
	a := &apiSpawner{cfg: config.Config{}, newID: func() string { return "abcd1234" }}
	cr := a.buildCreateRequest(Request{NamePrefix: "wk-", Ref: "experimental/pi-runtime"})
	if cr.Env["SPRITE_AGENT_BOOT_UPDATE"] != "0" {
		t.Errorf("ref spawn must pin the build (SPRITE_AGENT_BOOT_UPDATE=0), got %q", cr.Env["SPRITE_AGENT_BOOT_UPDATE"])
	}
	if !strings.Contains(cr.Env["SPRITE_AGENT_ARTIFACT"], "experimental/pi-runtime") {
		t.Errorf("ref spawn should record the ref in SPRITE_AGENT_ARTIFACT, got %q", cr.Env["SPRITE_AGENT_ARTIFACT"])
	}

	// A normal spawn must NOT pin (it should track the fleet build).
	cr2 := a.buildCreateRequest(Request{NamePrefix: "wk-"})
	if _, pinned := cr2.Env["SPRITE_AGENT_BOOT_UPDATE"]; pinned {
		t.Errorf("a normal spawn must not pin the build")
	}
}
