// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"testing"

	"github.com/devicechain-io/dc-command-delivery/model"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/deadletter"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/prometheus/client_golang/prometheus"
)

// testDeadSink is the sink the literal-built processors in this package's tests write
// through: a real producer's sink, so the letters they capture carry the source the
// service stamps, over a Microservice literal whose counters are built unregistered.
func testDeadSink(w deadletter.Writer) *deadletter.Sink {
	return deadletter.NewProducer(&core.Microservice{FunctionalArea: "command-delivery"}).NewSink(w)
}

// failingDeadWriter refuses every write, so a letter through it is lost.
type failingDeadWriter struct{ calls int }

func (w *failingDeadWriter) WriteMessages(context.Context, ...messaging.Message) error {
	w.calls++
	return errors.New("the broker is away")
}

// gathered reads a plain counter off reg by its full EXPORTED name. Reading a handle would
// pass whatever the name composed to, and the name is what the alert selects on.
func gathered(t *testing.T, reg *prometheus.Registry, name string) float64 {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gathering the registry: %v", err)
	}
	for _, f := range families {
		if f.GetName() == name {
			return f.GetMetric()[0].GetCounter().GetValue()
		}
	}
	t.Fatalf("the registry exports no %s", name)
	return 0
}

const (
	lostSeries     = "devicechain_commanddelivery_dead_letter_lost_total"
	letteredSeries = "devicechain_commanddelivery_command_delivery_responses_dead_lettered_total"
)

// constructorBuiltProcessor builds the processor the way main.go does: through
// NewCommandDeliveryProcessor, over a Microservice with a registry, with the sink from a
// producer built on that Microservice.
func constructorBuiltProcessor(t *testing.T, dead deadletter.Writer, api *fakeApi,
	numDelivered int) (*CommandDeliveryProcessor, *prometheus.Registry) {
	t.Helper()
	ms := &core.Microservice{InstanceId: "test", FunctionalArea: "command-delivery"}
	reg := prometheus.NewRegistry()
	ms.UseMetricsRegistry(reg)
	metrics := NewDeliveryMetrics(ms)
	producer := deadletter.NewProducer(ms)

	p := NewCommandDeliveryProcessor(ms, &oneMessageReader{
		msg: responseMessage(
			[]byte(`{"commandToken":"cmd-1","success":true}`), numDelivered, nil, nil),
	}, nil, core.NewNoOpLifecycleCallbacks(), api, nil, nil, producer.NewSink(dead), metrics)
	return p, reg
}

// 🔴 THE PROCESSOR MAIN.GO BUILDS WRITES LETTERS THAT NAME THIS SERVICE.
//
// Every other test in this package builds the processor by struct literal, which is how a
// constructor that had lost its copy of the functional area went unseen: the literals set
// the area themselves, so every one of them wrote a valid letter while the constructed
// processor wrote letters with no source — each refused by Validate, counted as a broker
// loss, and gone. This goes through the constructor and a producer, the way main.go does,
// and reads the counters by the names the alerts select on.
func TestTheConstructedProcessorDeadLettersWithItsSource(t *testing.T) {
	dead := &deadRecorder{}
	p, reg := constructorBuiltProcessor(t, dead,
		&fakeApi{responseErr: model.ErrResponseMissingNonce}, 1)

	readAndHandleOne(p, context.Background())

	if len(dead.msgs) != 1 {
		t.Fatalf("the writer received %d dead letters, want 1 (lost = %v)",
			len(dead.msgs), gathered(t, reg, lostSeries))
	}
	e, err := deadletter.Unmarshal(dead.msgs[0].Value)
	if err != nil {
		t.Fatalf("the written letter does not read back: %v", err)
	}
	if e.Source != "command-delivery" {
		t.Fatalf("letter source = %q, want %q", e.Source, "command-delivery")
	}
	if e.Kind != deadletter.KindCommandResponse || e.Reason != deadletter.ReasonUnprocessable {
		t.Fatalf("letter kind/reason = %q/%q", e.Kind, e.Reason)
	}
	if got := gathered(t, reg, lostSeries); got != 0 {
		t.Fatalf("%s = %v, want 0: the letter was written", lostSeries, got)
	}
	if got := gathered(t, reg, letteredSeries); got != 1 {
		t.Fatalf("%s = %v, want 1", letteredSeries, got)
	}
}

// And at the call site of the loss hook: a letter the constructed processor cannot write
// moves the PROCESS's dead_letter_lost_total — the series the DeadLetterWriteLost alert
// reads — by exactly one, and is not counted as dead-lettered.
func TestTheConstructedProcessorCountsALostLetterOnTheProcessCounter(t *testing.T) {
	broken := &failingDeadWriter{}
	p, reg := constructorBuiltProcessor(t, broken,
		&fakeApi{responseErr: model.ErrResponseMissingNonce}, 1)

	readAndHandleOne(p, context.Background())

	if broken.calls == 0 {
		t.Fatal("premise lost: the letter never reached the writer")
	}
	if got := gathered(t, reg, lostSeries); got != 1 {
		t.Fatalf("%s = %v after one lost letter, want 1", lostSeries, got)
	}
	if got := gathered(t, reg, letteredSeries); got != 0 {
		t.Fatalf("%s = %v, want 0: nothing was written", letteredSeries, got)
	}
}

// 🔑 THE LETTERS NOW REACH THE WRITE-BACK, AND IT ACTS ON THEM. Until the source was
// stamped, no command-response letter was ever valid, so this service's own write-back
// never received one in production. Feed it the exact bytes the constructed processor
// wrote: an EXHAUSTED response settles its command as lost, and an UNPROCESSABLE one — a
// declined answer, not a failed attempt — settles nothing.
func TestTheWritebackActsOnTheLettersTheConstructedProcessorWrites(t *testing.T) {
	for _, tc := range []struct {
		name      string
		api       *fakeApi
		delivered int
		reason    deadletter.Reason
		settles   bool
	}{
		{"exhausted", &fakeApi{responseErr: errors.New("the database is away")},
			messaging.MaxDeliver, deadletter.ReasonExhausted, true},
		{"missing nonce", &fakeApi{responseErr: model.ErrResponseMissingNonce},
			1, deadletter.ReasonUnprocessable, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dead := &deadRecorder{}
			p, _ := constructorBuiltProcessor(t, dead, tc.api, tc.delivered)
			readAndHandleOne(p, context.Background())
			if len(dead.msgs) != 1 {
				t.Fatalf("the writer received %d dead letters, want 1", len(dead.msgs))
			}
			e, err := deadletter.Unmarshal(dead.msgs[0].Value)
			if err != nil || e.Reason != tc.reason {
				t.Fatalf("letter reason = %q (err %v), want %q", e.Reason, err, tc.reason)
			}

			api := &dispositionRecorder{settled: true}
			w := newTestWriteback(t, api)
			ack := &countingAck{}
			w.Handle(messaging.NewConsumedMessage(messaging.ScopedSubject("inst", "acme", "dead-letters"),
				dead.msgs[0].Value, 1, nil, ack))

			if tc.settles {
				if len(api.calls) != 1 || api.calls[0] != "cmd-1" {
					t.Fatalf("write-back settled %v, want [cmd-1]", api.calls)
				}
			} else if len(api.calls) != 0 {
				t.Fatalf("write-back settled %v on a declined answer, want nothing", api.calls)
			}
			if ack.acks != 1 {
				t.Fatalf("acks = %d, want 1", ack.acks)
			}
		})
	}
}
