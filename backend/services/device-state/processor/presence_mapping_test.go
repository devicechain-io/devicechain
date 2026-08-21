// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/presence"
)

// TestAnUnmappableStateChangeIsAnErrorNotAHeartbeat is the reason this mapper returns an
// error at all. A nil transition does NOT mean "nothing to do" at the call site — it means
// "plain data event", which MergeDeviceState folds as an implicit heartbeat that advances
// LastActivityTime. So a StateChange nobody can read would arrive at the projection
// disguised as evidence of life, and keep an asserted device looking alive on the strength
// of an event the platform could not interpret. The disposition has to be loud.
func TestAnUnmappableStateChangeIsAnErrorNotAHeartbeat(t *testing.T) {
	ev := &dmmodel.ResolvedEvent{
		EventType:    esmodel.StateChange,
		OccurredTime: time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC),
		Payload:      &dmmodel.ResolvedStateChangePayload{State: "RETIRED", SessionId: 7},
	}
	pt, err := presenceTransitionFor(ev)
	if err == nil {
		t.Fatalf("an unmappable presence state produced no error (pt = %+v)", pt)
	}
	if pt != nil {
		t.Fatalf("an unmappable presence state produced a transition anyway: %+v", pt)
	}
}

// TestAStateChangePayloadOfTheWrongTypeIsAnError covers the other way the same disguise is
// reachable: the event says StateChange but carries something else. Returning nil there was
// equally a silent downgrade to a heartbeat.
func TestAStateChangePayloadOfTheWrongTypeIsAnError(t *testing.T) {
	ev := &dmmodel.ResolvedEvent{
		EventType: esmodel.StateChange,
		Payload:   &dmmodel.ResolvedMeasurementsPayload{},
	}
	if pt, err := presenceTransitionFor(ev); err == nil {
		t.Fatalf("a mistyped state-change payload produced no error (pt = %+v)", pt)
	}
}

// TestTheMapperCarriesEveryClaimAndStillPassesDataThrough is the counterweight. Making
// unmappable states loud is only safe while every state the vocabulary DOES name still maps,
// and while a genuine data event still folds as the heartbeat it is.
func TestTheMapperCarriesEveryClaimAndStillPassesDataThrough(t *testing.T) {
	t0 := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		state string
		want  presence.Claim
	}{
		{string(esmodel.PresenceConnected), presence.ClaimConnected},
		{string(esmodel.PresenceDisconnected), presence.ClaimDisconnected},
		{string(esmodel.PresenceDemoted), presence.ClaimDemoted},
	} {
		t.Run(tc.state, func(t *testing.T) {
			pt, err := presenceTransitionFor(&dmmodel.ResolvedEvent{
				EventType:    esmodel.StateChange,
				OccurredTime: t0,
				Payload:      &dmmodel.ResolvedStateChangePayload{State: tc.state, SessionId: 7},
			})
			if err != nil {
				t.Fatalf("%s did not map: %v", tc.state, err)
			}
			if pt == nil || pt.Claim != tc.want {
				t.Fatalf("%s mapped to %+v, want claim %v", tc.state, pt, tc.want)
			}
			if pt.SessionId != 7 || !pt.OccurredAt.Equal(t0) {
				t.Fatalf("%s lost its ordering key: %+v", tc.state, pt)
			}
		})
	}

	pt, err := presenceTransitionFor(&dmmodel.ResolvedEvent{
		EventType:    esmodel.Measurement,
		OccurredTime: t0,
		Payload:      &dmmodel.ResolvedMeasurementsPayload{},
	})
	if err != nil || pt != nil {
		t.Fatalf("a plain data event must map to (nil, nil), got (%+v, %v)", pt, err)
	}
}
