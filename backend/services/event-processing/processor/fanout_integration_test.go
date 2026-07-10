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
	detectcore "github.com/devicechain-io/dc-event-processing/internal/detect/core"
	rules0 "github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	esmodel "github.com/devicechain-io/dc-event-sources/model"
	"github.com/devicechain-io/dc-microservice/messaging"
)

// captureWriter records derived-event writes and can be made to fail (broker outage).
type captureWriter struct {
	writes int
	err    error
}

func (w *captureWriter) WriteMessages(ctx context.Context, msgs ...messaging.Message) error {
	if w.err != nil {
		return w.err
	}
	w.writes += len(msgs)
	return nil
}
func (w *captureWriter) HandleResponse(error) {}

// measuredMsg builds a consumed resolved measurement message for tenant/device under a
// profile version, carrying one metric reading, at stream sequence seq.
func measuredMsg(t *testing.T, seq uint64, tenant, device, profileVersion, metric, value string, ack *fakeAck) messaging.Message {
	t.Helper()
	occurred := testBase.Add(time.Duration(seq) * time.Second)
	ev := &dmmodel.ResolvedEvent{
		Source:              "http1",
		SourceDeviceToken:   device,
		ProfileVersionToken: profileVersion,
		OccurredTime:        occurred,
		ProcessedTime:       occurred,
		EventType:           esmodel.Measurement,
		Payload: &dmmodel.ResolvedMeasurementsPayload{Entries: []dmmodel.ResolvedMeasurementsEntry{{
			Entries: []dmmodel.ResolvedMeasurementEntry{{Name: metric, Value: value}},
		}}},
	}
	b, err := dmproto.MarshalResolvedEvent(ev)
	if err != nil {
		t.Fatalf("marshal resolved event: %v", err)
	}
	m := messaging.NewConsumedMessage("dc."+tenant+".resolved-events", b, 0, nil, ack)
	m.StreamSeq = seq
	return m
}

// thresholdReg wires a registry with a single threshold rule (temperature > 80) for tenant
// acme / profile p@1.
func thresholdReg(t *testing.T) *runtime.RuleRegistry {
	t.Helper()
	thr := 80.0
	cr, err := rules0.Compile(rules0.Rule{
		ID:   "acme/hot",
		Name: "hot",
		Type: rules0.TypeThreshold,
		When: rules0.Condition{Metric: "temperature", Op: rules0.OpGt, Threshold: &thr},
	}, rules0.Limits{})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return runtime.NewRuleRegistry([]runtime.ScopedRule{{Tenant: "acme", ProfileVersionToken: "p@1", Compiled: cr}})
}

// A matching resolved event fires the rule; the detection is published as a derived event
// BEFORE the checkpoint acks the message (deliver-before-checkpoint), and only then is the
// message acked and the snapshot committed.
func TestFanoutPublishesDerivedEventThenAcks(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	reg := thresholdReg(t)
	w := &captureWriter{}
	rp := &ResolvedEventsProcessor{
		Store: store,
		cfg: Config{
			PartitionId:        "singleton",
			CheckpointEvents:   100,
			CheckpointInterval: time.Hour,
			TickInterval:       time.Hour,
			Clock:              detectcore.RealClock{},
		},
		registry:  reg,
		publisher: runtime.NewPublisher(w, reg, (*detectMetrics)(nil)),
		clock:     detectcore.RealClock{},
		procCtx:   ctx,
	}
	if err := rp.restore(ctx); err != nil {
		t.Fatalf("restore: %v", err)
	}

	ack := &fakeAck{}
	rp.handle(measuredMsg(t, 1, "acme", "d1", "p@1", "temperature", "90", ack))
	// Before the checkpoint nothing is acked and nothing is published.
	if ack.acks != 0 || w.writes != 0 {
		t.Fatalf("before checkpoint: acks=%d writes=%d, want 0,0", ack.acks, w.writes)
	}

	rp.checkpoint(ctx)
	if w.writes != 1 {
		t.Fatalf("checkpoint should have published one derived event; got %d", w.writes)
	}
	if ack.acks != 1 {
		t.Fatalf("checkpoint should have acked the message; got %d", ack.acks)
	}
	if _, ok, err := store.Load(ctx, "singleton"); err != nil || !ok {
		t.Fatalf("checkpoint should have committed a snapshot; ok=%v err=%v", ok, err)
	}
}

// A publish failure defers the whole checkpoint (deliver-before-checkpoint): the message is
// not acked and no snapshot advances, so a replay re-derives and re-emits. Once the broker
// recovers, the next checkpoint publishes and acks.
func TestDeliverBeforeCheckpointDefersOnPublishError(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	reg := thresholdReg(t)
	w := &captureWriter{err: errors.New("broker down")}
	rp := &ResolvedEventsProcessor{
		Store: store,
		cfg: Config{
			PartitionId:        "singleton",
			CheckpointEvents:   100,
			CheckpointInterval: time.Hour,
			TickInterval:       time.Hour,
			Clock:              detectcore.RealClock{},
		},
		registry:  reg,
		publisher: runtime.NewPublisher(w, reg, (*detectMetrics)(nil)),
		clock:     detectcore.RealClock{},
		procCtx:   ctx,
	}
	if err := rp.restore(ctx); err != nil {
		t.Fatalf("restore: %v", err)
	}

	ack := &fakeAck{}
	rp.handle(measuredMsg(t, 1, "acme", "d1", "p@1", "temperature", "90", ack))
	rp.checkpoint(ctx) // publish fails → whole checkpoint deferred

	if ack.acks != 0 {
		t.Fatalf("a deferred checkpoint must not ack; got %d", ack.acks)
	}
	if _, ok, _ := store.Load(ctx, "singleton"); ok {
		t.Fatalf("a deferred checkpoint must not commit a snapshot")
	}

	// Broker recovers: the retained detection publishes and the checkpoint completes.
	w.err = nil
	rp.checkpoint(ctx)
	if w.writes != 1 || ack.acks != 1 {
		t.Fatalf("after recovery: writes=%d acks=%d, want 1,1", w.writes, ack.acks)
	}
}
