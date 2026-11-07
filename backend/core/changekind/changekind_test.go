// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package changekind

import (
	"sort"
	"strings"
	"testing"
)

func mustPanic(t *testing.T, want string, f func()) {
	t.Helper()
	defer func() {
		t.Helper()
		r := recover()
		if r == nil {
			t.Fatalf("expected panic containing %q, got none", want)
		}
		if msg, _ := r.(string); !strings.Contains(msg, want) {
			t.Fatalf("panic = %v, want it to contain %q", r, want)
		}
	}()
	f()
}

func TestRegisterWithoutEffectClassPanics(t *testing.T) {
	r := NewRegistry()
	mustPanic(t, "no effect class", func() {
		r.Register(Kind{Name: "device.profile", Version: 1})
	})
	mustPanic(t, "no effect class", func() {
		r.Register(Kind{Name: "device.profile", Effect: Effect(99), Version: 1})
	})
	if len(r.Kinds()) != 0 {
		t.Fatalf("a refused kind must not be registered, got %v", r.Kinds())
	}
}

func TestRegisterWithoutVersionPanics(t *testing.T) {
	r := NewRegistry()
	mustPanic(t, "versions start at 1", func() {
		r.Register(Kind{Name: "device.profile", Effect: EffectReversible})
	})
	mustPanic(t, "versions start at 1", func() {
		r.Register(Kind{Name: "device.profile", Effect: EffectReversible, Version: -1})
	})
}

func TestGovernanceKindCannotBeAgentWritable(t *testing.T) {
	r := NewRegistry()
	mustPanic(t, "cannot be agent-writable", func() {
		r.Register(Kind{Name: "governance.x", Effect: EffectReversible, Governance: true, AgentWritable: true, Version: 1})
	})
	if got := r.AgentWritable(); len(got) != 0 {
		t.Fatalf("agent-writable set = %v, want empty", got)
	}
}

func TestBadAndDuplicateNamesPanic(t *testing.T) {
	r := NewRegistry()
	for _, n := range []string{"", "Device", "a..b", ".a", "a.", "a b", "1a", "a-b"} {
		mustPanic(t, "must be dotted", func() {
			r.Register(Kind{Name: n, Effect: EffectReversible, Version: 1})
		})
	}
	r.Register(Kind{Name: "a.b", Effect: EffectReversible, Version: 1})
	mustPanic(t, "registered twice", func() {
		r.Register(Kind{Name: "a.b", Effect: EffectCompensable, Version: 2})
	})
}

func TestAgentWritableSetAndLookup(t *testing.T) {
	r := NewRegistry()
	r.Register(Kind{Name: "dashboard.layout", Effect: EffectReversible, AgentWritable: true, Version: 1})
	r.Register(Kind{Name: "device.ota", Effect: EffectIrreversible, AgentWritable: true, Version: 3})
	r.Register(Kind{Name: "tenant.tier", Effect: EffectCompensable, Version: 1})

	got := r.AgentWritable()
	if len(got) != 2 || got[0].Name != "dashboard.layout" || got[1].Name != "device.ota" {
		t.Fatalf("agent-writable = %v", got)
	}
	k, ok := r.Lookup("device.ota")
	if !ok || k.Version != 3 || k.Effect != EffectIrreversible {
		t.Fatalf("lookup = %v %v", k, ok)
	}
	if _, ok := r.Lookup("nope"); ok {
		t.Fatal("lookup of an unregistered kind must miss")
	}
}

func TestAutoEligible(t *testing.T) {
	cases := []struct {
		k    Kind
		want bool
	}{
		{Kind{Effect: EffectReversible}, true},
		{Kind{Effect: EffectCompensable}, true},
		{Kind{Effect: EffectIrreversible}, false},
		{Kind{Effect: EffectReversible, Governance: true}, false},
		{Kind{}, false},
		{Kind{Effect: Effect(99)}, false},
	}
	for _, c := range cases {
		if got := c.k.AutoEligible(); got != c.want {
			t.Errorf("%+v AutoEligible = %v, want %v", c.k, got, c.want)
		}
	}
}

func TestEffectString(t *testing.T) {
	want := map[Effect]string{0: "invalid", EffectReversible: "reversible", EffectCompensable: "compensable", EffectIrreversible: "irreversible", 42: "invalid"}
	for e, s := range want {
		if e.String() != s {
			t.Errorf("Effect(%d).String() = %q, want %q", int(e), e.String(), s)
		}
	}
}

// The governance plane is enumerated by a completeness check in both directions: every
// control the design names is registered, and nothing else is in the plane.
func TestDefaultHoldsExactlyTheGovernancePlane(t *testing.T) {
	want := []string{
		"governance.agentpolicy", "governance.conformancecheck", "governance.holdoutset",
		"governance.invariant", "governance.killswitch", "governance.mandate",
		"governance.outcomewatch", "governance.routertable", "governance.samplingplan",
	}
	var got []string
	for _, k := range Default().Governance() {
		got = append(got, k.Name)
		if k.AgentWritable || !k.Governance || !k.Effect.Valid() || k.Version < 1 {
			t.Errorf("governance kind %+v violates the registry rules", k)
		}
		if k.AutoEligible() {
			t.Errorf("governance kind %q must never be auto-eligible", k.Name)
		}
	}
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("governance plane = %v, want %v", got, want)
	}
	if len(GovernanceKinds()) != len(want) {
		t.Fatalf("GovernanceKinds lists %d, want %d", len(GovernanceKinds()), len(want))
	}
	for _, k := range Default().AgentWritable() {
		if k.Governance {
			t.Errorf("Default agent-writable set contains governance kind %q", k.Name)
		}
	}
}

// Every kind in Default, whoever registered it, satisfies the invariants. A later lane
// that registers a kind into Default is checked here without editing this test.
func TestEveryDefaultKindIsWellFormed(t *testing.T) {
	for _, k := range Default().Kinds() {
		if err := k.validate(); err != nil {
			t.Errorf("%v", err)
		}
	}
}

// A caller of the process-wide registry gets a read-only view: it has no way to register
// into it or to replace it, and a kind it registers through Register is then visible.
func TestDefaultIsReadOnlyAndRegisterIsTheWriter(t *testing.T) {
	var v any = Default()
	if _, ok := v.(interface{ Register(Kind) }); ok {
		t.Fatal("Default() must not expose Register")
	}
	if _, ok := v.(*Registry); ok {
		t.Fatal("Default() must not hand out the concrete registry")
	}
}

func TestRegisterAddsToDefault(t *testing.T) {
	const name = "test.registeraddstodefault"
	if _, ok := Default().Lookup(name); ok {
		t.Fatal("kind registered before the test")
	}
	Register(Kind{Name: name, Effect: EffectCompensable, Version: 1})
	if k, ok := Default().Lookup(name); !ok || k.Effect != EffectCompensable {
		t.Fatalf("lookup after Register = %+v %v", k, ok)
	}
}
