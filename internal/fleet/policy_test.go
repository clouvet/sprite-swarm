package fleet

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/clouvet/sprite-swarm/internal/config"
)

func TestEffectivePolicyPrecedence(t *testing.T) {
	p := DefaultPolicy()
	merge := "auto-on-green"
	override := PolicySet{Merge: &merge} // per-agent override

	// home: role raises spawn cap to 50; override flips merge.
	home := p.Effective("home", override)
	if home.Merge != "auto-on-green" {
		t.Errorf("override should win for merge, got %q", home.Merge)
	}
	if home.SpawnMaxTotal != 50 {
		t.Errorf("home role should raise spawn cap to 50, got %d", home.SpawnMaxTotal)
	}
	if home.PermissionMode != "acceptEdits" {
		t.Errorf("permission mode should fall through from defaults, got %q", home.PermissionMode)
	}
	if home.ModifyPolicy {
		t.Error("modify_policy must default false (the guardrail)")
	}

	// worker: no role bump → default cap 10, default merge human.
	worker := p.Effective("worker", PolicySet{})
	if worker.SpawnMaxTotal != 10 || worker.Merge != "human" {
		t.Errorf("worker effective wrong: cap=%d merge=%q", worker.SpawnMaxTotal, worker.Merge)
	}
}

func TestEffectivePolicyFromBrain(t *testing.T) {
	brain := newFakeBrain()
	// Control-plane writes a custom policy doc.
	cap3 := 3
	doc := Policy{Version: 1, Defaults: PolicySet{Spawn: &SpawnPolicy{Allowed: boolp(true), MaxTotal: &cap3}}}
	data, _ := json.Marshal(doc)
	brain.Put(context.Background(), policyConfigKey, data)

	svc := newService(brain, config.Config{AgentID: "home"})
	svc.role = "home"
	eff := svc.EffectivePolicy(context.Background())
	if eff.SpawnMaxTotal != 3 {
		t.Fatalf("expected cap 3 from brain doc, got %d", eff.SpawnMaxTotal)
	}
}

func TestSpawnAllowedCap(t *testing.T) {
	brain := newFakeBrain()
	cap2 := 2
	doc := Policy{Version: 1, Defaults: PolicySet{Spawn: &SpawnPolicy{Allowed: boolp(true), MaxTotal: &cap2}}}
	data, _ := json.Marshal(doc)
	brain.Put(context.Background(), policyConfigKey, data)

	home := newService(brain, config.Config{AgentID: "home"})
	home.role = "home"
	home.Register(context.Background())

	// 0 workers → allowed.
	if ok, _ := home.SpawnAllowed(context.Background()); !ok {
		t.Fatal("should allow spawn under cap")
	}
	// Add 2 workers (at cap) → refused.
	for _, id := range []string{"wk-1", "wk-2"} {
		w := newService(brain, config.Config{AgentID: id})
		w.Register(context.Background())
	}
	if ok, reason := home.SpawnAllowed(context.Background()); ok {
		t.Fatalf("should refuse spawn at cap, got allowed (reason=%q)", reason)
	}
}

func TestSpawnDisallowedByPolicy(t *testing.T) {
	brain := newFakeBrain()
	doc := Policy{Version: 1, Defaults: PolicySet{Spawn: &SpawnPolicy{Allowed: boolp(false)}}}
	data, _ := json.Marshal(doc)
	brain.Put(context.Background(), policyConfigKey, data)
	svc := newService(brain, config.Config{AgentID: "home"})
	if ok, _ := svc.SpawnAllowed(context.Background()); ok {
		t.Fatal("policy disallows spawn, but SpawnAllowed returned true")
	}
}

func boolp(b bool) *bool { return &b }
