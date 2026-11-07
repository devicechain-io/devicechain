// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package outcome

import (
	"math"
	"strings"
	"testing"
	"time"
)

// now sits mid-bucket on purpose: the bucket it falls in is still filling and must not be judged.
var now = time.Date(2026, 11, 7, 12, 7, 30, 0, time.UTC)

func def() Definition {
	return Definition{
		Token: "aq", Name: "Air quality", Metric: "pm25",
		Aggregation: Max, Comparator: LessEqual, Threshold: 15,
		Areas:  []string{"north", "south"},
		Window: 4 * time.Hour, Interval: time.Hour,
		Objective: 0.75,
	}
}

func f(v float64) *float64 { return &v }

// buckets builds one bucket per hour of the span with the given max values; nil means no bucket.
func buckets(t *testing.T, d Definition, vals ...*float64) []Bucket {
	t.Helper()
	sp, err := SpanFor(d, now)
	if err != nil {
		t.Fatal(err)
	}
	var out []Bucket
	for i, v := range vals {
		if v == nil {
			continue
		}
		out = append(out, Bucket{Start: sp.First.Add(time.Duration(i) * sp.Interval), Count: 10, Avg: v, Min: v, Max: v})
	}
	return out
}

func TestSpanIsCompleteAlignedBucketsOnly(t *testing.T) {
	sp, err := SpanFor(def(), now)
	if err != nil {
		t.Fatal(err)
	}
	// 12:07:30 falls in the 12:00 bucket, still filling: the span ends at 12:00.
	if want := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC); !sp.End().Equal(want) {
		t.Fatalf("end = %s, want %s", sp.End(), want)
	}
	if sp.Count != 4 || !sp.First.Equal(time.Date(2026, 11, 7, 8, 0, 0, 0, time.UTC)) {
		t.Fatalf("span = %+v", sp)
	}
}

func TestSpanOnExactBoundaryKeepsTheClosedBucket(t *testing.T) {
	sp, err := SpanFor(def(), time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if !sp.End().Equal(time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)) || sp.Count != 4 {
		t.Fatalf("span = %+v", sp)
	}
}

func TestSpanWindowNotAMultipleOfIntervalRoundsStartUp(t *testing.T) {
	d := def()
	d.Window = 3*time.Hour + 30*time.Minute
	sp, err := SpanFor(d, now)
	if err != nil {
		t.Fatal(err)
	}
	// The window starts 08:30; the first whole bucket at or after that is 09:00.
	if sp.Count != 3 || sp.First.Hour() != 9 {
		t.Fatalf("span = %+v", sp)
	}
}

func TestValidateRefusals(t *testing.T) {
	cases := map[string]func(*Definition){
		"token":       func(d *Definition) { d.Token = "" },
		"metric":      func(d *Definition) { d.Metric = "" },
		"aggregation": func(d *Definition) { d.Aggregation = "P99" },
		"comparator":  func(d *Definition) { d.Comparator = "EQ" },
		"threshold":   func(d *Definition) { d.Threshold = math.NaN() },
		"no areas":    func(d *Definition) { d.Areas = nil },
		"dup area":    func(d *Definition) { d.Areas = []string{"a", "a"} },
		"empty area":  func(d *Definition) { d.Areas = []string{""} },
		"too many areas": func(d *Definition) {
			d.Areas = nil
			for i := 0; i <= MaxAreas; i++ {
				d.Areas = append(d.Areas, string(rune('a'+i)))
			}
		},
		"interval not minutes":    func(d *Definition) { d.Interval = 90 * time.Second },
		"interval below a minute": func(d *Definition) { d.Interval = 30 * time.Second },
		"interval not dividing day": func(d *Definition) {
			d.Interval = 7 * time.Minute
		},
		"window zero":       func(d *Definition) { d.Window = 0 },
		"window too long":   func(d *Definition) { d.Window = MaxWindow + time.Hour },
		"window < interval": func(d *Definition) { d.Window = time.Minute },
		"too many buckets":  func(d *Definition) { d.Window = MaxWindow; d.Interval = time.Minute },
		"too much work": func(d *Definition) {
			d.Window = 20 * 24 * time.Hour
			d.Interval = 5 * time.Minute // 5760 per area x 2 areas
			d.Areas = []string{"a", "b", "c", "d"}
		},
		"objective 0":               func(d *Definition) { d.Objective = 0 },
		"objective 1":               func(d *Definition) { d.Objective = 1 },
		"objective below the floor": func(d *Definition) { d.Objective = MinObjective / 2 },
		"objective above the cap":   func(d *Definition) { d.Objective = (1 + MaxObjective) / 2 },
		"objective NaN":             func(d *Definition) { d.Objective = math.NaN() },
		"min samples":               func(d *Definition) { d.MinSamples = -1 },
		"spread":                    func(d *Definition) { d.MaxEquitySpread = 1 },
		"spread neg":                func(d *Definition) { d.MaxEquitySpread = -0.1 },
	}
	if err := def().Validate(); err != nil {
		t.Fatalf("baseline definition refused: %v", err)
	}
	for name, mut := range cases {
		d := def()
		mut(&d)
		if err := d.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestComparators(t *testing.T) {
	for _, c := range []struct {
		cmp  Comparator
		v    float64
		want bool
	}{
		{Less, 14.9, true}, {Less, 15, false},
		{LessEqual, 15, true}, {LessEqual, 15.1, false},
		{Greater, 15.1, true}, {Greater, 15, false},
		{GreaterEqual, 15, true}, {GreaterEqual, 14.9, false},
	} {
		if got := holds(c.cmp, c.v, 15); got != c.want {
			t.Errorf("%s %v = %v", c.cmp, c.v, got)
		}
	}
}

func TestAggregationIsTheOneJudged(t *testing.T) {
	d := def()
	d.Areas = []string{"a"}
	b := []Bucket{{Start: mustSpan(t, d).First, Count: 3, Avg: f(10), Min: f(1), Max: f(40)}}
	for agg, wantGood := range map[Aggregation]int64{Avg: 1, Min: 1, Max: 0} {
		d.Aggregation = agg
		r, err := Evaluate(d, now, []AreaInput{{Area: "a", Buckets: b}})
		if err != nil {
			t.Fatal(err)
		}
		if r.Areas[0].Good != wantGood {
			t.Errorf("%s: good = %d, want %d", agg, r.Areas[0].Good, wantGood)
		}
	}
}

func mustSpan(t *testing.T, d Definition) Span {
	t.Helper()
	sp, err := SpanFor(d, now)
	if err != nil {
		t.Fatal(err)
	}
	return sp
}

func TestStatusLadder(t *testing.T) {
	d := def()
	d.Areas = []string{"a"}
	d.Window = 20 * time.Hour // 20 buckets; objective .75 => budget of 5 short buckets
	cases := []struct {
		name  string
		short int // buckets that miss (bad)
		want  Status
		rem   float64
	}{
		{"none short", 0, StatusOK, 1},
		{"one short", 1, StatusOK, 0.8},
		{"three short leaves .4", 3, StatusOK, 0.4},
		{"four short leaves .2 => at risk", 4, StatusAtRisk, 0.2},
		{"exactly spent still meets objective", 5, StatusAtRisk, 0},
		{"overspent", 6, StatusBreached, -0.2},
	}
	for _, c := range cases {
		var vals []*float64
		for i := 0; i < 20; i++ {
			if i < c.short {
				vals = append(vals, f(99))
			} else {
				vals = append(vals, f(1))
			}
		}
		r, err := Evaluate(d, now, []AreaInput{{Area: "a", Buckets: buckets(t, d, vals...)}})
		if err != nil {
			t.Fatal(err)
		}
		a := r.Areas[0]
		if a.Status != c.want || math.Abs(a.BudgetRemaining-c.rem) > 1e-9 {
			t.Errorf("%s: status %s remaining %v, want %s %v", c.name, a.Status, a.BudgetRemaining, c.want, c.rem)
		}
	}
}

func TestSilenceIsChargedAgainstTheObjective(t *testing.T) {
	d := def()
	d.Areas = []string{"a"}
	// One good bucket and three empty: judged-only compliance would be 100%.
	r, err := Evaluate(d, now, []AreaInput{{Area: "a", Buckets: buckets(t, d, f(1), nil, nil, nil)}})
	if err != nil {
		t.Fatal(err)
	}
	a := r.Areas[0]
	if a.Status != StatusBreached || a.Compliance != 0.25 || a.Coverage != 0.25 || a.Empty != 3 {
		t.Fatalf("area = %+v", a)
	}
	// The budget is 25% of 4 buckets = 1 bucket; three silent ones overspend it threefold over.
	if math.Abs(a.BudgetRemaining-(-2)) > 1e-9 {
		t.Fatalf("budget remaining = %v, want -2: silent buckets must be charged", a.BudgetRemaining)
	}
}

func TestNoDataIsNeverOK(t *testing.T) {
	d := def()
	r, err := Evaluate(d, now, []AreaInput{{Area: "north"}, {Area: "south"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range r.Areas {
		if a.Status != StatusNoData {
			t.Fatalf("%s = %s", a.Area, a.Status)
		}
	}
	if r.Status != StatusNoData || r.Equity.Computed || r.Equity.Complete {
		t.Fatalf("result = %+v", r)
	}
}

func TestMinSamplesLeavesASparseBucketUnjudged(t *testing.T) {
	d := def()
	d.Areas = []string{"a"}
	d.MinSamples = 5
	b := buckets(t, d, f(1), f(1), f(1), f(1))
	b[0].Count = 4 // one short of judgeable
	r, err := Evaluate(d, now, []AreaInput{{Area: "a", Buckets: b}})
	if err != nil {
		t.Fatal(err)
	}
	if a := r.Areas[0]; a.Good != 3 || a.Empty != 1 {
		t.Fatalf("area = %+v", a)
	}
}

func TestEquityWorstMedianSpread(t *testing.T) {
	d := def()
	d.Areas = []string{"a", "b", "c", "d"}
	d.Window = 10 * time.Hour
	mk := func(good int) []Bucket {
		var v []*float64
		for i := 0; i < 10; i++ {
			if i < good {
				v = append(v, f(1))
			} else {
				v = append(v, f(99))
			}
		}
		return buckets(t, d, v...)
	}
	r, err := Evaluate(d, now, []AreaInput{
		{Area: "a", Buckets: mk(10)}, {Area: "b", Buckets: mk(9)},
		{Area: "c", Buckets: mk(8)}, {Area: "d", Buckets: mk(2)},
	})
	if err != nil {
		t.Fatal(err)
	}
	e := r.Equity
	// compliance .2 .8 .9 1.0 : median (.8+.9)/2, worst .2 in d.
	if !e.Computed || e.WorstArea != "d" || math.Abs(e.Median-0.85) > 1e-9 || math.Abs(e.Worst-0.2) > 1e-9 || math.Abs(e.Spread-0.65) > 1e-9 {
		t.Fatalf("equity = %+v", e)
	}
	if math.Abs(e.Pooled-0.725) > 1e-9 {
		t.Fatalf("pooled = %v", e.Pooled)
	}
}

// The reason the object exists: the city-wide figure passes while one district fails.
func TestThePooledFigureCanPassWhileADistrictFails(t *testing.T) {
	d := def()
	d.Areas = []string{"a", "b", "c", "d", "e"}
	d.Window = 20 * time.Hour
	d.Objective = 0.8
	mk := func(good int) []Bucket {
		var v []*float64
		for i := 0; i < 20; i++ {
			if i < good {
				v = append(v, f(1))
			} else {
				v = append(v, f(99))
			}
		}
		return buckets(t, d, v...)
	}
	r, err := Evaluate(d, now, []AreaInput{
		{Area: "a", Buckets: mk(20)}, {Area: "b", Buckets: mk(20)}, {Area: "c", Buckets: mk(20)},
		{Area: "d", Buckets: mk(20)}, {Area: "e", Buckets: mk(6)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.Equity.Pooled < d.Objective {
		t.Fatalf("pooled %v should meet the objective for this control", r.Equity.Pooled)
	}
	if r.Status != StatusBreached || r.Areas[4].Status != StatusBreached {
		t.Fatalf("status = %s, e = %s", r.Status, r.Areas[4].Status)
	}
}

func TestEquityBreachRaisesAnOtherwiseGreenOutcome(t *testing.T) {
	d := def()
	d.Window = 10 * time.Hour
	d.Objective = 0.5
	d.MaxEquitySpread = 0.2
	mk := func(good int) []Bucket {
		var v []*float64
		for i := 0; i < 10; i++ {
			if i < good {
				v = append(v, f(1))
			} else {
				v = append(v, f(99))
			}
		}
		return buckets(t, d, v...)
	}
	r, err := Evaluate(d, now, []AreaInput{{Area: "north", Buckets: mk(10)}, {Area: "south", Buckets: mk(6)}})
	if err != nil {
		t.Fatal(err)
	}
	// Both meet the .5 objective; the .2 spread limit is broken by .2 > ... median is (.6+1)/2=.8, worst .6.
	if r.ObjectiveStatus == StatusBreached {
		t.Fatalf("objective status = %s", r.ObjectiveStatus)
	}
	if math.Abs(r.Equity.Spread-0.2) > 1e-9 {
		t.Fatalf("spread = %v", r.Equity.Spread)
	}
	// spread exactly at the limit is not a breach (strictly over).
	if r.Equity.Breached {
		t.Fatal("a spread equal to the limit must not breach")
	}
	d.MaxEquitySpread = 0.19
	r, _ = Evaluate(d, now, []AreaInput{{Area: "north", Buckets: mk(10)}, {Area: "south", Buckets: mk(6)}})
	if !r.Equity.Breached || rank(r.Status) < rank(StatusAtRisk) {
		t.Fatalf("result = %+v", r)
	}
}

func TestExcludedAreaMakesEquityIncomplete(t *testing.T) {
	d := def()
	d.Areas = []string{"a", "b", "c"}
	r, err := Evaluate(d, now, []AreaInput{
		{Area: "a", Buckets: buckets(t, d, f(1), f(1), f(1), f(1))},
		{Area: "b", Buckets: buckets(t, d, f(1), f(1), f(1), f(1))},
		{Area: "c"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.Equity.Complete || r.Equity.Excluded != 1 || r.Equity.Evaluated != 2 || !r.Equity.Computed {
		t.Fatalf("equity = %+v", r.Equity)
	}
	if r.Status != StatusNoData {
		t.Fatalf("status = %s", r.Status)
	}
}

func TestSingleEvaluatedAreaHasNoSpread(t *testing.T) {
	d := def()
	r, err := Evaluate(d, now, []AreaInput{
		{Area: "north", Buckets: buckets(t, d, f(1), f(1), f(1), f(1))}, {Area: "south"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.Equity.Computed || r.Equity.Spread != 0 {
		t.Fatalf("equity = %+v", r.Equity)
	}
}

func TestInputRefusals(t *testing.T) {
	d := def()
	sp := mustSpan(t, d)
	ok := []Bucket{{Start: sp.First, Count: 1, Max: f(1)}}
	cases := map[string][]AreaInput{
		"missing area":     {{Area: "north", Buckets: ok}},
		"duplicate area":   {{Area: "north"}, {Area: "north"}, {Area: "south"}},
		"unknown area":     {{Area: "north"}, {Area: "south"}, {Area: "west"}},
		"misaligned":       {{Area: "north", Buckets: []Bucket{{Start: sp.First.Add(time.Minute), Count: 1, Max: f(1)}}}, {Area: "south"}},
		"duplicate bucket": {{Area: "north", Buckets: []Bucket{ok[0], ok[0]}}, {Area: "south"}},
		"negative count":   {{Area: "north", Buckets: []Bucket{{Start: sp.First, Count: -1, Max: f(1)}}}, {Area: "south"}},
		"count no value":   {{Area: "north", Buckets: []Bucket{{Start: sp.First, Count: 2}}}, {Area: "south"}},
		"non finite":       {{Area: "north", Buckets: []Bucket{{Start: sp.First, Count: 2, Max: f(math.Inf(1))}}}, {Area: "south"}},
	}
	for name, in := range cases {
		if _, err := Evaluate(d, now, in); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := Evaluate(d, now, cases["missing area"]); err == nil || !strings.Contains(err.Error(), `no input for area "south"`) {
		t.Errorf("a missing area must be named, got %v", err)
	}
}

func TestBucketsOutsideTheSpanAreIgnoredAndCounted(t *testing.T) {
	d := def()
	d.Areas = []string{"a"}
	sp := mustSpan(t, d)
	b := buckets(t, d, f(1), f(1), f(1), f(1))
	b = append(b,
		Bucket{Start: sp.End(), Count: 5, Max: f(99)},                 // the still-filling bucket
		Bucket{Start: sp.First.Add(-time.Hour), Count: 5, Max: f(99)}, // before the window
	)
	r, err := Evaluate(d, now, []AreaInput{{Area: "a", Buckets: b}})
	if err != nil {
		t.Fatal(err)
	}
	if r.Stray != 2 || r.Areas[0].Good != 4 || r.Areas[0].Status != StatusOK {
		t.Fatalf("result = %+v", r)
	}
}

func TestEvaluateIsDeterministicAndOrderIndependent(t *testing.T) {
	d := def()
	a := buckets(t, d, f(1), f(99), f(1), f(1))
	rev := []Bucket{a[3], a[2], a[1], a[0]}
	r1, _ := Evaluate(d, now, []AreaInput{{Area: "north", Buckets: a}, {Area: "south"}})
	r2, _ := Evaluate(d, now, []AreaInput{{Area: "south"}, {Area: "north", Buckets: rev}})
	if r1.Areas[0] != r2.Areas[0] || r1.Equity != r2.Equity || r1.Status != r2.Status {
		t.Fatalf("%+v != %+v", r1, r2)
	}
}

func TestTieForWorstTakesTheSmallestToken(t *testing.T) {
	d := def()
	d.Areas = []string{"zeta", "alpha", "mid"}
	all := buckets(t, d, f(1), f(1), f(1), f(1))
	half := buckets(t, d, f(1), f(1), f(99), f(99))
	r, err := Evaluate(d, now, []AreaInput{{Area: "zeta", Buckets: half}, {Area: "alpha", Buckets: half}, {Area: "mid", Buckets: all}})
	if err != nil {
		t.Fatal(err)
	}
	if r.Equity.WorstArea != "alpha" {
		t.Fatalf("worst = %s", r.Equity.WorstArea)
	}
}

func TestErrorsNameNoSecrets(t *testing.T) {
	if err := (Definition{}).Validate(); err == nil || !strings.HasPrefix(err.Error(), "outcome:") {
		t.Fatalf("err = %v", err)
	}
}

// The equity limit alone can move the outcome's status: both areas meet the objective with
// plenty of budget left, yet the gap between them is over the limit.
func TestEquityBreachAloneRaisesTheStatus(t *testing.T) {
	d := def()
	d.Window = 10 * time.Hour
	d.Objective = 0.5
	d.MaxEquitySpread = 0.05
	mk := func(good int) []Bucket {
		var v []*float64
		for i := 0; i < 10; i++ {
			if i < good {
				v = append(v, f(1))
			} else {
				v = append(v, f(99))
			}
		}
		return buckets(t, d, v...)
	}
	r, err := Evaluate(d, now, []AreaInput{{Area: "north", Buckets: mk(10)}, {Area: "south", Buckets: mk(8)}})
	if err != nil {
		t.Fatal(err)
	}
	if r.ObjectiveStatus != StatusOK {
		t.Fatalf("objective status = %s, want OK (both areas have budget to spare)", r.ObjectiveStatus)
	}
	if !r.Equity.Breached || r.Status != StatusAtRisk {
		t.Fatalf("status = %s, equity = %+v", r.Status, r.Equity)
	}
	// And a limit of zero is "no limit", not "no gap allowed".
	d.MaxEquitySpread = 0
	r, err = Evaluate(d, now, []AreaInput{{Area: "north", Buckets: mk(10)}, {Area: "south", Buckets: mk(8)}})
	if err != nil {
		t.Fatal(err)
	}
	if r.Equity.Breached || r.Status != StatusOK || r.Equity.Spread <= 0 {
		t.Fatalf("a zero limit must mean no limit: status = %s, equity = %+v", r.Status, r.Equity)
	}
}

// A tiny objective must not let every bucket failing read as meeting it (the tolerance used for
// rounding is far below the smallest objective that is accepted).
func TestTheSmallestObjectiveStillBreachesWhenEveryBucketFails(t *testing.T) {
	d := def()
	d.Areas = []string{"a"}
	d.Objective = MinObjective
	r, err := Evaluate(d, now, []AreaInput{{Area: "a", Buckets: buckets(t, d, f(99), f(99), f(99), f(99))}})
	if err != nil {
		t.Fatal(err)
	}
	if r.Areas[0].Status != StatusBreached {
		t.Fatalf("status = %s", r.Areas[0].Status)
	}
}

// The span is anchored on the last bucket boundary, so the same number of buckets is judged
// wherever in the current bucket the evaluation runs.
func TestSpanLengthDoesNotDependOnPositionInTheBucket(t *testing.T) {
	for _, sec := range []int{0, 1, 1799, 3599} {
		at := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC).Add(time.Duration(sec) * time.Second)
		sp, err := SpanFor(def(), at)
		if err != nil {
			t.Fatal(err)
		}
		if sp.Count != 4 || !sp.End().Equal(time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)) {
			t.Fatalf("at +%ds: %+v", sec, sp)
		}
	}
}
