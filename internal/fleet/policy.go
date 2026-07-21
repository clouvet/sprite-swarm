package fleet

import (
	"context"
	"encoding/json"
	"path"
)

// Capability/policy control plane (DESIGN §6.2): a layered policy — fleet
// defaults → per-agent override, most-specific wins. It lives in the brain
// (fleet/config/policy.json + fleet/<id>/policy.json), is read on boot, and
// maps onto real enforcement primitives (spawn cap, permission mode, …).
//
// Guardrail (DESIGN §6.2): can-modify-policy is human-held. Agents only READ
// fleet/config/*; no agent code writes it. The storage-permission hardening
// (per-prefix-scoped creds so it's enforced, not merely convention) is
// remaining hardening.

const policyConfigKey = "fleet/config/policy.json"

func agentPolicyKey(id string) string { return path.Join("fleet", id, "policy.json") }

// PolicySet is a (partial) set of powers. Pointers/omitempty distinguish "unset"
// (inherit) from an explicit value, so merge is "most-specific non-nil wins".
type PolicySet struct {
	Spawn        *SpawnPolicy `json:"spawn,omitempty"`
	Merge        *string      `json:"merge,omitempty"` // "human" | "auto-on-green" | "auto"
	PushMain     *bool        `json:"push_main,omitempty"`
	Spend        *SpendPolicy `json:"spend,omitempty"`
	Secrets      []string     `json:"secrets,omitempty"`
	Tools        *ToolsPolicy `json:"tools,omitempty"`
	ModifyPolicy *bool        `json:"modify_policy,omitempty"`
}

type SpawnPolicy struct {
	Allowed    *bool   `json:"allowed,omitempty"`
	MaxTotal   *int    `json:"max_total,omitempty"`
	NamePrefix *string `json:"name_prefix,omitempty"`
}
type SpendPolicy struct {
	DailyUSDCap *float64 `json:"daily_usd_cap,omitempty"`
}
type ToolsPolicy struct {
	PermissionMode *string  `json:"permission_mode,omitempty"`
	Deny           []string `json:"deny,omitempty"`
}

// Policy is the control-plane document (fleet/config/policy.json).
type Policy struct {
	Version  int       `json:"version"`
	Defaults PolicySet `json:"defaults"`
}

// Effective is the fully-resolved policy for one agent (no pointers).
type Effective struct {
	SpawnAllowed     bool     `json:"spawn_allowed"`
	SpawnMaxTotal    int      `json:"spawn_max_total"`
	SpawnNamePrefix  string   `json:"spawn_name_prefix"`
	Merge            string   `json:"merge"`
	PushMain         bool     `json:"push_main"`
	SpendDailyUSDCap float64  `json:"spend_daily_usd_cap"`
	Secrets          []string `json:"secrets"`
	PermissionMode   string   `json:"permission_mode"`
	ToolsDeny        []string `json:"tools_deny"`
	ModifyPolicy     bool     `json:"modify_policy"`
}

// DefaultPolicy is the built-in baseline (DESIGN §6.2 example) used when the
// control-plane doc is absent: conservative — human merge, no push to main, no
// self-modify; every sprite may spawn up to 50 total.
func DefaultPolicy() Policy {
	b := func(v bool) *bool { return &v }
	i := func(v int) *int { return &v }
	s := func(v string) *string { return &v }
	f := func(v float64) *float64 { return &v }
	return Policy{
		Version: 1,
		Defaults: PolicySet{
			Spawn:        &SpawnPolicy{Allowed: b(true), MaxTotal: i(50), NamePrefix: s("wk-")},
			Merge:        s("human"),
			PushMain:     b(false),
			Spend:        &SpendPolicy{DailyUSDCap: f(50)},
			Secrets:      []string{"github", "anthropic"},
			Tools:        &ToolsPolicy{PermissionMode: s("acceptEdits"), Deny: []string{"Bash(rm -rf /*)"}},
			ModifyPolicy: b(false),
		},
	}
}

// Effective resolves powers with an optional per-agent override:
// effective = merge(defaults, override) — most-specific non-nil wins.
func (p Policy) Effective(override PolicySet) Effective {
	return resolve([]PolicySet{p.Defaults, override})
}

// resolve folds policy layers (least → most specific); each non-nil field wins.
func resolve(layers []PolicySet) Effective {
	var e Effective
	for _, l := range layers {
		if l.Spawn != nil {
			if l.Spawn.Allowed != nil {
				e.SpawnAllowed = *l.Spawn.Allowed
			}
			if l.Spawn.MaxTotal != nil {
				e.SpawnMaxTotal = *l.Spawn.MaxTotal
			}
			if l.Spawn.NamePrefix != nil {
				e.SpawnNamePrefix = *l.Spawn.NamePrefix
			}
		}
		if l.Merge != nil {
			e.Merge = *l.Merge
		}
		if l.PushMain != nil {
			e.PushMain = *l.PushMain
		}
		if l.Spend != nil && l.Spend.DailyUSDCap != nil {
			e.SpendDailyUSDCap = *l.Spend.DailyUSDCap
		}
		if l.Secrets != nil {
			e.Secrets = l.Secrets
		}
		if l.Tools != nil {
			if l.Tools.PermissionMode != nil {
				e.PermissionMode = *l.Tools.PermissionMode
			}
			if l.Tools.Deny != nil {
				e.ToolsDeny = l.Tools.Deny
			}
		}
		if l.ModifyPolicy != nil {
			e.ModifyPolicy = *l.ModifyPolicy
		}
	}
	return e
}

// LoadPolicy reads the control-plane doc (or the built-in default if absent).
func (s *Service) LoadPolicy(ctx context.Context) Policy {
	data, err := s.brain.Get(ctx, policyConfigKey)
	if err != nil {
		return DefaultPolicy()
	}
	var p Policy
	if json.Unmarshal(data, &p) != nil {
		return DefaultPolicy()
	}
	return p
}

// agentOverride reads this agent's per-agent override (empty if none).
func (s *Service) agentOverride(ctx context.Context, id string) PolicySet {
	data, err := s.brain.Get(ctx, agentPolicyKey(id))
	if err != nil {
		return PolicySet{}
	}
	var ps PolicySet
	_ = json.Unmarshal(data, &ps)
	return ps
}

// EffectivePolicy resolves this agent's effective powers from the brain, layered
// over the built-in defaults so a partial control-plane doc inherits sane
// baselines (and an explicit 0/false still wins). Order, least→most specific:
// builtin defaults → doc defaults → per-agent override.
func (s *Service) EffectivePolicy(ctx context.Context) Effective {
	builtin := DefaultPolicy()
	doc := s.LoadPolicy(ctx)
	override := s.agentOverride(ctx, s.id)
	return resolve([]PolicySet{
		builtin.Defaults,
		doc.Defaults,
		override,
	})
}

// EffectivePolicyValue is the any-returning wrapper for the HTTP layer.
func (s *Service) EffectivePolicyValue(ctx context.Context) (interface{}, error) {
	return s.EffectivePolicy(ctx), nil
}

// SpawnAllowed reports whether this agent may spawn now, given its effective
// policy and the current fleet size (cap enforcement, §6.2). It counts every
// roster entry against SpawnMaxTotal.
func (s *Service) SpawnAllowed(ctx context.Context) (bool, string) {
	eff := s.EffectivePolicy(ctx)
	if !eff.SpawnAllowed {
		return false, "spawn not allowed by policy"
	}
	if eff.SpawnMaxTotal < 0 {
		return true, "" // negative = no numeric cap
	}
	roster, err := s.roster(ctx)
	if err != nil {
		return true, "" // fail open on read error; don't block legitimate work
	}
	if len(roster) >= eff.SpawnMaxTotal {
		return false, "spawn cap reached (max_total)"
	}
	return true, ""
}
