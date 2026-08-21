// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"testing"
	"time"

	dmmodel "github.com/devicechain-io/dc-device-management/model"
	dmprocessor "github.com/devicechain-io/dc-device-management/processor"
	dmproto "github.com/devicechain-io/dc-device-management/proto"
	"github.com/devicechain-io/dc-device-state/model"
	esproto "github.com/devicechain-io/dc-event-sources/proto"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/presence"
)

// demotionCaptureWriter is captureWriter plus the rest of messaging.MessageWriter, which
// the demotion emitter takes because it publishes to the real stream rather than through
// event-sources' narrower EventWriter seam. WriteToDevice panics rather than recording: a
// demotion is tenant-shaped, and a per-device subject would put it outside the stream.
type demotionCaptureWriter struct{ captureWriter }

func (w *demotionCaptureWriter) WriteToDevice(context.Context, string, ...messaging.Message) error {
	panic("a demotion is tenant-shaped and must never take the per-device subject")
}
func (w *demotionCaptureWriter) HandleResponse(error) {}

// TestTheOperatorDemotionSurvivesTheWholeChain drives one operator demotion from the
// mutation's emitter all the way into the projection's row, through every real mapping in
// between.
//
// 🔴🔴 NOTHING ELSE CROSSES THESE SEAMS FOR A DEMOTION, and the mutation's entitlement to
// apply travels through six hand-written field-by-field mappings: the emitter, the
// event-sources proto marshal and unmarshal, the resolver, the device-management proto
// marshal and unmarshal, and the projection's transition mapper. Drop SessionId at ANY of
// them and nothing fails: every module still compiles, every module's own tests still
// pass, the event is still published and still consumed — it just carries session 0, the
// resolver refuses it as a demotion that names no session, and every row stays exactly as
// wedged as before. That is a green suite over a repair that repairs nothing.
//
// The scenario is the one the mutation exists for: a source's presence tap has stopped,
// so its devices are frozen ASSERTED and neither the inactivity sweep nor a data event can
// move them.
func TestTheOperatorDemotionSurvivesTheWholeChain(t *testing.T) {
	const (
		tenant = "acme"
		device = "sensor-001"
	)
	session := uint64(1786552664076882575)
	connectedAt := time.Date(2026, 8, 19, 18, 0, 0, 0, time.UTC)
	demotedAt := connectedAt.Add(48 * time.Hour)
	ctx := core.WithTenant(context.Background(), tenant)

	// --- the projection's starting point: ASSERTED and frozen ONLINE ---
	projection := newProjection(t)
	if _, err := projection.MergeDeviceState(ctx, device, connectedAt, &model.PresenceTransition{
		Claim: presence.ClaimConnected, SessionId: session, OccurredAt: connectedAt,
	}, model.DeviceIdentity{Source: mqttTestSource}); err != nil {
		t.Fatalf("seeding the asserted row failed: %v", err)
	}

	// --- 1. the mutation's emitter: projection row -> unresolved event on the wire ---
	writer := &demotionCaptureWriter{}
	projection.SetDemotionEmitter(model.NewDemotionEmitter(writer, func() time.Time { return demotedAt }))
	result, err := projection.DemoteAssertedPresence(ctx, mqttTestSource, nil, 0, 10, "ops", "tap disabled")
	if err != nil {
		t.Fatalf("demote failed: %v", err)
	}
	if result.Demoted != 1 {
		t.Fatalf("demoted %d rows, want 1", result.Demoted)
	}
	if len(writer.msgs) != 1 {
		t.Fatalf("emitted %d messages, want 1", len(writer.msgs))
	}

	// --- 2. event-sources proto round trip ---
	unresolved, err := esproto.UnmarshalUnresolvedEvent(writer.msgs[0].Value)
	if err != nil {
		t.Fatalf("unresolved decode failed: %v", err)
	}

	// --- 3. the resolver, which refuses a session-less or CAS-bearing demotion ---
	resolvedPayload, err := (&dmprocessor.EventResolver{}).
		ResolveStateChangeEventPayload(ctx, nil, nil, unresolved)
	if err != nil {
		t.Fatalf("the resolver refused the demotion this mutation emits: %v", err)
	}

	// --- 4. device-management proto round trip ---
	encoded, err := dmproto.MarshalResolvedEvent(&dmmodel.ResolvedEvent{
		Source:            mqttTestSource,
		SourceDeviceToken: device,
		EventType:         unresolved.EventType,
		OccurredTime:      demotedAt,
		ProcessedTime:     demotedAt,
		Payload:           resolvedPayload,
	})
	if err != nil {
		t.Fatalf("resolved encode failed: %v", err)
	}
	resolved, err := dmproto.UnmarshalResolvedEvent(encoded)
	if err != nil {
		t.Fatalf("resolved decode failed: %v", err)
	}

	// --- 5. the projection's transition mapper (the real one) ---
	pt, err := presenceTransitionFor(resolved)
	if err != nil {
		t.Fatalf("the resolved event could not be mapped to a presence transition: %v", err)
	}
	if pt == nil {
		t.Fatal("the resolved event produced no presence transition; a demotion folded as a plain " +
			"data event would advance LastActivityTime on the very row it came to release")
	}
	if pt.Claim != presence.ClaimDemoted {
		t.Fatalf("the claim arrived as %v, want ClaimDemoted — read as a connectivity value the "+
			"projection applies a DISCONNECT and DETECT raises an offline alarm for every demoted device",
			pt.Claim)
	}
	if pt.SessionId != session {
		t.Fatalf("the released session was lost somewhere in the chain: got %d, want %d — a demotion "+
			"naming any other session fails the compare-and-set and the row stays ASSERTED",
			pt.SessionId, session)
	}

	// --- 6. the projection write ---
	after, err := projection.MergeDeviceState(ctx, device, demotedAt, pt,
		model.DeviceIdentity{Source: mqttTestSource})
	if err != nil {
		t.Fatalf("merge failed: %v", err)
	}
	if after.PresenceSource != model.PresenceSourceInferred {
		t.Fatalf("the row is still %s: the demotion reached the projection and was refused",
			after.PresenceSource)
	}
	if !after.Active {
		t.Fatal("the demotion flipped Active to false: a custody release asserts NOTHING about " +
			"connectivity, and this is an offline alarm for every device in the fleet")
	}
	if after.SessionId != session {
		t.Errorf("SessionId = %d, want the released session %d to stay named — it is what a "+
			"re-assertion has to beat", after.SessionId, session)
	}

	// 🔑 THE COUNTERWEIGHT. Everything above passes on an implementation that accepts any
	// demotion at all, which would let a stale echo release custody the source still holds.
	// The same chain with a session the row does not hold must be REFUSED.
	naked := newProjection(t)
	if _, err := naked.MergeDeviceState(ctx, device, connectedAt, &model.PresenceTransition{
		Claim: presence.ClaimConnected, SessionId: session, OccurredAt: connectedAt,
	}, model.DeviceIdentity{Source: mqttTestSource}); err != nil {
		t.Fatalf("seeding the control failed: %v", err)
	}
	control := *pt
	control.SessionId = session + 1
	control.OccurredAt = demotedAt
	unmoved, err := naked.MergeDeviceState(ctx, device, demotedAt, &control,
		model.DeviceIdentity{Source: mqttTestSource})
	if err != nil {
		t.Fatalf("control merge failed: %v", err)
	}
	if unmoved.PresenceSource != model.PresenceSourceAsserted {
		t.Fatal("a demotion naming a session the row does not hold was APPLIED — the " +
			"compare-and-set is open, and this test cannot tell the two implementations apart")
	}
}
