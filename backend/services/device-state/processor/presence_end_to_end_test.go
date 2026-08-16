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
	"github.com/devicechain-io/dc-event-sources/adapter"
	esproto "github.com/devicechain-io/dc-event-sources/proto"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// captureWriter stands in for the durable stream, keeping the encoded message so the
// test can carry it onward by hand. It is the ONLY fake in this test, and it fakes
// transport rather than translation — every mapping the field has to survive is the
// real one.
type captureWriter struct{ msgs []messaging.Message }

func (w *captureWriter) WriteMessages(_ context.Context, msgs ...messaging.Message) error {
	w.msgs = append(w.msgs, msgs...)
	return nil
}

// newProjection builds the real device-state write path over in-memory sqlite.
func newProjection(t *testing.T) *model.Api {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("failed to register tenant scoping: %v", err)
	}
	if err := db.AutoMigrate(&model.DeviceState{}); err != nil {
		t.Fatalf("failed to migrate: %v", err)
	}
	return model.NewApi(&rdb.RdbManager{Database: db})
}

// TestARegressedSessionSurvivesTheWholeChain drives one compare-and-set repair from the
// producer's struct all the way into the projection's row, through every real mapping in
// between.
//
// 🔴🔴 THIS TEST EXISTS BECAUSE NO OTHER TEST IN ANY PACKAGE CROSSES THESE SEAMS. The
// repair's entitlement to apply travels through SIX hand-written field-by-field mappings
// — the emitter, the event-sources proto marshal and unmarshal, the resolver, the
// device-management proto marshal and unmarshal, and the projection's transition mapper.
// Every one of them is a place where a newly added field is simply not copied. Drop it at
// ANY of them and nothing fails: each module still compiles, each module's own tests still
// pass, the repair is still emitted and still consumed — it just silently loses the one
// field that entitles it to apply, and the device stays wedged exactly as it was before
// the fix. That is a green suite over a feature that does nothing, which this arc has
// already shipped once.
//
// The scenario is the real one: a device reconnects onto a broker node whose clock trails
// its peers, so its genuinely-current session id is LOWER than the projection's stored id
// and every ordinary rule refuses it.
func TestARegressedSessionSurvivesTheWholeChain(t *testing.T) {
	const (
		tenant = "acme"
		device = "sensor-001"
	)
	storedSession := uint64(1786552664076882575)
	liveSession := storedSession - uint64(90*time.Second) // a node ~90s behind its peers

	diedAt := time.Date(2026, 8, 12, 18, 0, 0, 0, time.UTC)
	repairAt := diedAt.Add(time.Minute)
	ctx := core.WithTenant(context.Background(), tenant)

	// --- the projection's starting point: offline, filed under the HIGHER session ---
	projection := newProjection(t)
	if _, err := projection.MergeDeviceState(ctx, device, diedAt, &model.PresenceTransition{
		Connected: false, SessionId: storedSession, OccurredAt: diedAt,
	}, model.DeviceIdentity{Source: mqttTestSource}); err != nil {
		t.Fatalf("seeding the dead session failed: %v", err)
	}

	// --- 1. the emitter: producer struct -> unresolved event on the wire ---
	writer := &captureWriter{}
	emitter := adapter.NewEmitter(writer, func() time.Time { return repairAt }, "mq", false)
	if err := emitter.EmitPresence(ctx, tenant, mqttTestSource, device, adapter.PresenceEvent{
		Connected:         true,
		Reason:            "reconcile-connected",
		SessionId:         liveSession,
		ExpectedSessionId: storedSession,
		OccurredAt:        repairAt,
		DedupNonce:        repairAt.Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatalf("emit failed: %v", err)
	}
	if len(writer.msgs) != 1 {
		t.Fatalf("emitted %d messages, want 1", len(writer.msgs))
	}

	// --- 2. event-sources proto round trip ---
	unresolved, err := esproto.UnmarshalUnresolvedEvent(writer.msgs[0].Value)
	if err != nil {
		t.Fatalf("unresolved decode failed: %v", err)
	}

	// --- 3. the resolver: string session ids parsed and range-checked ---
	resolvedPayload, err := (&dmprocessor.EventResolver{}).
		ResolveStateChangeEventPayload(ctx, nil, nil, unresolved)
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}

	// --- 4. device-management proto round trip ---
	encoded, err := dmproto.MarshalResolvedEvent(&dmmodel.ResolvedEvent{
		Source:            mqttTestSource,
		SourceDeviceToken: device,
		EventType:         unresolved.EventType,
		OccurredTime:      repairAt,
		ProcessedTime:     repairAt,
		Payload:           resolvedPayload,
	})
	if err != nil {
		t.Fatalf("resolved encode failed: %v", err)
	}
	resolved, err := dmproto.UnmarshalResolvedEvent(encoded)
	if err != nil {
		t.Fatalf("resolved decode failed: %v", err)
	}

	// --- 5. the projection's transition mapper (the real one, not a restatement) ---
	pt := presenceTransitionFor(resolved)
	if pt == nil {
		t.Fatal("the resolved event produced no presence transition")
	}
	if pt.ExpectedSessionId != storedSession {
		t.Fatalf("the compare-and-set was lost somewhere in the chain: expected session = %d, want %d",
			pt.ExpectedSessionId, storedSession)
	}

	// --- 6. the projection write ---
	state, err := projection.MergeDeviceState(ctx, device, repairAt, pt, model.DeviceIdentity{Source: mqttTestSource})
	if err != nil {
		t.Fatalf("merge failed: %v", err)
	}
	if !state.Active {
		t.Fatal("the device is still offline: the repair reached the projection and was refused")
	}
	if state.SessionId != liveSession {
		t.Fatalf("the row is filed under session %d, want the LIVE %d — a repair that leaves the "+
			"device on a dead session is the defect this replaces, not the fix",
			state.SessionId, liveSession)
	}

	// 🔑 THE COUNTERWEIGHT. Everything above passes on an implementation that simply
	// accepts any lower session, which would be a far worse bug than the one being fixed.
	// The same chain, with the compare-and-set removed and nothing else changed, must be
	// REFUSED.
	naked := newProjection(t)
	if _, err := naked.MergeDeviceState(ctx, device, diedAt, &model.PresenceTransition{
		Connected: false, SessionId: storedSession, OccurredAt: diedAt,
	}, model.DeviceIdentity{Source: mqttTestSource}); err != nil {
		t.Fatalf("seeding the control failed: %v", err)
	}
	control := *pt
	control.ExpectedSessionId = 0
	after, err := naked.MergeDeviceState(ctx, device, repairAt, &control, model.DeviceIdentity{Source: mqttTestSource})
	if err != nil {
		t.Fatalf("control merge failed: %v", err)
	}
	if after.Active {
		t.Fatal("a regressed session was accepted WITHOUT a compare-and-set — the guard is open to " +
			"every stale transition, and this test cannot tell the two implementations apart")
	}
}

// TestTheChainRejectsAnUnstorableExpectedSession pins that the compare-and-set field is
// range-checked the SAME way the session id is.
//
// 🔑 The two are compared against each other, so a bound that differs between them can
// only fail as a repair that never matches — silence, not an error. That is why they
// share one parse rather than having two that happen to agree today.
func TestTheChainRejectsAnUnstorableExpectedSession(t *testing.T) {
	ctx := core.WithTenant(context.Background(), "acme")
	writer := &captureWriter{}
	at := time.Date(2026, 8, 12, 18, 0, 0, 0, time.UTC)
	emitter := adapter.NewEmitter(writer, func() time.Time { return at }, "mq", false)

	// Above MaxInt64: unstorable in the signed bigint both sinks use.
	if err := emitter.EmitPresence(ctx, "acme", mqttTestSource, "sensor-001", adapter.PresenceEvent{
		Connected:         true,
		SessionId:         42,
		ExpectedSessionId: 1 << 63,
		OccurredAt:        at,
	}); err != nil {
		t.Fatalf("emit failed: %v", err)
	}
	unresolved, err := esproto.UnmarshalUnresolvedEvent(writer.msgs[0].Value)
	if err != nil {
		t.Fatalf("unresolved decode failed: %v", err)
	}
	if _, err := (&dmprocessor.EventResolver{}).
		ResolveStateChangeEventPayload(ctx, nil, nil, unresolved); err == nil {
		t.Fatal("an unstorable expected session id resolved cleanly; it would fail at the database " +
			"instead, where the error is not classified deterministic and the message burns every " +
			"redelivery before dead-lettering")
	}
}
