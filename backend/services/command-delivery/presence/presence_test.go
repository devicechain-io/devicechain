// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package presence

import (
	"context"
	"errors"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
)

// sparkplugSource is the source string a Sparkplug device is ACTUALLY projected with —
// sparkplug-ingest stamps "sparkplug:"+hostId onto every event it emits, and device-state
// denormalizes that verbatim.
//
// 🔴🔴 USE THIS, NEVER A HAND-WRITTEN "sparkplug". Every case here once passed a bare
// "sparkplug", which no producer in the platform emits, and the whole Undeliverable branch
// was unreachable in production for as long as those fixtures looked green. A fixture that
// carries a PLAUSIBLE BUT UNPRODUCEABLE value tests the fixture, not the gate — it is the
// same failure as a fake that drops a field, one step further from being visible.
//
// It is spelled with a real host id rather than the "{hostId}" placeholder so it is a value
// the platform could genuinely mint, and it is deliberately NOT the default host id: a
// qualifier that happened to be empty or defaulted would let a whole-string comparison keep
// passing for the wrong reason.
const sparkplugSource = "sparkplug:plant-a"

// mqttSource is what a plain-MQTT device is ACTUALLY projected with, and it is NOT "mqtt".
// event-sources stamps the configured event source's own id (`EventSource.Id`, free text,
// shipped default "mqtt1"); the constant `processor.TYPE_MQTT = "mqtt"` is a config TYPE
// discriminator matched in a switch and is never written onto an event.
//
// 🔑 THESE CASES ARE BEHAVIOUR-NEUTRAL, AND THAT IS EXACTLY WHY THE VALUE IS FIXED HERE.
// Any source not on the deny list dispatches, so "mqtt" and "mqtt1" produce identical
// verdicts and no test would have caught the fiction — which is the same reason the bare
// "sparkplug" survived. A fixture spelling a value the platform cannot mint teaches the
// next reader that it can, and the next deny-list entry written against that belief is a
// repeat of the defect this file now guards.
const mqttSource = "mqtt1"

// TestDecide walks every combination the gate can be handed, because each wrong answer
// is a different production failure and they are not interchangeable.
func TestDecide(t *testing.T) {
	cases := []struct {
		name  string
		state State
		want  Verdict
		why   string
	}{
		{
			"unknown device dispatches",
			State{Known: false},
			Dispatch,
			"a device the projection has never seen must not be held; that is every device " +
				"before its first event, and holding them would gate the whole platform closed",
		},
		{
			"asserted and absent holds",
			State{Known: true, Asserted: true, Active: false, Source: mqttSource},
			Hold,
			"this is the case the gate exists for: an authoritative absence makes a publish " +
				"provably futile, and today it is silently discarded and marked SENT",
		},
		{
			"asserted and present dispatches",
			State{Known: true, Asserted: true, Active: true, Source: mqttSource},
			Dispatch,
			"the device is there",
		},
		{
			"INFERRED and inactive dispatches",
			State{Known: true, Asserted: false, Active: false, Source: mqttSource},
			Dispatch,
			"🔴 THE CONJUNCT THAT STOPS THIS BEING A DISASTER. An inferred row's inactivity " +
				"means only 'no events for a while' — a device reporting hourly is inactive for " +
				"59 minutes of every 60 and reachable throughout. Holding on !Active alone would " +
				"hold commands for every quiet device on every non-asserting transport",
		},
		{
			"INFERRED and active dispatches",
			State{Known: true, Asserted: false, Active: true, Source: mqttSource},
			Dispatch,
			"nothing to withhold on",
		},
		{
			"sparkplug is undeliverable, not held, even when ACTIVE",
			State{Known: true, Asserted: true, Active: true, Source: sparkplugSource},
			Undeliverable,
			"🔑 the transport has no command path at all, so presence is beside the point",
		},
		{
			"sparkplug is undeliverable, not held, when absent",
			State{Known: true, Asserted: true, Active: false, Source: sparkplugSource},
			Undeliverable,
			"HELD would mean 'waiting for the device to come back' — false here, because it " +
				"comes back and delivery still cannot happen, while the row occupies the " +
				"tenant's ceiling for a full TTL",
		},
		{
			"an UNQUALIFIED sparkplug source is still undeliverable",
			State{Known: true, Asserted: true, Active: false, Source: "sparkplug"},
			Undeliverable,
			"the transport cut must not stop matching the bare form; a source is " +
				"`transport` OR `transport:qualifier` and both name the same transport",
		},
		{
			"a source merely NAMED like sparkplug is deliverable",
			State{Known: true, Asserted: true, Active: true, Source: "sparkplugin"},
			Dispatch,
			"🔴 THE COUNTERWEIGHT TO THE TRANSPORT CUT. A prefix match would condemn this. " +
				"`source` for a plain gateway is an OPERATOR-CHOSEN id, so a name that merely " +
				"starts with a denied transport is reachable input, not a hypothetical",
		},
		{
			"an operator source named sparkplug-test is deliverable",
			State{Known: true, Asserted: true, Active: false, Source: "sparkplug-test"},
			Hold,
			"same counterweight, on the path where the alternative verdict is HOLD rather " +
				"than DISPATCH — it holds on its absence, and is never condemned as " +
				"undeliverable",
		},
		{
			"lwm2m absent holds",
			State{Known: true, Asserted: true, Active: false, Source: "lwm2m"},
			Hold,
			"lwm2m delivers over its own CoAP session and drains on the device's return",
		},
		{
			"an unrecognised source is deliverable",
			State{Known: true, Asserted: true, Active: true, Source: "some-future-transport"},
			Dispatch,
			"🔑 the deny list is not the complement of an allow list: a source nobody has " +
				"classified must fall through to dispatch, not be condemned by silence",
		},
		{
			"an unrecognised source that is absent still HOLDS rather than terminating",
			State{Known: true, Asserted: true, Active: false, Source: "some-future-transport"},
			Hold,
			"absence is established; the lack of a delivery path is not",
		},
		{
			"an empty source is deliverable",
			State{Known: true, Asserted: true, Active: true, Source: ""},
			Dispatch,
			"source is null until the device has produced an event carrying one",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Decide(c.state); got != c.want {
				t.Errorf("Decide(%+v) = %v, want %v\n%s", c.state, got, c.want, c.why)
			}
		})
	}
}

// TestOnlyPositiveEvidenceWithholds is the invariant behind every case above, asserted
// directly so it survives a future conjunct being added to Decide.
//
// 🔑 The gate may only ever WITHHOLD on positive evidence. Every unknown — no row, no
// source, an unrecognised source, an inferred row — must dispatch, because the cost of a
// wrong hold (a command that never goes out) is paid by a device that was reachable all
// along, while the cost of a wrong dispatch is the behaviour that already exists today.
func TestOnlyPositiveEvidenceWithholds(t *testing.T) {
	for _, s := range []State{
		{Known: false},
		{Known: false, Asserted: true, Active: false, Source: sparkplugSource},
		{Known: true, Asserted: false, Active: false},
		{Known: true, Asserted: false, Active: false, Source: mqttSource},
		{Known: true, Asserted: true, Active: true},
	} {
		if got := Decide(s); got != Dispatch {
			t.Errorf("Decide(%+v) = %v; nothing here is positive evidence of absence or of a "+
				"missing delivery path, so it must dispatch", s, got)
		}
	}
}

// fakeClient records the query it was given and returns a canned response.
type fakeClient struct {
	gotTenant string
	gotVars   map[string]any
	rows      []map[string]any
	err       error
}

func (f *fakeClient) Query(_ context.Context, _, tenant, _ string, vars map[string]any, out any) error {
	f.gotTenant = tenant
	f.gotVars = vars
	if f.err != nil {
		return f.err
	}
	resp, ok := out.(*struct {
		DeviceStatesByDeviceToken []struct {
			DeviceToken    string `json:"deviceToken"`
			Active         bool   `json:"active"`
			PresenceSource string `json:"presenceSource"`
			Source         string `json:"source"`
		} `json:"deviceStatesByDeviceToken"`
	})
	if !ok {
		return errors.New("unexpected out type")
	}
	for _, r := range f.rows {
		resp.DeviceStatesByDeviceToken = append(resp.DeviceStatesByDeviceToken, struct {
			DeviceToken    string `json:"deviceToken"`
			Active         bool   `json:"active"`
			PresenceSource string `json:"presenceSource"`
			Source         string `json:"source"`
		}{
			DeviceToken:    r["deviceToken"].(string),
			Active:         r["active"].(bool),
			PresenceSource: r["presenceSource"].(string),
			Source:         r["source"].(string),
		})
	}
	return nil
}

// TestStatesForMarksMissingDevicesUnknown is the read-side half of the fail-open rule.
//
// 🔴 deviceStatesByDeviceToken RETURNS ONLY ROWS THAT EXIST. A device the projection has
// never seen simply does not come back in the response — it is not returned as inactive.
// Reading that silence as absence would hold commands for every device that has not yet
// produced an event, which on a fresh instance is all of them.
func TestStatesForMarksMissingDevicesUnknown(t *testing.T) {
	client := &fakeClient{rows: []map[string]any{
		{"deviceToken": "known-1", "active": false, "presenceSource": "ASSERTED", "source": mqttSource},
	}}
	reader := NewGraphQLReader(client, "http://device-state/graphql")
	ctx := core.WithTenant(context.Background(), "acme")

	states, err := reader.StatesFor(ctx, []string{"known-1", "never-seen"})
	if err != nil {
		t.Fatalf("StatesFor failed: %v", err)
	}
	if got := states["known-1"]; !got.Known || got.Active || !got.Asserted || got.Source != mqttSource {
		t.Fatalf("known-1 decoded wrong: %+v", got)
	}
	if got, present := states["never-seen"]; present {
		t.Fatalf("a device absent from the response must not appear in the result at all, got %+v", got)
	}
	// And the gate's own reduction of that must be Dispatch, not Hold.
	if v := Decide(states["never-seen"]); v != Dispatch {
		t.Fatalf("a device the projection has never seen decided %v, want Dispatch", v)
	}

	if client.gotTenant != "acme" {
		t.Fatalf("the read must be tenant-scoped, got %q", client.gotTenant)
	}
	// 🔑 ONE round trip for the whole batch. A read per command would put a network call
	// between the platform and every dispatch.
	tokens, _ := client.gotVars["deviceTokens"].([]string)
	if len(tokens) != 2 {
		t.Fatalf("the batch must be sent as one query, got vars %+v", client.gotVars)
	}
}

// TestStatesForRequiresATenant fails closed rather than reading cross-tenant.
func TestStatesForRequiresATenant(t *testing.T) {
	reader := NewGraphQLReader(&fakeClient{}, "http://device-state/graphql")
	if _, err := reader.StatesFor(context.Background(), []string{"d"}); err == nil {
		t.Fatal("a presence read with no tenant in context must fail rather than read across tenants")
	}
}

// TestStatesForSkipsTheRoundTripWhenThereIsNothingToAsk keeps an idle sweep tick free.
func TestStatesForSkipsTheRoundTripWhenThereIsNothingToAsk(t *testing.T) {
	client := &fakeClient{err: errors.New("must not be called")}
	reader := NewGraphQLReader(client, "http://device-state/graphql")
	states, err := reader.StatesFor(core.WithTenant(context.Background(), "acme"), nil)
	if err != nil {
		t.Fatalf("an empty batch must not call out: %v", err)
	}
	if len(states) != 0 {
		t.Fatalf("expected no states, got %+v", states)
	}
}
