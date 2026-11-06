// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"io"
	"sort"
	"testing"
	"time"

	detectcore "github.com/devicechain-io/dc-event-processing/internal/detect/core"
	"github.com/devicechain-io/dc-event-processing/internal/preview"
	rules0 "github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
)

// previewOpener replays a fixed slice of messages to the preview.
type previewOpener struct{ msgs []messaging.Message }

type previewReader struct {
	msgs []messaging.Message
	i    int
}

func (r *previewReader) Read(context.Context) (messaging.Message, error) {
	if r.i >= len(r.msgs) {
		return messaging.Message{}, io.EOF
	}
	m := r.msgs[r.i]
	r.i++
	return m, nil
}
func (r *previewReader) Close() error { return nil }

func (o *previewOpener) NewReplayReaderFromTime(string, time.Time) (messaging.ReplayReader, time.Time, error) {
	return &previewReader{msgs: o.msgs}, time.Time{}, nil
}

// countRegistry is a rule whose answer depends on HOW MANY events arrive: more than one temperature
// sample inside a sliding minute. A duplicate of one event is the difference between firing and not.
func countRegistry(t *testing.T) *runtime.RuleRegistry {
	t.Helper()
	one := 1.0
	cr, err := rules0.Compile(rules0.Rule{ID: "acme/burst", Name: "burst", Type: rules0.TypeAggregate,
		Agg: rules0.AggCount, Mode: rules0.ModeSliding,
		Window: rules0.Duration(time.Minute), Op: rules0.OpGt, Threshold: &one}, rules0.Limits{})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return runtime.NewRuleRegistry([]runtime.ScopedRule{{Tenant: "acme", ProfileVersionToken: "p@1", Compiled: cr}})
}

type edge struct {
	series string
	raise  bool
}

func sortedEdges(e []edge) []edge {
	sort.Slice(e, func(i, j int) bool {
		if e[i].series != e[j].series {
			return e[i].series < e[j].series
		}
		return !e[i].raise && e[j].raise
	})
	return e
}

// The replay preview and the live engine must answer the same question about the same stream, in
// both envelope modes. A retransmitted legacy duplicate (same device, instant and payload under two
// sequences) is counted by live when envelopes are off (no duplicate guard is built, the v1.1
// default) and dropped when they are on; the preview used to drop it always.
func TestPreviewAgreesWithLiveOnDuplicatesInBothEnvelopeModes(t *testing.T) {
	t0 := testBase
	events := []diffEvent{
		{device: "d1", value: "20.0", occurred: t0},
		{device: "d1", value: "20.0", occurred: t0}, // the retransmitted copy of the line above
		{device: "d2", value: "21.0", occurred: t0.Add(5 * time.Second)},
		{device: "d3", value: "22.0", occurred: t0.Add(10 * time.Second)},
		{device: "d3", value: "23.0", occurred: t0.Add(11 * time.Second)}, // a genuine second sample
	}
	msgs := make([]messaging.Message, len(events))
	for i, e := range events {
		b, err := resolvedevent.MarshalResolvedEvent(diffResolved(t, e))
		if err != nil {
			t.Fatal(err)
		}
		m := messaging.NewConsumedMessage("dc.acme.resolved-events", b, 0, nil, &fakeAck{})
		m.StreamSeq = uint64(10 + i)
		m.AppendTime = e.occurred
		msgs[i] = m
	}

	for _, tc := range []struct {
		name      string
		envelopes bool
		want      []edge
	}{
		{"envelopes off keeps duplicates", false, []edge{{"d1", true}, {"d3", true}}},
		{"envelopes on drops duplicates", true, []edge{{"d3", true}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Live.
			w := &derivedCapture{}
			reg := countRegistry(t)
			rp := &ResolvedEventsProcessor{
				Store: newTestStore(t),
				cfg: Config{PartitionId: "singleton", CheckpointEvents: 1000, CheckpointInterval: time.Hour,
					TickInterval: time.Hour, Clock: detectcore.RealClock{}, Output: serialOutput,
					AppliedGuard: tc.envelopes},
				registry: reg, clock: detectcore.RealClock{}, procCtx: context.Background(),
				Replay: &fakeReplayOpener{},
			}
			rp.publisher = runtime.NewPublisher(w, reg, (*DetectMetrics)(nil))
			if err := rp.restore(context.Background()); err != nil {
				t.Fatal(err)
			}
			for _, m := range msgs {
				rp.handle(m)
			}
			rp.checkpoint(context.Background())
			var live []edge
			for _, d := range w.events {
				live = append(live, edge{d.Series, d.Edge != "resolved"})
			}

			// Preview, told the instance's envelope mode.
			res, err := preview.Run(context.Background(), &previewOpener{msgs: msgs}, "resolved-events", countRegistry(t),
				"acme", "p@1", preview.TimeRange{Start: t0.Add(-time.Hour), End: t0.Add(time.Hour)}, 0, 0, 0, nil, nil,
				previewOptions(tc.envelopes)...)
			if err != nil {
				t.Fatal(err)
			}
			var prev []edge
			for _, f := range res.Firings {
				prev = append(prev, edge{f.Series, f.Raise})
			}

			live, prev = sortedEdges(live), sortedEdges(prev)
			if len(live) != len(tc.want) {
				t.Fatalf("live fired %v, want %v (the fixture is wrong)", live, tc.want)
			}
			if len(prev) != len(live) {
				t.Fatalf("preview fired %v, live fired %v", prev, live)
			}
			for i := range live {
				if live[i] != prev[i] {
					t.Fatalf("preview fired %v, live fired %v", prev, live)
				}
			}
		})
	}
}

// previewOptions is how the test tells the preview the instance's envelope mode.
func previewOptions(envelopes bool) []preview.Option {
	return []preview.Option{preview.WithAppliedGuard(envelopes)}
}
