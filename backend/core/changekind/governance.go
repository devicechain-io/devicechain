// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package changekind

// The governance plane: the controls that bound an agent. None is agent-writable, and
// Default registers every one so that a later change cannot drop one quietly (the test
// enumerates this list against the registry in both directions).
const (
	KindMandate          = "governance.mandate"
	KindAgentPolicy      = "governance.agentpolicy"
	KindRouterTable      = "governance.routertable"
	KindSamplingPlan     = "governance.samplingplan"
	KindOutcomeWatch     = "governance.outcomewatch"
	KindInvariant        = "governance.invariant"
	KindHoldoutSet       = "governance.holdoutset"
	KindKillSwitch       = "governance.killswitch"
	KindConformanceCheck = "governance.conformancecheck"
)

// GovernanceKinds returns the full governance-plane list, in the order above. It returns
// a fresh slice each call so no caller can edit the list the registry is seeded from.
func GovernanceKinds() []string {
	return []string{
		KindMandate, KindAgentPolicy, KindRouterTable, KindSamplingPlan, KindOutcomeWatch,
		KindInvariant, KindHoldoutSet, KindKillSwitch, KindConformanceCheck,
	}
}

var defaultRegistry = newDefault()

// Default returns the read side of the process-wide registry, which starts with the
// governance plane in place. Packages that own a kind add it with Register, from init.
// Returning an interface, not the *Registry, is what stops a caller from overwriting the
// registry in place (*Default() = *NewRegistry()) and with it the governance plane.
func Default() Reader { return readOnly{defaultRegistry} }

// readOnly hides the registry's writer: a caller cannot assert the Reader back to a
// *Registry to reach Register.
type readOnly struct{ r *Registry }

func (v readOnly) Lookup(name string) (Kind, bool) { return v.r.Lookup(name) }
func (v readOnly) Kinds() []Kind                   { return v.r.Kinds() }
func (v readOnly) AgentWritable() []Kind           { return v.r.AgentWritable() }
func (v readOnly) Governance() []Kind              { return v.r.Governance() }

func newDefault() *Registry {
	r := NewRegistry()
	for _, name := range GovernanceKinds() {
		// Every governance control can be reverted by restoring its prior definition,
		// so each is reversible; none is agent-writable.
		r.Register(Kind{Name: name, Effect: EffectReversible, Governance: true, Version: 1})
	}
	return r
}

// Register adds a kind to Default and panics as Registry.Register does.
func Register(k Kind) { defaultRegistry.Register(k) }
