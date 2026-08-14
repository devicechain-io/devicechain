// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/devicechain-io/dc-device-management/config"
	"github.com/devicechain-io/dc-device-management/model"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/stretchr/testify/require"
)

// 🔴 THE OBLIGATION THIS FILE ENFORCES, FOR EVERY PAYLOAD TYPE RATHER THAN THE ONE THAT
// WAS BROKEN.
//
// event-management derives an event's identity from json.Marshal of the resolved payload
// (DeriveEventId), and that identity is what makes a redelivery idempotent. The digest
// hashes those bytes opaquely, so it can only be as stable as the producer's output —
// every resolver branch owes it a payload that encodes identically every time. The full
// statement of that contract, including the trap that makes it easy to break, lives on
// DeriveEventId itself in event-management.
//
// Measurements broke the contract by ranging a map into a slice; the fix sorts. But a
// per-branch test only ever proves the branch it names, and the natural way to reintroduce
// this bug is in a DIFFERENT branch — add a map-valued field to alerts, expand it into a
// slice the way measurements did, and every measurement-specific test stays green.
//
// So this test enumerates the event types rather than listing the ones we happen to have
// covered, and REFUSES TO PASS when a type has no fixture: a new event type cannot be added
// without either giving it one or deliberately excluding it below.

// resolutionFixture is one event type's input, plus what the branch needs to resolve it.
type resolutionFixture struct {
	// event is rebuilt for every attempt because production rebuilds it too — a redelivery
	// decodes the message from raw bytes again, so each attempt should start from its own
	// value rather than one a previous attempt has already been handed. Sharing one value
	// would make the repetitions less independent than the thing they stand in for.
	event func() *esmodel.UnresolvedEvent
	// api is the store the branch reads through, or nil when the branch needs none.
	api model.DeviceManagementApi
	// wantEntries is how many ordered elements the resolved payload must expose — the
	// instrument check. Stated as an EXACT expected count rather than a lower bound,
	// because "more than one" would let a fixture that silently dropped four of its five
	// measurements still satisfy the guard. Zero means the payload is a flat struct with
	// no ordering freedom at all, so there is nothing to count.
	wantEntries int
}

// canonicalFixtures maps each event type the resolver handles to a fixture exercising its
// payload branch. Each carries MORE THAN ONE entry wherever the type allows it, because a
// single-entry payload has only one possible encoding and would make the assertion vacuous.
func canonicalFixtures() map[esmodel.EventType]resolutionFixture {
	entryTime := fixtureTime
	return map[esmodel.EventType]resolutionFixture{
		esmodel.Measurement: {
			event: func() *esmodel.UnresolvedEvent {
				return &esmodel.UnresolvedEvent{
					Device: "TEST-123", EventType: esmodel.Measurement, OccurredTime: fixtureTime,
					Payload: &esmodel.UnresolvedMeasurementsPayload{
						Entries: []esmodel.UnresolvedMeasurementsEntry{
							{Measurements: orderFixture},
							{Measurements: map[string]string{"temperature": "21.6", "humidity": "47"}},
						},
					},
				}
			},
			api:         noMetricDefsApi(),
			wantEntries: len(orderFixture) + 2,
		},
		esmodel.Location: {
			event: func() *esmodel.UnresolvedEvent {
				lat, lon := "33.749", "-84.388"
				lat2, lon2 := "47.6062", "-122.3321"
				return &esmodel.UnresolvedEvent{
					Device: "TEST-123", EventType: esmodel.Location, OccurredTime: fixtureTime,
					Payload: &esmodel.UnresolvedLocationsPayload{
						Entries: []esmodel.UnresolvedLocationEntry{
							{Latitude: &lat, Longitude: &lon, OccurredTime: &entryTime},
							{Latitude: &lat2, Longitude: &lon2},
						},
					},
				}
			},
			wantEntries: 2,
		},
		esmodel.Alert: {
			event: func() *esmodel.UnresolvedEvent {
				return &esmodel.UnresolvedEvent{
					Device: "TEST-123", EventType: esmodel.Alert, OccurredTime: fixtureTime,
					Payload: &esmodel.UnresolvedAlertsPayload{
						Entries: []esmodel.UnresolvedAlertEntry{
							{Type: "overheat", Level: 3, Message: "too hot", Source: "sensor"},
							{Type: "undervolt", Level: 1, Message: "low", Source: "psu"},
						},
					},
				}
			},
			wantEntries: 2,
		},
		esmodel.StateChange: {
			event: func() *esmodel.UnresolvedEvent {
				return &esmodel.UnresolvedEvent{
					Device: "TEST-123", EventType: esmodel.StateChange, OccurredTime: fixtureTime,
					Payload: &esmodel.UnresolvedStateChangePayload{
						State: "CONNECTED", Reason: "birth", SessionId: "7",
					},
				}
			},
			// A flat struct: json.Marshal emits its fields in declaration order, so this
			// branch has no ordering freedom to test. The stability assertion still runs
			// and still means "this branch introduced no nondeterminism".
			wantEntries: 0,
		},
	}
}

// Event types the resolver deliberately does not handle, so this test demands no fixture.
// NewRelationship is resolved on its own path (HandleNewRelationshipEvent) rather than
// through ResolveEventPayload; the two command types are enum values HandleEvent rejects.
// Anything moving OUT of this set must gain a fixture above.
var notResolvedAsPayload = map[esmodel.EventType]string{
	esmodel.NewRelationship:   "resolved on its own path, not through ResolveEventPayload",
	esmodel.CommandInvocation: "not routed to a payload resolver",
	esmodel.CommandResponse:   "not routed to a payload resolver",
}

// declaredEventTypes walks the EventType enum itself, using the fact that stringer returns
// the out-of-range form "EventType(N)" past the last declared constant.
//
// 🔴 IT DOES NOT USE esmodel.EventTypesByName, AND THAT IS THE WHOLE POINT. That registry is
// a HAND-WRITTEN init() listing each constant by name, and its only production consumer is
// the JSON decoder — so an event type added for a proto-decoded or internally minted path
// (the way presence mints StateChange) compiles and ships without ever touching it. Deriving
// this test's universe from the registry would mean a type that skipped the registry also
// skipped the test, which is precisely the silent gap this test exists to close. The
// stringer table is generated from the const block and carries a compile-time guard against
// the two drifting apart, so it is the more honest source.
func declaredEventTypes(t *testing.T) []esmodel.EventType {
	t.Helper()
	var out []esmodel.EventType
	for i := 0; i < 64; i++ {
		typ := esmodel.EventType(i)
		if typ.String() == fmt.Sprintf("EventType(%d)", i) {
			return out
		}
		out = append(out, typ)
	}
	t.Fatal("the EventType enum did not terminate within 64 values; stringer output changed shape")
	return nil
}

// Every event type the resolver turns into a payload must be enumerated — either with a
// fixture or with a written reason for having none. This is what makes the file generalise:
// a new event type added without a fixture FAILS rather than being silently uncovered,
// which is exactly how the measurements gap survived.
func TestEveryEventTypeIsAccountedFor(t *testing.T) {
	declared := declaredEventTypes(t)
	require.NotEmpty(t, declared,
		"no event types were enumerated, so this test would pass having checked nothing")

	fixtures := canonicalFixtures()
	for _, typ := range declared {
		_, covered := fixtures[typ]
		_, excused := notResolvedAsPayload[typ]
		require.True(t, covered || excused,
			"event type %s has neither a canonicalisation fixture nor a documented exclusion; "+
				"its payload encoding is unproven and a redelivery of it may not dedup", typ)
		require.False(t, covered && excused,
			"event type %s is both covered and excused — one of the two is wrong", typ)
	}

	// The registry is not this test's universe, but a type missing from it is its own bug
	// (the JSON decoder would refuse that event type by name), so report it from here
	// rather than leaving the discrepancy for someone to find in production.
	require.Len(t, esmodel.EventTypesByName, len(declared),
		"EventTypesByName lists %d of the %d declared event types; the hand-written init() "+
			"has drifted from the const block", len(esmodel.EventTypesByName), len(declared))
}

// Resolving one message repeatedly must produce byte-identical payload bytes, for EVERY
// payload type. Those bytes are the preimage event-management hashes into the event id, and
// DeriveEventId is a pure function of them — so byte-equal preimage means equal id, and this
// asserts the half that can vary.
//
// A redelivery is a designed-for path on two independent lanes: device-management leaves its
// source unacked when a resolved publish fails, and event-management's own consumer
// redelivers on a transient failure. "The same message resolved twice" is ordinary.
func TestPayloadEncodingIsStableAcrossResolutions(t *testing.T) {
	const attempts = 100

	for typ, fixture := range canonicalFixtures() {
		t.Run(typ.String(), func(t *testing.T) {
			encodings := map[string]struct{}{}

			for i := 0; i < attempts; i++ {
				rez := NewEventResolver(1, fixture.api, config.AuthModeOptional, EventTimePolicy{},
					nil, nil, nil, nil, nil, nil)

				out, err := rez.ResolveEventPayload(context.Background(),
					deviceWithToken("TEST-123"), nil, fixture.event())
				require.NoError(t, err)

				raw, err := json.Marshal(out)
				require.NoError(t, err)
				encodings[string(raw)] = struct{}{}

				// The instrument check, on every attempt rather than the last: a branch
				// that dropped its entries would encode identically every time and report
				// perfect stability. An exact count also catches a PARTIAL drop, which a
				// "more than one" bound would wave through.
				if fixture.wantEntries > 0 {
					require.Equal(t, fixture.wantEntries, countEntries(t, out),
						"the %s fixture resolved to the wrong number of entries, so the "+
							"stability assertion below would be measuring the wrong payload", typ)
				}
			}

			require.Len(t, encodings, 1,
				"%s resolved to %d distinct payload encodings across %d resolutions of ONE message; "+
					"each is a different event id, so a redelivery double-persists the event",
				typ, len(encodings), attempts)
		})
	}
}

// 🔑 THE INPUT'S OWN VARIANCE, which the stability assertion above cannot prove for itself.
// Everything above compares repeated resolutions of the same message; if the fixture's map
// happened to iterate identically every time, every comparison would pass while measuring
// nothing. This proves the measurement fixture really does yield more than one raw order —
// so a resolver that did not sort would be caught rather than accidentally agreed with.
func TestTheMeasurementFixtureActuallyVariesItsOrder(t *testing.T) {
	const attempts = 200

	orders := map[string]struct{}{}
	for i := 0; i < attempts; i++ {
		order := ""
		for name := range orderFixture {
			order += name + "|"
		}
		orders[order] = struct{}{}
	}

	require.Greater(t, len(orders), 1,
		"map iteration over the shared measurement fixture did not vary across %d ranges, so "+
			"the stability tests cannot detect an unsorted resolver", attempts)
}

// countEntries reports how many ordered elements a resolved payload exposes. The
// type-switch-with-fatal-default is the point: a new payload type cannot be added to the
// fixtures without deciding, here, what counting it means.
func countEntries(t *testing.T, payload any) int {
	t.Helper()
	switch p := payload.(type) {
	case *model.ResolvedMeasurementsPayload:
		// The INNER slice is the one built from a map, so it is what must be counted.
		total := 0
		for _, e := range p.Entries {
			total += len(e.Entries)
		}
		return total
	case *model.ResolvedLocationsPayload:
		return len(p.Entries)
	case *model.ResolvedAlertsPayload:
		return len(p.Entries)
	case *model.ResolvedStateChangePayload:
		// Flat: no repeated element. Its fixture sets wantEntries to 0, so this is never
		// compared against anything — returning a made-up count here would be a number
		// invented to satisfy a guard.
		return 0
	default:
		t.Fatalf("unhandled resolved payload type %T; add it to countEntries so its "+
			"stability check cannot pass vacuously", payload)
		return 0
	}
}
