// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rules

import (
	"testing"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/detect/core"
	"github.com/devicechain-io/dc-event-processing/internal/detect/predicate"
)

func at(sec int) time.Time   { return time.Unix(int64(sec), 0).UTC() }
func ptr(v float64) *float64 { return &v }

// testLimits use a generous cost ceiling so the functional tests exercise firing, not the
// gate (the gate is proven in predicate_test and the rejection tests below).
var testLimits = Limits{PredicateCostCeiling: 1_000_000, DefaultCorrelationMemberCap: 1024}

// driver compiles a rule and drives synthetic events through the REAL keyed-streaming core
// via BuildEvent, collecting detections — the proof that the compiler's (CEL + core-config)
// output fires correctly end to end.
type driver struct {
	t   *testing.T
	cr  *CompiledRule
	e   *core.Engine
	seq uint64
	got []core.Detection
}

func newDriver(t *testing.T, r Rule) *driver {
	t.Helper()
	cr, err := Compile(r, testLimits)
	if err != nil {
		t.Fatalf("compile %s: %v", r.Type, err)
	}
	return &driver{t: t, cr: cr, e: core.NewEngine([]core.Rule{cr.Core}, 0)}
}

// send feeds one event: series is the key token (device, or anchor token for correlation),
// member the contributing device.
func (d *driver) send(sec int, series, member string, m map[string]float64) {
	d.t.Helper()
	d.seq++
	in := predicate.Input{Device: member, Occurred: at(sec), M: m, Anchors: map[string]string{"site": series}}
	ev, err := d.cr.BuildEvent(d.seq, series, member, in)
	if err != nil {
		d.t.Fatalf("build event: %v", err)
	}
	d.e.ProcessEvent(ev)
	d.got = append(d.got, d.e.Drain()...)
}

func (d *driver) advance(sec int) {
	d.e.Advance(at(sec))
	d.got = append(d.got, d.e.Drain()...)
}

func (d *driver) assertFires(wantKind core.RuleKind, wantAt time.Time) {
	d.t.Helper()
	if len(d.got) != 1 {
		d.t.Fatalf("want exactly one detection, got %d: %+v", len(d.got), d.got)
	}
	if d.got[0].Kind != wantKind || !d.got[0].At.Equal(wantAt) {
		d.t.Fatalf("want %v@%v, got %v@%v", wantKind, wantAt, d.got[0].Kind, d.got[0].At)
	}
}

func (d *driver) assertQuiet() {
	d.t.Helper()
	if len(d.got) != 0 {
		d.t.Fatalf("want no detections, got %+v", d.got)
	}
}

func TestThreshold(t *testing.T) {
	d := newDriver(t, Rule{ID: "t", Name: "hot", Type: TypeThreshold,
		When: Condition{Metric: "temp", Op: OpGt, Threshold: ptr(30)}})
	if d.cr.Core.Kind != core.Threshold {
		t.Fatalf("want core.Threshold, got %v", d.cr.Core.Kind)
	}
	d.send(0, "dev1", "dev1", map[string]float64{"temp": 20})
	d.assertQuiet()
	d.send(1, "dev1", "dev1", map[string]float64{"temp": 35})
	d.assertFires(core.Threshold, at(1))
}

func TestThresholdRawCEL(t *testing.T) {
	// The advanced escape hatch: a raw CEL leaf compiles and fires like the structured form.
	d := newDriver(t, Rule{ID: "t", Name: "hot", Type: TypeThreshold,
		When: Condition{CEL: `"temp" in m && m["temp"] > 30.0`}})
	d.send(1, "dev1", "dev1", map[string]float64{"temp": 35})
	d.assertFires(core.Threshold, at(1))
}

func TestDeltaRate(t *testing.T) {
	d := newDriver(t, Rule{ID: "d", Name: "spike", Type: TypeDeltaRate,
		Metric: "count", Op: OpGt, Threshold: ptr(10)})
	if d.cr.Core.Kind != core.DeltaRate || d.cr.Core.Op != core.GT || d.cr.Core.Thresh != 10 || d.cr.ValueMetric != "count" {
		t.Fatalf("lowering: %+v value=%q", d.cr.Core, d.cr.ValueMetric)
	}
	d.send(0, "dev1", "dev1", map[string]float64{"count": 100}) // prime
	d.assertQuiet()
	d.send(10, "dev1", "dev1", map[string]float64{"count": 130}) // +30 > 10
	d.assertFires(core.DeltaRate, at(10))
}

func TestRepeating(t *testing.T) {
	d := newDriver(t, Rule{ID: "rp", Name: "flap", Type: TypeRepeating,
		Count: 3, Window: Duration(10 * time.Second),
		When: Condition{Metric: "open", Op: OpGe, Threshold: ptr(1)}})
	if d.cr.Core.Kind != core.Repeating || d.cr.Core.Count != 3 || d.cr.Core.Window != 10*time.Second {
		t.Fatalf("lowering: %+v", d.cr.Core)
	}
	d.send(0, "dev1", "dev1", map[string]float64{"open": 1})
	d.send(1, "dev1", "dev1", map[string]float64{"open": 1})
	d.assertQuiet()
	d.send(2, "dev1", "dev1", map[string]float64{"open": 1})
	d.assertFires(core.Repeating, at(2))
}

func TestDuration(t *testing.T) {
	d := newDriver(t, Rule{ID: "du", Name: "stuck", Type: TypeDuration,
		Hold: Duration(10 * time.Second),
		When: Condition{Metric: "t", Op: OpGt, Threshold: ptr(30)}})
	if d.cr.Core.Kind != core.Duration || d.cr.Core.Hold != 10*time.Second {
		t.Fatalf("lowering: %+v", d.cr.Core)
	}
	d.send(0, "dev1", "dev1", map[string]float64{"t": 35}) // enter the matched run
	d.assertQuiet()
	d.advance(11) // the hold elapses on the watermark
	d.assertFires(core.Duration, at(10))
}

func TestDurationClearedByNonMatch(t *testing.T) {
	d := newDriver(t, Rule{ID: "du", Name: "stuck", Type: TypeDuration,
		Hold: Duration(10 * time.Second),
		When: Condition{Metric: "t", Op: OpGt, Threshold: ptr(30)}})
	d.send(0, "dev1", "dev1", map[string]float64{"t": 35}) // arm
	d.send(2, "dev1", "dev1", map[string]float64{"t": 20}) // drop below → cancels
	d.advance(15)
	d.assertQuiet()
}

func TestAbsence(t *testing.T) {
	d := newDriver(t, Rule{ID: "ab", Name: "dead", Type: TypeAbsence, Ttl: Duration(10 * time.Second)})
	if d.cr.Core.Kind != core.Absence || d.cr.Core.Timeout != 10*time.Second {
		t.Fatalf("lowering: %+v", d.cr.Core)
	}
	d.send(0, "dev1", "dev1", map[string]float64{"t": 1}) // heartbeat → arm dead-man
	d.assertQuiet()
	d.advance(11)
	d.assertFires(core.Absence, at(10))
}

func TestAggregateTumbling(t *testing.T) {
	d := newDriver(t, Rule{ID: "ag", Name: "avg", Type: TypeAggregate, Mode: ModeTumbling,
		Metric: "t", Agg: AggAvg, Op: OpGt, Threshold: ptr(30), Window: Duration(10 * time.Second)})
	if d.cr.Core.Kind != core.Aggregate || d.cr.Core.Agg != core.AggAvg || d.cr.Core.Window != 10*time.Second {
		t.Fatalf("lowering: %+v", d.cr.Core)
	}
	d.send(1, "dev1", "dev1", map[string]float64{"t": 40})
	d.send(2, "dev1", "dev1", map[string]float64{"t": 40})
	d.assertQuiet()
	d.advance(10) // window [0,10) closes on the watermark
	d.assertFires(core.Aggregate, at(10))
}

func TestAggregateSliding(t *testing.T) {
	d := newDriver(t, Rule{ID: "sl", Name: "slide", Type: TypeAggregate, Mode: ModeSliding,
		Metric: "t", Agg: AggMax, Op: OpGt, Threshold: ptr(50), Window: Duration(10 * time.Second)})
	if d.cr.Core.Kind != core.SlidingAgg {
		t.Fatalf("lowering: %+v", d.cr.Core)
	}
	d.send(1, "dev1", "dev1", map[string]float64{"t": 40})
	d.assertQuiet()
	d.send(2, "dev1", "dev1", map[string]float64{"t": 60}) // max 60 > 50
	d.assertFires(core.SlidingAgg, at(2))
}

func TestAggregateSession(t *testing.T) {
	d := newDriver(t, Rule{ID: "se", Name: "sess", Type: TypeAggregate, Mode: ModeSession,
		Gap: Duration(10 * time.Second), Agg: AggCount, Op: OpGe, Threshold: ptr(3)})
	if d.cr.Core.Kind != core.Session || d.cr.Core.Gap != 10*time.Second {
		t.Fatalf("lowering: %+v", d.cr.Core)
	}
	d.send(0, "dev1", "dev1", map[string]float64{"t": 1})
	d.send(2, "dev1", "dev1", map[string]float64{"t": 1})
	d.send(4, "dev1", "dev1", map[string]float64{"t": 1}) // 3 events, last @4 → deadline @14
	d.assertQuiet()
	d.advance(15)
	d.assertFires(core.Session, at(14))
}

func TestAggregateCountWindow(t *testing.T) {
	d := newDriver(t, Rule{ID: "cw", Name: "cnt", Type: TypeAggregate, Mode: ModeCount,
		Count: 3, Metric: "t", Agg: AggSum, Op: OpGe, Threshold: ptr(9)})
	if d.cr.Core.Kind != core.CountWindow || d.cr.Core.Count != 3 {
		t.Fatalf("lowering: %+v", d.cr.Core)
	}
	d.send(0, "dev1", "dev1", map[string]float64{"t": 3})
	d.send(1, "dev1", "dev1", map[string]float64{"t": 3})
	d.assertQuiet()
	d.send(2, "dev1", "dev1", map[string]float64{"t": 4}) // sum 10 >= 9 on the 3rd event
	d.assertFires(core.CountWindow, at(2))
}

func TestCorrelation(t *testing.T) {
	d := newDriver(t, Rule{ID: "co", Name: "area", Type: TypeCorrelation,
		AnchorType: "site", Count: 3, Window: Duration(100 * time.Second), MemberCap: 10})
	if d.cr.Core.Kind != core.Correlation || d.cr.Core.Count != 3 || d.cr.Core.MemberCap != 10 || d.cr.AnchorType != "site" {
		t.Fatalf("lowering: %+v anchor=%q", d.cr.Core, d.cr.AnchorType)
	}
	if !d.cr.KeyedByAnchor() {
		t.Fatal("correlation must be keyed by anchor")
	}
	d.send(0, "site-1", "devA", nil)
	d.send(1, "site-1", "devB", nil)
	d.assertQuiet()
	d.send(2, "site-1", "devC", nil) // 3 distinct members under the anchor
	d.assertFires(core.Correlation, at(2))
}

// TestCorrelationDefaultMemberCap proves an unset member cap resolves to the limit default
// (never unlimited) — the ADR-023 fail-safe posture.
func TestCorrelationDefaultMemberCap(t *testing.T) {
	cr, err := Compile(Rule{ID: "co", Name: "area", Type: TypeCorrelation,
		AnchorType: "site", Count: 3, Window: Duration(100 * time.Second)}, testLimits)
	if err != nil {
		t.Fatal(err)
	}
	if cr.Core.MemberCap != testLimits.DefaultCorrelationMemberCap {
		t.Fatalf("want default member cap %d, got %d", testLimits.DefaultCorrelationMemberCap, cr.Core.MemberCap)
	}
}

// TestCompileRejections is the fail-closed matrix: every structurally invalid rule is
// rejected at publish with an error the console can surface.
func TestCompileRejections(t *testing.T) {
	cases := []struct {
		name string
		rule Rule
	}{
		{"missing id", Rule{Name: "x", Type: TypeThreshold, When: Condition{Metric: "t", Op: OpGt, Threshold: ptr(1)}}},
		{"missing name", Rule{ID: "r", Type: TypeThreshold, When: Condition{Metric: "t", Op: OpGt, Threshold: ptr(1)}}},
		{"unknown type", Rule{ID: "r", Name: "x", Type: "bogus"}},
		{"threshold no condition", Rule{ID: "r", Name: "x", Type: TypeThreshold}},
		{"threshold stray window", Rule{ID: "r", Name: "x", Type: TypeThreshold,
			When: Condition{Metric: "t", Op: OpGt, Threshold: ptr(1)}, Window: Duration(time.Second)}},
		{"metric injection", Rule{ID: "r", Name: "x", Type: TypeThreshold,
			When: Condition{Metric: `t"] || size(m) > 0 || m["x`, Op: OpGt, Threshold: ptr(1)}}},
		{"structured and raw", Rule{ID: "r", Name: "x", Type: TypeThreshold,
			When: Condition{Metric: "t", Op: OpGt, Threshold: ptr(1), CEL: "true"}}},
		{"deltaRate no metric", Rule{ID: "r", Name: "x", Type: TypeDeltaRate, Op: OpGt, Threshold: ptr(1)}},
		{"deltaRate eq op", Rule{ID: "r", Name: "x", Type: TypeDeltaRate, Metric: "t", Op: OpEq, Threshold: ptr(1)}},
		{"repeating no count", Rule{ID: "r", Name: "x", Type: TypeRepeating, Window: Duration(time.Second),
			When: Condition{Metric: "t", Op: OpGe, Threshold: ptr(1)}}},
		{"duration no hold", Rule{ID: "r", Name: "x", Type: TypeDuration,
			When: Condition{Metric: "t", Op: OpGt, Threshold: ptr(1)}}},
		{"absence with when", Rule{ID: "r", Name: "x", Type: TypeAbsence, Ttl: Duration(time.Second),
			When: Condition{Metric: "t", Op: OpGt, Threshold: ptr(1)}}},
		{"absence no timeout", Rule{ID: "r", Name: "x", Type: TypeAbsence}},
		{"aggregate no mode", Rule{ID: "r", Name: "x", Type: TypeAggregate,
			Metric: "t", Agg: AggAvg, Op: OpGt, Threshold: ptr(1)}},
		{"aggregate count with metric", Rule{ID: "r", Name: "x", Type: TypeAggregate, Mode: ModeTumbling,
			Metric: "t", Agg: AggCount, Op: OpGt, Threshold: ptr(1), Window: Duration(time.Second)}},
		{"aggregate avg no metric", Rule{ID: "r", Name: "x", Type: TypeAggregate, Mode: ModeTumbling,
			Agg: AggAvg, Op: OpGt, Threshold: ptr(1), Window: Duration(time.Second)}},
		{"session with window", Rule{ID: "r", Name: "x", Type: TypeAggregate, Mode: ModeSession,
			Gap: Duration(time.Second), Window: Duration(time.Second), Agg: AggCount, Op: OpGe, Threshold: ptr(1)}},
		{"aggregate eq op", Rule{ID: "r", Name: "x", Type: TypeAggregate, Mode: ModeTumbling,
			Metric: "t", Agg: AggAvg, Op: OpEq, Threshold: ptr(1), Window: Duration(time.Second)}},
		{"correlation no anchor", Rule{ID: "r", Name: "x", Type: TypeCorrelation,
			Count: 3, Window: Duration(time.Second)}},
		{"correlation cap below count", Rule{ID: "r", Name: "x", Type: TypeCorrelation,
			AnchorType: "site", Count: 5, Window: Duration(time.Second), MemberCap: 2}},
		{"aggregate no threshold", Rule{ID: "r", Name: "x", Type: TypeAggregate, Mode: ModeTumbling,
			Metric: "t", Agg: AggAvg, Op: OpGt, Window: Duration(time.Second)}},
		{"deltaRate no threshold", Rule{ID: "r", Name: "x", Type: TypeDeltaRate, Metric: "t", Op: OpGt}},
		{"count over count window", Rule{ID: "r", Name: "x", Type: TypeAggregate, Mode: ModeCount,
			Count: 5, Agg: AggCount, Op: OpGt, Threshold: ptr(10)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Compile(tc.rule, testLimits); err == nil {
				t.Fatalf("expected %q to be rejected", tc.name)
			}
		})
	}
}
