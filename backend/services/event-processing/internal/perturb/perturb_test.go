// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package perturb

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strconv"
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/preview"
	rules0 "github.com/devicechain-io/dc-event-processing/internal/rules"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
)

var base = time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)

type fakeReader struct {
	msgs []messaging.Message
	idx  int
}

func (r *fakeReader) Read(context.Context) (messaging.Message, error) {
	if r.idx >= len(r.msgs) {
		return messaging.Message{}, io.EOF
	}
	m := r.msgs[r.idx]
	r.idx++
	return m, nil
}
func (r *fakeReader) Close() error { return nil }

type fakeOpener struct{ msgs []messaging.Message }

func (o *fakeOpener) NewReplayReaderFromTime(string, time.Time) (messaging.ReplayReader, time.Time, error) {
	return &fakeReader{msgs: o.msgs}, time.Time{}, nil
}

func measurement(t *testing.T, seq uint64, tenant, device, metric string, value float64, at time.Time) messaging.Message {
	t.Helper()
	ev := &resolvedevent.ResolvedEvent{
		Source: "http1", SourceDeviceToken: device, ProfileVersionToken: "p@1",
		OccurredTime: at, ProcessedTime: at, EventType: eventtype.Measurement,
		Payload: &resolvedevent.ResolvedMeasurementsPayload{Entries: []resolvedevent.ResolvedMeasurementsEntry{{
			OccurredTime: at,
			Entries:      []resolvedevent.ResolvedMeasurementEntry{{Name: metric, Value: strconv.FormatFloat(value, 'g', -1, 64)}},
		}}},
	}
	b, err := resolvedevent.MarshalResolvedEvent(ev)
	if err != nil {
		t.Fatal(err)
	}
	m := messaging.NewConsumedMessage("dc."+tenant+".resolved-events", b, 0, nil, nil)
	m.StreamSeq = seq
	return m
}

func hotRule(t *testing.T) *runtime.RuleRegistry {
	t.Helper()
	thr := 80.0
	cr, err := rules0.Compile(rules0.Rule{
		ID: "acme/p@1/hot", Name: "hot", Type: rules0.TypeThreshold, Severity: rules0.SeverityCritical,
		When: rules0.Condition{Metric: "temperature", Op: rules0.OpGt, Threshold: &thr},
	}, rules0.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	return runtime.NewRuleRegistry([]runtime.ScopedRule{{Tenant: "acme", ProfileVersionToken: "p@1", Compiled: cr}})
}

func win() preview.TimeRange {
	return preview.TimeRange{Start: base.Add(-time.Hour), End: base.Add(2 * time.Hour)}
}

// drain reads a wrapped opener to the end and returns what the detector would be handed.
func drain(t *testing.T, src Source, tenant string, specs []Spec) []messaging.Message {
	t.Helper()
	o, err := Wrap(src, tenant, specs)
	if err != nil {
		t.Fatal(err)
	}
	r, _, err := o.NewReplayReaderFromTime("resolved-events", base)
	if err != nil {
		t.Fatal(err)
	}
	var out []messaging.Message
	for {
		m, err := r.Read(context.Background())
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
}

func run(t *testing.T, src Source, specs []Spec) preview.Result {
	t.Helper()
	o, err := Wrap(src, "acme", specs)
	if err != nil {
		t.Fatal(err)
	}
	res, err := preview.Run(context.Background(), o, "resolved-events", hotRule(t), "acme", "p@1", win(), 0, 0, 0, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// A plant: three chillers each running a few degrees under the alarm line, one genuinely hot.
func plant(t *testing.T) *fakeOpener {
	var msgs []messaging.Message
	seq := uint64(0)
	for step := 0; step < 6; step++ {
		at := base.Add(time.Duration(step) * 10 * time.Minute)
		for i, dev := range []string{"chiller-1", "chiller-2", "chiller-3"} {
			seq++
			v := 74.0 + float64(i)*2 // 74, 76, 78
			if dev == "chiller-3" && step == 4 {
				v = 81 // reality: one genuine excursion
			}
			msgs = append(msgs, measurement(t, seq, "acme", dev, "temperature", v, at))
		}
	}
	return &fakeOpener{msgs: msgs}
}

func TestIdentityPassesBytesThroughUntouched(t *testing.T) {
	src := plant(t)
	got := drain(t, src, "acme", nil)
	if len(got) != len(src.msgs) {
		t.Fatalf("identity changed the message count: %d -> %d", len(src.msgs), len(got))
	}
	for i := range got {
		if !bytes.Equal(got[i].Value, src.msgs[i].Value) {
			t.Fatalf("message %d bytes changed under the identity perturbation", i)
		}
	}
}

func TestOperatorsAreDeterministic(t *testing.T) {
	specs := []Spec{
		{Op: OpMetricOffset, Metric: "temperature", Offset: 5},
		{Op: OpMetricFreeze, Device: "chiller-2", Metric: "temperature", From: base.Add(20 * time.Minute)},
		{Op: OpBlackout, Device: "chiller-1", From: base.Add(10 * time.Minute), To: base.Add(30 * time.Minute)},
	}
	a, b := drain(t, plant(t), "acme", specs), drain(t, plant(t), "acme", specs)
	if len(a) != len(b) {
		t.Fatalf("two runs differ in length: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if !bytes.Equal(a[i].Value, b[i].Value) || a[i].StreamSeq != b[i].StreamSeq {
			t.Fatalf("message %d differs between two identical runs", i)
		}
	}
}

func TestHeatwaveOffsetAddsAlarms(t *testing.T) {
	src := plant(t)
	base0 := run(t, src, nil)
	fork := run(t, src, []Spec{{Op: OpMetricOffset, Metric: "temperature", Offset: 5}})
	d := Diff(base0.Firings, fork.Firings, 0)
	added := map[string]bool{}
	for _, x := range d {
		if x.Kind == Added && x.Raise {
			added[x.Series] = true
		}
	}
	// +5: chiller-2 (76 -> 81) and chiller-3 (78 -> 83) cross 80 for the whole run; chiller-1 (74 -> 79) never does.
	if !added["chiller-2"] || added["chiller-1"] {
		t.Fatalf("heatwave should add chiller-2 and not chiller-1, got %+v", d)
	}
	// chiller-3's genuine excursion in reality becomes a permanent alarm: its raise moves earlier.
	var shifted bool
	for _, x := range d {
		if x.Series == "chiller-3" && x.Raise && (x.Kind == Shifted || x.Kind == Added) {
			shifted = true
		}
	}
	if !shifted {
		t.Fatalf("chiller-3's alarm should diverge, got %+v", d)
	}
}

func TestScaleAndOffsetCompose(t *testing.T) {
	two := 2.0
	got := drain(t, &fakeOpener{msgs: []messaging.Message{measurement(t, 1, "acme", "d", "temperature", 10, base)}},
		"acme", []Spec{{Op: OpMetricOffset, Metric: "temperature", Scale: &two, Offset: 1}})
	ev, err := resolvedevent.UnmarshalResolvedEvent(got[0].Value)
	if err != nil {
		t.Fatal(err)
	}
	v := ev.Payload.(*resolvedevent.ResolvedMeasurementsPayload).Entries[0].Entries[0].Value
	if v != "21" {
		t.Fatalf("10*2+1 = %q, want 21", v)
	}
}

func TestDropDeviceFromFailureRemovesTheRaise(t *testing.T) {
	src := plant(t)
	// chiller-3's real excursion is at base+40m; it "failed" at base+30m, so it never reports it.
	fork := run(t, src, []Spec{{Op: OpDropDevice, Device: "chiller-3", From: base.Add(30 * time.Minute)}})
	base0 := run(t, src, nil)
	d := Diff(base0.Firings, fork.Firings, 0)
	if len(base0.Firings) == 0 {
		t.Fatal("reality should hold chiller-3's excursion")
	}
	var removed bool
	for _, x := range d {
		if x.Kind == Removed && x.Series == "chiller-3" && x.Raise {
			removed = true
		}
	}
	if !removed {
		t.Fatalf("dropping the failed device should remove its raise, got %+v", d)
	}
}

func TestBlackoutRemovesOnlyTheWindow(t *testing.T) {
	got := drain(t, plant(t), "acme", []Spec{{Op: OpBlackout, Device: "chiller-1",
		From: base.Add(10 * time.Minute), To: base.Add(30 * time.Minute)}})
	if want := 18 - 2; len(got) != want { // samples at +10m and +20m are dark; the end is exclusive
		t.Fatalf("blackout kept %d messages, want %d", len(got), want)
	}
}

func TestBlackoutNeedsBothEnds(t *testing.T) {
	if _, err := Wrap(&fakeOpener{}, "acme", []Spec{{Op: OpBlackout, From: base}}); err == nil {
		t.Fatal("an open-ended blackout must be refused")
	}
}

func TestFreezeHoldsTheLastValueSeenBeforeFrom(t *testing.T) {
	var msgs []messaging.Message
	for i, v := range []float64{70, 72, 90, 95} {
		msgs = append(msgs, measurement(t, uint64(i+1), "acme", "d", "temperature", v, base.Add(time.Duration(i)*time.Minute)))
	}
	got := drain(t, &fakeOpener{msgs: msgs}, "acme",
		[]Spec{{Op: OpMetricFreeze, Device: "d", Metric: "temperature", From: base.Add(2 * time.Minute)}})
	var vals []string
	for _, m := range got {
		ev, _ := resolvedevent.UnmarshalResolvedEvent(m.Value)
		vals = append(vals, ev.Payload.(*resolvedevent.ResolvedMeasurementsPayload).Entries[0].Entries[0].Value)
	}
	if fmt.Sprint(vals) != "[70 72 72 72]" {
		t.Fatalf("values = %v, want [70 72 72 72]", vals)
	}
}

func TestAnotherTenantsEventsAreNeverTouched(t *testing.T) {
	foreign := measurement(t, 1, "other", "d", "temperature", 10, base)
	got := drain(t, &fakeOpener{msgs: []messaging.Message{foreign}}, "acme",
		[]Spec{{Op: OpMetricOffset, Metric: "temperature", Offset: 100}})
	if len(got) != 1 || !bytes.Equal(got[0].Value, foreign.Value) {
		t.Fatal("another tenant's event was altered or dropped")
	}
}

func TestValidateRefusesNonsense(t *testing.T) {
	for name, s := range map[string]Spec{
		"unknown op":       {Op: "explode"},
		"drop no device":   {Op: OpDropDevice},
		"offset no metric": {Op: OpMetricOffset, Offset: 1},
		"backwards window": {Op: OpDropDevice, Device: "d", From: base, To: base.Add(-time.Second)},
	} {
		if s.Validate() == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	if _, err := Wrap(&fakeOpener{}, "", nil); err == nil {
		t.Error("a fork with no tenant must be refused")
	}
	if _, err := Wrap(&fakeOpener{}, "acme", make([]Spec, MaxSpecs+1)); err == nil {
		t.Error("too many perturbations must be refused")
	}
}

func fire(series string, raise bool, at time.Time) preview.Firing {
	return preview.Firing{OccurredAt: at, Series: series, Raise: raise}
}

func TestDiffClassifiesAddedRemovedShifted(t *testing.T) {
	t0 := base
	b := []preview.Firing{fire("a", true, t0), fire("b", true, t0), fire("c", true, t0)}
	f := []preview.Firing{fire("a", true, t0), fire("b", true, t0.Add(2*time.Minute)), fire("d", true, t0.Add(time.Minute))}
	d := Diff(b, f, 0)
	kinds := map[string]string{}
	for _, x := range d {
		kinds[x.Series] = x.Kind
	}
	if _, same := kinds["a"]; same {
		t.Error("an identical firing must not diverge")
	}
	if kinds["b"] != Shifted || kinds["c"] != Removed || kinds["d"] != Added {
		t.Fatalf("kinds = %v", kinds)
	}
	for _, x := range d {
		if x.Series == "b" && x.Delta != 2*time.Minute {
			t.Errorf("delta = %v, want 2m", x.Delta)
		}
	}
}

func TestDiffTooFarApartIsRemovedPlusAdded(t *testing.T) {
	d := Diff([]preview.Firing{fire("a", true, base)}, []preview.Firing{fire("a", true, base.Add(time.Hour))}, time.Minute)
	if len(d) != 2 || d[0].Kind != Removed || d[1].Kind != Added {
		t.Fatalf("got %+v", d)
	}
}

func TestDiffIsOrderIndependentAndEmptyWhenEqual(t *testing.T) {
	a := []preview.Firing{fire("x", true, base), fire("y", false, base.Add(time.Minute))}
	b := []preview.Firing{a[1], a[0]}
	if d := Diff(a, b, 0); len(d) != 0 {
		t.Fatalf("equal timelines diverged: %+v", d)
	}
}

// An offset ahead of a freeze must not leak into what the freeze holds: each operator reads the
// recording, not another operator's output.
func TestFreezeHoldsTheRecordedValueNotAnotherOperatorsOutput(t *testing.T) {
	var msgs []messaging.Message
	for i, v := range []float64{70, 72, 90} {
		msgs = append(msgs, measurement(t, uint64(i+1), "acme", "d", "temperature", v, base.Add(time.Duration(i)*time.Minute)))
	}
	got := drain(t, &fakeOpener{msgs: msgs}, "acme", []Spec{
		{Op: OpMetricOffset, Metric: "temperature", Offset: 10, To: base.Add(2 * time.Minute)},
		{Op: OpMetricFreeze, Device: "d", Metric: "temperature", From: base.Add(2 * time.Minute)},
	})
	var vals []string
	for _, m := range got {
		ev, _ := resolvedevent.UnmarshalResolvedEvent(m.Value)
		vals = append(vals, ev.Payload.(*resolvedevent.ResolvedMeasurementsPayload).Entries[0].Entries[0].Value)
	}
	if fmt.Sprint(vals) != "[80 82 72]" {
		t.Fatalf("values = %v, want [80 82 72] (offset applied early, freeze holds the recorded 72)", vals)
	}
}

// Two freezes that start together keep their own held values.
func TestTwoFreezesWithTheSameStartAreIndependent(t *testing.T) {
	var msgs []messaging.Message
	for i, v := range []float64{70, 90} {
		msgs = append(msgs, measurement(t, uint64(i+1), "acme", "d", "temperature", v, base.Add(time.Duration(i)*time.Minute)))
	}
	five, nine := 5.0, 9.0
	got := drain(t, &fakeOpener{msgs: msgs}, "acme", []Spec{
		{Op: OpMetricFreeze, Device: "d", Metric: "temperature", Value: &five, From: base.Add(time.Minute)},
		{Op: OpMetricFreeze, Device: "d", Metric: "temperature", Value: &nine, From: base.Add(time.Minute)},
	})
	ev, _ := resolvedevent.UnmarshalResolvedEvent(got[1].Value)
	if v := ev.Payload.(*resolvedevent.ResolvedMeasurementsPayload).Entries[0].Entries[0].Value; v != "9" {
		t.Fatalf("second freeze should win with its own value, got %s", v)
	}
}
