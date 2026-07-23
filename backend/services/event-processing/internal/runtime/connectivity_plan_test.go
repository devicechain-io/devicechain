// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-event-processing/internal/rules"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
)

// stateChange builds a resolved presence StateChange for a device under a profile version.
func stateChange(tenant, device, profileVersion string, occurred time.Time, state string, session uint64) *dmmodel.ResolvedEvent {
	return &dmmodel.ResolvedEvent{
		SourceDeviceToken:   device,
		ProfileVersionToken: profileVersion,
		OccurredTime:        occurred,
		EventType:           esmodel.StateChange,
		Payload:             &dmmodel.ResolvedStateChangePayload{State: state, SessionId: session},
	}
}

func connRule() rules.Rule {
	return rules.Rule{ID: ComposeRuleID("acme", "offline"), Name: "device offline", Type: rules.TypeConnectivity}
}

// TestPlanStateChangeFeedsConnectivityEdge proves a resolved StateChange fans a TYPED presence
// edge (not a Value/Match measurement) to a Connectivity rule, keyed by the source device, with
// the session epoch and direction intact.
func TestPlanStateChangeFeedsConnectivityEdge(t *testing.T) {
	base := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	reg := NewRuleRegistry([]ScopedRule{compileScoped(t, "acme", "p@1", connRule())})

	res := planEv(reg, 1, "acme", stateChange("acme", "d1", "p@1", base, "DISCONNECTED", 100))
	if len(res.Events) != 1 {
		t.Fatalf("a StateChange should feed one connectivity edge, got %d: %+v", len(res.Events), res.Events)
	}
	e := res.Events[0]
	if e.Presence == nil {
		t.Fatalf("connectivity event must carry a typed PresenceEdge, got %+v", e)
	}
	if e.Presence.SessionId != 100 || e.Presence.Connected {
		t.Fatalf("edge session/direction wrong: %+v", e.Presence)
	}
	if e.Key.Series != "d1" || e.Key.Rule != connRule().ID {
		t.Fatalf("edge must key on (rule, device); got %+v", e.Key)
	}

	// A CONNECTED state maps to Connected=true.
	up := planEv(reg, 2, "acme", stateChange("acme", "d1", "p@1", base, "CONNECTED", 200))
	if len(up.Events) != 1 || !up.Events[0].Presence.Connected {
		t.Fatalf("a CONNECTED StateChange must carry Connected=true: %+v", up.Events)
	}
}

// TestPlanStateChangeDoesNotFeedMeasurementRules proves a StateChange only reaches Connectivity
// rules — a threshold/measurement rule in the same profile sees nothing from a presence edge.
func TestPlanStateChangeDoesNotFeedMeasurementRules(t *testing.T) {
	base := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	thr := rules.Rule{
		ID:   ComposeRuleID("acme", "hot"),
		Name: "hot",
		Type: rules.TypeThreshold,
		When: rules.Condition{Metric: "temperature", Op: rules.OpGt, Threshold: fptr(80)},
	}
	reg := NewRuleRegistry([]ScopedRule{compileScoped(t, "acme", "p@1", thr)})

	res := planEv(reg, 1, "acme", stateChange("acme", "d1", "p@1", base, "DISCONNECTED", 100))
	if len(res.Events) != 0 {
		t.Fatalf("a StateChange must not feed a measurement rule, got %d events", len(res.Events))
	}
}

// TestPlanMeasurementDoesNotFeedConnectivity proves the converse: a measurement event does not
// build a (presence-less, no-op) event for a Connectivity rule — the fan-out skips it.
func TestPlanMeasurementDoesNotFeedConnectivity(t *testing.T) {
	base := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	reg := NewRuleRegistry([]ScopedRule{compileScoped(t, "acme", "p@1", connRule())})

	res := planEv(reg, 1, "acme", measured("acme", "d1", "p@1", base, map[string]string{"temperature": "90"}))
	if len(res.Events) != 0 {
		t.Fatalf("a measurement must not feed a connectivity rule, got %d events: %+v", len(res.Events), res.Events)
	}
}

// TestBuildInputsStateChangeStaysNil locks the heartbeat invariant across the S3b refactor: a
// StateChange must NEVER become a heartbeat Input (which would reset an absence/dead-man timer,
// masking the very disconnect it reports). Connectivity is fed via Plan's typed edge, not here.
func TestBuildInputsStateChangeStaysNil(t *testing.T) {
	base := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	ev := stateChange("acme", "d1", "p@1", base, "DISCONNECTED", 100)
	if in := BuildInputs(ev, base); in != nil {
		t.Fatalf("BuildInputs(StateChange) must be nil (no heartbeat), got %+v", in)
	}
}

// TestPlanConnectivityGroupScopeDescopes proves a group-scoped connectivity rule the device is
// OUT of scope for yields a descope (which resolves any raised offline alarm), not an edge — so a
// device leaving a "these devices" group clears its offline alarm rather than stranding it.
func TestPlanConnectivityGroupScopeDescopes(t *testing.T) {
	base := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	r := connRule()
	sr := compileScoped(t, "acme", "p@1", r)
	sr.GroupToken = "fleet"
	sr.GroupVersion = 1
	reg := NewRuleRegistry([]ScopedRule{sr})

	// A StateChange whose device is NOT a member of fleet@1: out of scope → descope, no edge.
	ev := stateChange("acme", "d1", "p@1", base, "DISCONNECTED", 100) // no ScopeMemberships
	res := planEv(reg, 1, "acme", ev)
	if len(res.Events) != 0 {
		t.Fatalf("an out-of-scope connectivity rule must feed no edge, got %+v", res.Events)
	}
	if len(res.Descopes) != 1 || res.Descopes[0].Series != "d1" || res.Descopes[0].RuleID != r.ID {
		t.Fatalf("expected one descope for the out-of-scope device, got %+v", res.Descopes)
	}

	// In scope (member of fleet@1): the edge is fed.
	ev.ScopeMemberships = []dmmodel.GroupRef{{GroupToken: "fleet", Version: 1}}
	inScope := planEv(reg, 2, "acme", ev)
	if len(inScope.Events) != 1 || inScope.Events[0].Presence == nil {
		t.Fatalf("an in-scope connectivity rule must feed the edge, got %+v", inScope.Events)
	}
}
