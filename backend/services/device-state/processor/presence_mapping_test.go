// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	dmproto "github.com/devicechain-io/dc-device-management/proto"
	"github.com/devicechain-io/dc-device-state/model"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/presence"
	"gorm.io/gorm"
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

// TestAnUnmappableStateChangeNeverReachesTheProjection drives the real message handler,
// which is where the mapper's error either does its job or does nothing at all.
//
// 🔑 TESTING THE MAPPER IS NOT TESTING THE DISPOSITION. presenceTransitionFor returning an
// error is worth exactly what its caller does with it, and the caller's failure mode is
// silent: on error the transition is nil, and a nil transition is not "nothing to do" — it
// is "plain data event", which the projection folds as an implicit heartbeat. So a caller
// that ignored the error would CREATE an active row for a device on the strength of an
// event nobody could read, and every assertion about the mapper would still pass.
func TestAnUnmappableStateChangeNeverReachesTheProjection(t *testing.T) {
	sp := newLocationProcessor(t)
	ctx := core.WithTenant(context.Background(), "tenant1")
	t0 := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)

	sp.mergeOne(context.Background(), stateChangeMessage(t, "ghost-01", t0, "RETIRED", 100))

	api := sp.Api.(*model.Api)
	var ds model.DeviceState
	err := api.RDB.DB(ctx).Where("device_token = ?", "ghost-01").First(&ds).Error
	if err == nil {
		t.Fatalf("an unreadable presence claim created a projection row: %+v", ds)
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("reading the projection: %v", err)
	}

	// The counterweight: a state the vocabulary DOES name reaches the projection through
	// the same path, so the assertion above is not satisfied by a handler that drops
	// every state change.
	sp.mergeOne(context.Background(), stateChangeMessage(t, "real-01", t0, "CONNECTED", 100))
	real := loadState(t, sp, ctx, "real-01")
	if real.PresenceSource != model.PresenceSourceAsserted || !real.Active {
		t.Fatalf("a mappable state change did not reach the projection: %+v", real)
	}
}

// stateChangeMessage marshals a resolved state-change onto the wire exactly as
// device-management publishes it, so the handler unmarshals real bytes.
func stateChangeMessage(t *testing.T, deviceToken string, occurredAt time.Time,
	state string, session uint64) messaging.Message {
	t.Helper()
	event := &dmmodel.ResolvedEvent{
		Source:            mqttTestSource,
		SourceDeviceToken: deviceToken,
		EventType:         esmodel.StateChange,
		OccurredTime:      occurredAt,
		ProcessedTime:     occurredAt.Add(250 * time.Millisecond),
		Payload:           &dmmodel.ResolvedStateChangePayload{State: state, SessionId: session},
	}
	encoded, err := dmproto.MarshalResolvedEvent(event)
	if err != nil {
		t.Fatalf("marshal resolved event: %v", err)
	}
	return messaging.Message{Subject: locationTestSubject, Value: encoded}
}
