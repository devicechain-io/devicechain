// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-device-management/config"
	dmodel "github.com/devicechain-io/dc-device-management/model"
	dmtest "github.com/devicechain-io/dc-device-management/test"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
)

// These tests cover the geofence stamp (ADR-078): the tenant's active fence-set version
// denormalized onto resolved LOCATION events, which is what makes containment
// replay-correct without a time-travel lookup.
//
// The seam on the other side of the mock — that a fence write actually moves what
// ProfileScopeByDeviceType returns — is pinned by
// model.TestProfileScopeCarriesCurrentFenceSetVersion. Without that test these would be
// asserting the resolver faithfully copies a number nothing produces.

// stampTestApi builds a mock API adequate for HandleStandardEvent on any event type,
// reporting the given fence-set version through the profile scope.
func stampTestApi(t *testing.T, fenceSetVersion int32) *dmtest.MockApi {
	t.Helper()
	api := new(dmtest.MockApi)
	api.Mock.On("MetricDefinitionsByDeviceType").Return([]*dmodel.MetricDefinition{}, nil)
	api.Mock.On("EntityRelationships").Return(
		&dmodel.EntityRelationshipSearchResults{Results: []dmodel.EntityRelationship{}}, nil)
	api.ProfileScopeResult = &dmodel.ProfileScope{
		DeviceTypeToken:     "excavator",
		ProfileVersionToken: "earthmoving@4",
		FenceSetVersion:     fenceSetVersion,
	}
	return api
}

// locationEvent is a position report — the one event type a fence can be entered with.
func locationEvent() *esmodel.UnresolvedEvent {
	lat, lon := "33.7490", "-84.3880"
	return &esmodel.UnresolvedEvent{
		Device:    "TEST-123",
		EventType: esmodel.Location,
		Payload: &esmodel.UnresolvedLocationsPayload{
			Entries: []esmodel.UnresolvedLocationEntry{{Latitude: &lat, Longitude: &lon}},
		},
	}
}

// resolveOne runs one event through the standard-event path and returns the resolved event.
func resolveOne(t *testing.T, api *dmtest.MockApi, event *esmodel.UnresolvedEvent) *dmodel.ResolvedEvent {
	t.Helper()
	rez := NewEventResolver(1, api, config.AuthModeOptional, EventTimePolicy{}, nil, nil, nil, nil, nil, nil)
	device := deviceWithToken("TEST-123")
	device.DeviceTypeId = 77
	results, reason, err := rez.HandleStandardEvent(context.Background(), device, event)
	if err != nil {
		t.Fatalf("resolve %s: %v", event.EventType.String(), err)
	}
	if reason != 0 {
		t.Fatalf("resolve %s: failure reason %d", event.EventType.String(), reason)
	}
	if len(results) != 1 {
		t.Fatalf("resolve %s: %d results, want 1", event.EventType.String(), len(results))
	}
	return results[0].Resolved
}

// A resolved LOCATION event carries the tenant's CURRENT fence-set version.
func TestResolvedLocationEventCarriesFenceSetVersion(t *testing.T) {
	resolved := resolveOne(t, stampTestApi(t, 17), locationEvent())
	if resolved.FenceSetVersion != 17 {
		t.Errorf("fence set version = %d, want 17", resolved.FenceSetVersion)
	}
	// The stamp does not displace the scope stamp it sits beside.
	if resolved.ProfileVersionToken != "earthmoving@4" {
		t.Errorf("profile version token = %q, want earthmoving@4", resolved.ProfileVersionToken)
	}
}

// 🔴 THE DETERMINISM PAIR, AND BOTH HALVES ARE REQUIRED. After a fence changes, a
// subsequently resolved event carries the NEW version — and the event resolved before
// the change still carries the OLD one. The second half is what says the version is
// FROZEN INTO the event rather than read live at consumption time; without it, an
// implementation that looked the version up on every read would pass the first half and
// break replay entirely.
func TestFenceEditChangesLaterStampsAndLeavesEarlierOnesAlone(t *testing.T) {
	api := stampTestApi(t, 5)

	before := resolveOne(t, api, locationEvent())
	if before.FenceSetVersion != 5 {
		t.Fatalf("first event's fence set version = %d, want 5", before.FenceSetVersion)
	}

	// A fence is edited: the tenant mints version 6, and the resolve path's scope lookup
	// now answers with it (model.TestProfileScopeCarriesCurrentFenceSetVersion pins that
	// this is what a real fence write does).
	api.ProfileScopeResult = &dmodel.ProfileScope{
		DeviceTypeToken:     "excavator",
		ProfileVersionToken: "earthmoving@4",
		FenceSetVersion:     6,
	}

	after := resolveOne(t, api, locationEvent())
	if after.FenceSetVersion != 6 {
		t.Errorf("the event resolved after the fence edit carries version %d, want 6", after.FenceSetVersion)
	}
	if before.FenceSetVersion != 5 {
		t.Errorf("the earlier event's stamp moved to %d; a resolved event's fence set must be "+
			"frozen at resolve time, not re-read later", before.FenceSetVersion)
	}
}

// 🔴 THE STAMP DOES NOT SPREAD. Measurement, alert and state-change events carry no
// fence-set version — nothing but a position can enter a fence.
//
// Each of them is anchored against a LOCATION event resolved from the SAME api in the
// same test, which carries a non-zero version. That anchor is the whole point: asserting
// "measurement carries 0" on its own passes just as happily when nothing was stamped
// anywhere, including when the feature has been deleted.
func TestNonLocationEventsCarryNoFenceSetVersion(t *testing.T) {
	api := stampTestApi(t, 23)

	anchor := resolveOne(t, api, locationEvent())
	if anchor.FenceSetVersion != 23 {
		t.Fatalf("the anchor location event carries %d, want 23 — the assertions below "+
			"would be vacuous", anchor.FenceSetVersion)
	}

	measurement := &esmodel.UnresolvedEvent{
		Device:    "TEST-123",
		EventType: esmodel.Measurement,
		Payload: &esmodel.UnresolvedMeasurementsPayload{
			Entries: []esmodel.UnresolvedMeasurementsEntry{
				{Measurements: map[string]string{"temperature": "42"}},
			},
		},
	}
	alert := &esmodel.UnresolvedEvent{
		Device:    "TEST-123",
		EventType: esmodel.Alert,
		Payload: &esmodel.UnresolvedAlertsPayload{
			Entries: []esmodel.UnresolvedAlertEntry{{Type: "overheat", Level: 1, Message: "hot"}},
		},
	}
	stateChange := &esmodel.UnresolvedEvent{
		Device:    "TEST-123",
		EventType: esmodel.StateChange,
		Payload: &esmodel.UnresolvedStateChangePayload{
			State: esmodel.PresenceConnected, SessionId: "12345",
		},
	}

	for _, event := range []*esmodel.UnresolvedEvent{measurement, alert, stateChange} {
		resolved := resolveOne(t, api, event)
		if resolved.FenceSetVersion != 0 {
			t.Errorf("%s event carries fence set version %d; only location events may",
				event.EventType.String(), resolved.FenceSetVersion)
		}
		// The sibling scope stamps DO reach these events, so a zero above is the fence
		// stamp being withheld and not the whole scope failing to resolve.
		if resolved.ProfileVersionToken != "earthmoving@4" {
			t.Errorf("%s event lost its profile version token (%q); the fence assertion above "+
				"would then hold for the wrong reason",
				event.EventType.String(), resolved.ProfileVersionToken)
		}
	}
}
