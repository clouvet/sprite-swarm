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

	// override flips merge; the rest falls through from the defaults.
	eff := p.Effective(override)
	if eff.Merge != "auto-on-green" {
		t.Errorf("override should win for merge, got %q", eff.Merge)
	}
	if eff.SpawnMaxTotal != 50 {
		t.Errorf("default spawn cap should be 50, got %d", eff.SpawnMaxTotal)
	}
	if eff.PermissionMode != "acceptEdits" {
		t.Errorf("permission mode should fall through from defaults, got %q", eff.PermissionMode)
	}
	if eff.ModifyPolicy {
		t.Error("modify_policy must default false (the guardrail)")
	}

	// No override → default cap 50, default merge human.
	base := p.Effective(PolicySet{})
	if base.SpawnMaxTotal != 50 || base.Merge != "human" {
		t.Errorf("default effective wrong: cap=%d merge=%q", base.SpawnMaxTotal, base.Merge)
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

	// 0 sprites registered → under cap → allowed.
	if ok, _ := home.SpawnAllowed(context.Background()); !ok {
		t.Fatal("should allow spawn under cap")
	}
	// Register 2 sprites (at cap of 2, counting all roster entries) → refused.
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
