// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
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

// measuredMsgAt is measuredMsg with the occurred time given rather than derived from the
// sequence — a store-and-forward upload is exactly a message whose sequence is current and
// whose event time is not, so the two have to be settable independently.
func measuredMsgAt(t *testing.T, seq uint64, occurred time.Time, tenant, device, profileVersion, metric, value string, ack *fakeAck) messaging.Message {
	t.Helper()
	ev := &dmmodel.ResolvedEvent{
		Source:              "http1",
		SourceDeviceToken:   device,
		ProfileVersionToken: profileVersion,
		OccurredTime:        occurred,
		ProcessedTime:       occurred,
		EventType:           esmodel.Measurement,
		Payload: &dmmodel.ResolvedMeasurementsPayload{Entries: []dmmodel.ResolvedMeasurementsEntry{{
			OccurredTime: occurred,
			Entries:      []dmmodel.ResolvedMeasurementEntry{{Name: metric, Value: value}},
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

// repeatingReg wires a registry with one Repeating rule: 3 readings above 80 inside 10s.
func repeatingReg(t *testing.T) *runtime.RuleRegistry {
	t.Helper()
	thr := 80.0
	cr, err := rules0.Compile(rules0.Rule{
		ID:     "acme/burst",
		Name:   "burst",
		Type:   rules0.TypeRepeating,
		When:   rules0.Condition{Metric: "temperature", Op: rules0.OpGt, Threshold: &thr},
		Count:  3,
		Window: rules0.Duration(10 * time.Second),
	}, rules0.Limits{})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return runtime.NewRuleRegistry([]runtime.ScopedRule{{Tenant: "acme", ProfileVersionToken: "p@1", Compiled: cr}})
}

// The processor must DRAIN the engine's late-sample count on every message. The counter is the
// only thing a store-and-forward fleet has to look at when its windowed rules stop firing, so a
// missing drain does not merely lose a number — it reports a steady zero, which reads as "no
// samples lost" and is worse than publishing nothing at all.
//
// Asserting the engine holds nothing afterwards, rather than reading the Prometheus counter, is
// deliberate: rp.metrics is nil in these tests (recordLateSamples is nil-safe), so the residual on
// the engine is the observable that actually distinguishes a wired drain from an absent one.
func TestProcessorDrainsTheEngineLateSampleCount(t *testing.T) {
	ctx := context.Background()
	reg := repeatingReg(t)
	rp := &ResolvedEventsProcessor{
		Store: newTestStore(t),
		cfg: Config{
			PartitionId:        "singleton",
			CheckpointEvents:   100,
			CheckpointInterval: time.Hour,
			TickInterval:       time.Hour,
			Clock:              detectcore.RealClock{},
		},
		registry:  reg,
		publisher: runtime.NewPublisher(&captureWriter{}, reg, (*detectMetrics)(nil)),
		clock:     detectcore.RealClock{},
		procCtx:   ctx,
	}
	if err := rp.restore(ctx); err != nil {
		t.Fatalf("restore: %v", err)
	}

	live := testBase.Add(100 * time.Second)
	rp.handle(measuredMsgAt(t, 1, live, "acme", "d1", "p@1", "temperature", "90", &fakeAck{}))
	rp.handle(measuredMsgAt(t, 2, live.Add(5*time.Second), "acme", "d1", "p@1", "temperature", "90", &fakeAck{}))
	if n := rp.engine.DrainLateSamples(); n != 0 {
		t.Fatalf("in-order traffic left %d undrained late samples; either the drain is missing or the clamp is counting live samples", n)
	}

	// The buffered upload: current sequence, event time 65s behind the frontier.
	rp.handle(measuredMsgAt(t, 3, testBase.Add(40*time.Second), "acme", "d1", "p@1", "temperature", "90", &fakeAck{}))
	if n := rp.engine.DrainLateSamples(); n != 0 {
		t.Errorf("the processor left %d late samples undrained — detect_late_samples_total would read a permanent zero", n)
	}
}
