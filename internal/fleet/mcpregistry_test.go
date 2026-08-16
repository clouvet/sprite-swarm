package fleet

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/clouvet/sprite-swarm/internal/config"
)

func TestMCPRegistry(t *testing.T) {
	s := newService(newFakeBrain(), config.Config{AgentID: "a"})
	ctx := context.Background()

	// Empty to start.
	if reg, err := s.MCPRegistry(ctx); err != nil || len(reg) != 0 {
		t.Fatalf("fresh registry: %v (%d entries)", err, len(reg))
	}

	// A stdio server (command) and a remote server (url) both accepted.
	if err := s.SetMCPServer(ctx, "github", json.RawMessage(`{"command":"npx","args":["-y","@modelcontextprotocol/server-github"]}`)); err != nil {
		t.Fatalf("add stdio: %v", err)
	}
	if err := s.SetMCPServer(ctx, "remote", json.RawMessage(`{"url":"https://mcp.example.com/mcp"}`)); err != nil {
		t.Fatalf("add remote: %v", err)
	}

	reg, err := s.MCPRegistry(ctx)
	if err != nil || len(reg) != 2 {
		t.Fatalf("after adds: %v (%d entries)", err, len(reg))
	}
	if _, ok := reg["github"]; !ok {
		t.Error("github entry missing")
	}

	// Invalid configs are rejected (no command/url; non-object).
	for _, bad := range []string{`{"args":["x"]}`, `"just a string"`, `123`} {
		if err := s.SetMCPServer(ctx, "bad", json.RawMessage(bad)); err == nil {
			t.Errorf("expected rejection for %s", bad)
		}
	}
	// Empty name rejected.
	if err := s.SetMCPServer(ctx, "  ", json.RawMessage(`{"command":"x"}`)); err == nil {
		t.Error("expected empty-name rejection")
	}

	// Delete removes it; deleting an absent one is a no-op.
	if err := s.DeleteMCPServer(ctx, "github"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := s.DeleteMCPServer(ctx, "nope"); err != nil {
		t.Fatalf("delete absent: %v", err)
	}
	reg, _ = s.MCPRegistry(ctx)
	if _, ok := reg["github"]; ok {
		t.Error("github should be gone")
	}
	if len(reg) != 1 {
		t.Errorf("expected 1 entry after delete, got %d", len(reg))
	}
}

// Persistence is fleet-wide: a second Service on the same brain sees the registry.
func TestMCPRegistryShared(t *testing.T) {
	brain := newFakeBrain()
	a := newService(brain, config.Config{AgentID: "a"})
	if err := a.SetMCPServer(context.Background(), "x", json.RawMessage(`{"command":"echo"}`)); err != nil {
		t.Fatal(err)
	}
	b := newService(brain, config.Config{AgentID: "b"})
	reg, err := b.MCPRegistry(context.Background())
	if err != nil || len(reg) != 1 {
		t.Fatalf("peer read: %v (%d)", err, len(reg))
	}
}
