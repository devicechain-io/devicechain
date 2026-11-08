// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package evaluate

import (
	"context"
	"io"
	"math/rand/v2"
	"reflect"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/preview"
	"github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
)

// The replay preview and an evaluation are two doors onto the same detection core, and the claim a
// test of a rule stands on is that they agree: the same readings, delivered in the same order,
// derive the same edges. This replays one fixture through preview.Run, over messages marshalled
// exactly as the resolved-events stream stores them, and compares edge for edge.

type replayReader struct {
	msgs []messaging.Message
	i    int
}

func (r *replayReader) Read(context.Context) (messaging.Message, error) {
	if r.i >= len(r.msgs) {
		return messaging.Message{}, io.EOF
	}
	m := r.msgs[r.i]
	r.i++
	return m, nil
}
func (r *replayReader) Close() error { return nil }

type replayOpener struct{ msgs []messaging.Message }

func (o replayOpener) NewReplayReaderFromTime(string, time.Time) (messaging.ReplayReader, time.Time, error) {
	return &replayReader{msgs: o.msgs}, time.Time{}, nil
}

func stored(t *testing.T, seq uint64, ev Event) messaging.Message {
	t.Helper()
	b, err := resolvedevent.MarshalResolvedEvent(resolve("p@1", ev))
	if err != nil {
		t.Fatal(err)
	}
	m := messaging.NewConsumedMessage("dc.acme.resolved-events", b, 0, nil, nil)
	m.StreamSeq = seq
	m.AppendTime = ev.At
	return m
}

func previewEdges(t *testing.T, rs []Rule, evs []Event, end time.Time) []preview.Firing {
	t.Helper()
	var scoped []runtime.ScopedRule
	for _, r := range rs {
		def := r.Def
		def.ID = runtime.PublishedRuleID("acme", "p@1", r.Token)
		c, err := rules.Compile(def, rules.DefaultLimits())
		if err != nil {
			t.Fatal(err)
		}
		scoped = append(scoped, runtime.ScopedRule{Tenant: "acme", ProfileVersionToken: "p@1", Compiled: c})
	}
	var msgs []messaging.Message
	for i, e := range evs {
		msgs = append(msgs, stored(t, uint64(i+1), e))
	}
	res, err := preview.Run(context.Background(), replayOpener{msgs}, "resolved-events", runtime.NewRuleRegistry(scoped),
		"acme", "p@1", preview.TimeRange{Start: t0.Add(-time.Hour), End: end}, 0, 0, 0, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return res.Firings
}

func TestEvaluationAgreesWithPreviewReplay(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 11))
	var evs []Event
	temp := 60.0
	for i := 0; i < 600; i++ {
		temp += (rng.Float64() - 0.5) * 12
		evs = append(evs, reading([]string{"a", "b", "c", "d"}[rng.IntN(4)], t0.Add(time.Duration(i)*time.Minute), "temperature", temp))
	}
	end := t0.Add(12 * time.Hour)
	// Rules with one series each (a shared token would be ambiguous in the preview's firing order).
	for _, rs := range [][]Rule{
		{hotRule()},
		{absenceRule(20 * time.Minute)},
	} {
		p := params(rs, evs)
		p.AdvanceTo = end
		got := mustRun(t, p)
		want := previewEdges(t, rs, evs, end)
		if len(want) == 0 {
			t.Fatalf("%s: the fixture derived nothing in the preview, so it proves nothing", rs[0].Token)
		}
		type edge struct {
			At     time.Time
			Series string
			Raise  bool
		}
		var a, b []edge
		for _, e := range got.Edges {
			a = append(a, edge{e.At, e.Series, e.Raise})
		}
		for _, f := range want {
			b = append(b, edge{f.OccurredAt, f.Series, f.Raise})
		}
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("%s: evaluation and preview disagree:\nevaluate: %v\npreview:  %v", rs[0].Token, a, b)
		}
	}
}
