package fleet

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/clouvet/sprite-swarm/internal/config"
)

func TestOperatorIdentityDefaultSetClear(t *testing.T) {
	s := newService(newFakeBrain(), config.Config{AgentID: "a"})
	s.now = time.Now
	ctx := context.Background()

	// Unset by default — no personal data unless this fleet configured it.
	if id := s.OperatorIdentity(ctx); id != "" {
		t.Errorf("default operator = %q, want empty", id)
	}
	if ctxBlock := s.operatorContext(ctx); ctxBlock != "" {
		t.Errorf("unset operator should render nothing, got:\n%s", ctxBlock)
	}

	if err := s.SetOperatorIdentity(ctx, "  Ada — gh:ada  "); err != nil {
		t.Fatalf("set operator: %v", err)
	}
	// Persisted fleet-wide: a fresh service on the same brain picks it up, trimmed.
	s2 := newService(s.brain, config.Config{AgentID: "b"})
	s2.now = time.Now
	if id := s2.OperatorIdentity(ctx); id != "Ada — gh:ada" {
		t.Errorf("persisted operator = %q, want %q", id, "Ada — gh:ada")
	}

	// Empty value clears it.
	if err := s2.SetOperatorIdentity(ctx, ""); err != nil {
		t.Fatalf("clear operator: %v", err)
	}
	s3 := newService(s2.brain, config.Config{AgentID: "c"})
	s3.now = time.Now
	if id := s3.OperatorIdentity(ctx); id != "" {
		t.Errorf("cleared operator = %q, want empty", id)
	}
}

func TestOperatorContextBlock(t *testing.T) {
	s := newService(newFakeBrain(), config.Config{AgentID: "a"})
	s.now = time.Now
	ctx := context.Background()
	if err := s.SetOperatorIdentity(ctx, "Ada — gh:ada"); err != nil {
		t.Fatalf("set operator: %v", err)
	}
	block := s.operatorContext(ctx)
	for _, want := range []string{"## Operator", "Ada — gh:ada", "not a third party", "you"} {
		if !strings.Contains(strings.ToLower(block), strings.ToLower(want)) {
			t.Errorf("operator block missing %q in:\n%s", want, block)
		}
	}
}
