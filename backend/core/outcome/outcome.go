// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package outcome is an EXPERIMENT: a declarative target over one measurement, per area, with an
// evaluation window and an error budget, judged from measurements the platform already stores.
//
// It is pure arithmetic. It reads nothing and holds no clock: the caller fetches the bucketed
// aggregates and says what "now" is, so the same inputs always give the same answer and an
// evaluation can be replayed.
//
// # What a compliance figure is
//
// The window is cut into fixed, epoch-aligned time buckets of Interval, and only COMPLETE
// buckets count: the span is the last Window / Interval of them, ending at the most recent bucket
// boundary at or before now. The bucket now falls in is still filling and is not judged, and the
// span is anchored on that boundary rather than on now, so the same number of buckets is judged
// however far into the current bucket the evaluation runs. A bucket is
//
//   - good when it holds at least MinSamples readings and its chosen aggregate (average, minimum
//     or maximum) satisfies the comparator against the threshold;
//   - bad when it holds enough readings and the aggregate does not;
//   - empty when it holds too few readings, or none.
//
// Compliance is good / total, where total is EVERY bucket the window holds. An empty bucket is
// therefore charged against the objective exactly as a bad one is: a district whose sensors went
// silent is not a district that is meeting its target, and an average over only the buckets that
// happen to have data would let silence improve the score. Coverage (the share of buckets that
// were judged at all) is reported beside it so a reader can tell "failing" from "not reporting".
//
// Compliance counts successful TIME BUCKETS. It is not the share of readings that were good, and
// it is not "uptime" in a device sense; with uneven sampling those differ.
//
// # Error budget
//
// Objective is the share of buckets that must be good, from MinObjective to MaxObjective. The budget is
// the share that may fall short, (1 - Objective) * total buckets. BudgetRemaining is the
// fraction of that budget still unspent: 1 with no shortfall, 0 when it is exactly spent,
// negative once overspent (it is not clipped, so how far over is visible).
//
// # Equity
//
// Every area is judged on its own and the spread between areas is reported: the worst area's
// compliance against the median area's. The pooled (city-wide) compliance is reported too,
// because it is the number that hides a failing neighbourhood, and showing both is the point.
package outcome

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"time"
)

// Comparator is how a bucket's aggregate is held against the threshold.
type Comparator string

const (
	// Less is good when the aggregate is strictly below the threshold.
	Less Comparator = "LT"
	// LessEqual is good when the aggregate is at or below the threshold.
	LessEqual Comparator = "LTE"
	// Greater is good when the aggregate is strictly above the threshold.
	Greater Comparator = "GT"
	// GreaterEqual is good when the aggregate is at or above the threshold.
	GreaterEqual Comparator = "GTE"
)

// Aggregation picks which of a bucket's aggregates is judged.
type Aggregation string

const (
	Avg Aggregation = "AVG"
	Min Aggregation = "MIN"
	Max Aggregation = "MAX"
)

// Status is the verdict on one area, or on the outcome as a whole.
type Status string

const (
	// StatusOK means the objective is met and more than AtRiskBudget of the error budget is left.
	StatusOK Status = "OK"
	// StatusAtRisk means the objective is met but the budget is nearly spent.
	StatusAtRisk Status = "AT_RISK"
	// StatusBreached means the objective is not met.
	StatusBreached Status = "BREACHED"
	// StatusNoData means not one bucket could be judged. It is never read as OK.
	StatusNoData Status = "NO_DATA"
)

// epsilon absorbs binary rounding where a ratio is held against a limit written as a decimal:
// 0.8 - 0.6 is 0.20000000000000007, and a spread exactly at its limit is not over it.
const epsilon = 1e-9

// Bounds. They keep a definition from asking for work the caller did not intend to price.
const (
	// AtRiskBudget is the share of the error budget below which an area is AT_RISK.
	AtRiskBudget = 0.25
	// MinInterval is the narrowest bucket. A multiple of a minute because the platform's
	// summaries are minute-granular.
	MinInterval = time.Minute
	// MaxWindow is the longest evaluation window.
	MaxWindow = 31 * 24 * time.Hour
	// MaxBucketsPerArea bounds Window / Interval.
	MaxBucketsPerArea = 10000
	// MaxAreas bounds how many areas one outcome names.
	MaxAreas = 25
	// MaxTotalBuckets bounds areas x buckets: the work one evaluation may ask of the store.
	MaxTotalBuckets = 20000
	// MinObjective and MaxObjective bound the objective. An objective of exactly 0 or 1 is
	// meaningless (nothing can fail, or nothing may), and one so near either end that the
	// rounding tolerance reaches it is refused for the same reason.
	MinObjective = 0.001
	MaxObjective = 0.999
	// DefaultMinSamples is how many readings make a bucket judgeable when none is set.
	DefaultMinSamples = 1
)

// Definition is an outcome: "Aggregation of Metric is Comparator Threshold in at least Objective
// of the Interval-wide buckets of the trailing Window, in each of Areas".
type Definition struct {
	Token       string
	Name        string
	Metric      string
	Aggregation Aggregation
	Comparator  Comparator
	Threshold   float64
	// Areas are the area tokens judged, each on its own.
	Areas    []string
	Window   time.Duration
	Interval time.Duration
	// Objective is the share of buckets that must be good, from MinObjective to MaxObjective.
	Objective float64
	// MinSamples is how many readings a bucket needs to be judged; 0 means DefaultMinSamples.
	MinSamples int64
	// MaxEquitySpread, when above 0, is the largest allowed gap between the median area's
	// compliance and the worst area's, as a fraction. 0 means no equity limit.
	MaxEquitySpread float64
}

// Validate reports the first reason the definition cannot be evaluated.
func (d Definition) Validate() error {
	if d.Token == "" {
		return errors.New("outcome: token is required")
	}
	if d.Metric == "" {
		return errors.New("outcome: metric is required")
	}
	switch d.Aggregation {
	case Avg, Min, Max:
	default:
		return fmt.Errorf("outcome: unknown aggregation %q", d.Aggregation)
	}
	switch d.Comparator {
	case Less, LessEqual, Greater, GreaterEqual:
	default:
		return fmt.Errorf("outcome: unknown comparator %q", d.Comparator)
	}
	if !finite(d.Threshold) {
		return errors.New("outcome: threshold must be a finite number")
	}
	if len(d.Areas) == 0 {
		return errors.New("outcome: at least one area is required")
	}
	if len(d.Areas) > MaxAreas {
		return fmt.Errorf("outcome: %d areas exceeds the limit of %d", len(d.Areas), MaxAreas)
	}
	seen := make(map[string]struct{}, len(d.Areas))
	for _, a := range d.Areas {
		if a == "" {
			return errors.New("outcome: an area token is empty")
		}
		if _, dup := seen[a]; dup {
			return fmt.Errorf("outcome: area %q is listed twice", a)
		}
		seen[a] = struct{}{}
	}
	if d.Interval < MinInterval || d.Interval%time.Minute != 0 || (24*time.Hour)%d.Interval != 0 {
		return fmt.Errorf("outcome: interval %s must be a whole number of minutes that divides a day", d.Interval)
	}
	if d.Window <= 0 || d.Window > MaxWindow {
		return fmt.Errorf("outcome: window %s must be above zero and at most %s", d.Window, MaxWindow)
	}
	if d.Window < d.Interval {
		return fmt.Errorf("outcome: window %s is shorter than the interval %s", d.Window, d.Interval)
	}
	perArea := int64(d.Window / d.Interval)
	if perArea > MaxBucketsPerArea {
		return fmt.Errorf("outcome: %d buckets per area exceeds the limit of %d", perArea, MaxBucketsPerArea)
	}
	if perArea*int64(len(d.Areas)) > MaxTotalBuckets {
		return fmt.Errorf("outcome: %d buckets in all exceeds the limit of %d; narrow the window, widen the interval or name fewer areas",
			perArea*int64(len(d.Areas)), MaxTotalBuckets)
	}
	// Held away from both ends by more than the rounding tolerance, which would otherwise be
	// able to turn "every bucket failed" into "within tolerance of an objective of nearly zero".
	if !(d.Objective >= MinObjective && d.Objective <= MaxObjective) {
		return fmt.Errorf("outcome: objective must be between %g and %g", MinObjective, MaxObjective)
	}
	if d.MinSamples < 0 {
		return errors.New("outcome: minSamples must not be negative")
	}
	if !finite(d.MaxEquitySpread) || d.MaxEquitySpread < 0 || d.MaxEquitySpread >= 1 {
		return errors.New("outcome: maxEquitySpread must be in [0, 1)")
	}
	return nil
}

func (d Definition) minSamples() int64 {
	if d.MinSamples == 0 {
		return DefaultMinSamples
	}
	return d.MinSamples
}

// Span is the exact set of buckets an evaluation judges: Count buckets of Interval from First.
type Span struct {
	First    time.Time
	Interval time.Duration
	Count    int64
}

// Start is the first instant the span covers.
func (s Span) Start() time.Time { return s.First }

// End is the instant the last bucket closes.
func (s Span) End() time.Time { return s.First.Add(time.Duration(s.Count) * s.Interval) }

// SpanFor returns the complete epoch-aligned buckets of the trailing window ending at now. The
// bucket that now falls inside is still filling and is left out, so the span ends at the last
// boundary at or before now. All arithmetic is on UTC epoch seconds, which has no daylight-saving
// ambiguity.
func SpanFor(d Definition, now time.Time) (Span, error) {
	if err := d.Validate(); err != nil {
		return Span{}, err
	}
	iv := int64(d.Interval / time.Second)
	end := floorDiv(now.Unix(), iv) * iv
	first := end - int64(d.Window/time.Second)
	// The first bucket must start at or after the window's start, so round the start UP.
	first = ceilDiv(first, iv) * iv
	n := (end - first) / iv
	if n < 1 {
		return Span{}, errors.New("outcome: the window holds no complete bucket yet")
	}
	return Span{First: time.Unix(first, 0).UTC(), Interval: d.Interval, Count: n}, nil
}

// Bucket is one stored time bucket of a measurement: its start, how many readings it holds, and
// the aggregates. An aggregate is nil when the store had none.
type Bucket struct {
	Start time.Time
	Count int64
	Avg   *float64
	Min   *float64
	Max   *float64
}

// AreaInput is the buckets the store returned for one area.
type AreaInput struct {
	Area    string
	Buckets []Bucket
}

// AreaResult is the verdict on one area.
type AreaResult struct {
	Area   string
	Status Status
	// Good, Bad and Empty partition Total.
	Good, Bad, Empty, Total int64
	// Compliance is Good / Total.
	Compliance float64
	// Coverage is (Good + Bad) / Total: how much of the window could be judged at all.
	Coverage float64
	// BudgetRemaining is the unspent fraction of the error budget; negative when overspent.
	BudgetRemaining float64
}

// Equity is how evenly the outcome is met across the areas that were judged.
type Equity struct {
	// Evaluated is how many areas had at least one judged bucket, Excluded how many had none.
	Evaluated, Excluded int
	// Computed is false when fewer than two areas were evaluated: there is no spread to speak of.
	Computed bool
	// Complete is false when any requested area was excluded, because the figures then describe
	// a smaller set of areas than was asked about.
	Complete bool
	// Worst is the lowest compliance among evaluated areas and WorstArea its area (the smallest
	// token on a tie). Median is the median compliance. Spread is Median - Worst.
	Worst, Median, Spread float64
	WorstArea             string
	// Pooled is the compliance over every bucket of every requested area.
	Pooled float64
	// Breached is true when a limit was set and Spread is over it.
	Breached bool
}

// Result is a whole evaluation.
type Result struct {
	Span  Span
	Areas []AreaResult
	// ObjectiveStatus is the worst per-area status.
	ObjectiveStatus Status
	// Status is ObjectiveStatus raised to at least AT_RISK when the equity limit is breached.
	Status Status
	Equity Equity
	// Stray counts buckets the store returned that fall outside the span and were ignored.
	Stray int
}

// Evaluate judges def at now over the buckets the store returned.
//
// inputs must name each of def.Areas exactly once, no other area; an area with nothing stored is
// an AreaInput with no buckets, which is NO_DATA rather than a gap in the answer.
func Evaluate(def Definition, now time.Time, inputs []AreaInput) (Result, error) {
	span, err := SpanFor(def, now)
	if err != nil {
		return Result{}, err
	}
	byArea := make(map[string]AreaInput, len(inputs))
	for _, in := range inputs {
		if _, dup := byArea[in.Area]; dup {
			return Result{}, fmt.Errorf("outcome: input for area %q is given twice", in.Area)
		}
		byArea[in.Area] = in
	}
	for _, a := range def.Areas {
		if _, ok := byArea[a]; !ok {
			return Result{}, fmt.Errorf("outcome: no input for area %q", a)
		}
	}
	if len(byArea) != len(def.Areas) {
		return Result{}, errors.New("outcome: input names an area the definition does not")
	}

	res := Result{Span: span, Areas: make([]AreaResult, 0, len(def.Areas))}
	for _, a := range def.Areas {
		ar, stray, err := evaluateArea(def, span, byArea[a])
		if err != nil {
			return Result{}, err
		}
		res.Stray += stray
		res.Areas = append(res.Areas, ar)
	}
	res.ObjectiveStatus = worstStatus(res.Areas)
	res.Equity = equityOf(def, res.Areas)
	res.Status = res.ObjectiveStatus
	if res.Equity.Breached && rank(res.Status) < rank(StatusAtRisk) {
		res.Status = StatusAtRisk
	}
	return res, nil
}

func evaluateArea(def Definition, span Span, in AreaInput) (AreaResult, int, error) {
	iv := int64(span.Interval / time.Second)
	first := span.First.Unix()
	seen := make(map[int64]struct{}, len(in.Buckets))
	var good, bad, stray int64
	for _, b := range in.Buckets {
		s := b.Start.Unix()
		if b.Start.Nanosecond() != 0 || s%iv != 0 {
			return AreaResult{}, 0, fmt.Errorf("outcome: area %q bucket %s is not aligned to %s", in.Area, b.Start.UTC().Format(time.RFC3339), span.Interval)
		}
		if _, dup := seen[s]; dup {
			return AreaResult{}, 0, fmt.Errorf("outcome: area %q has two buckets starting %s", in.Area, b.Start.UTC().Format(time.RFC3339))
		}
		seen[s] = struct{}{}
		if b.Count < 0 {
			return AreaResult{}, 0, fmt.Errorf("outcome: area %q bucket %s has a negative count", in.Area, b.Start.UTC().Format(time.RFC3339))
		}
		if s < first || s >= first+span.Count*iv {
			stray++
			continue
		}
		if b.Count < def.minSamples() {
			continue // too few readings to judge: counted as empty below
		}
		v := pick(def.Aggregation, b)
		if v == nil {
			return AreaResult{}, 0, fmt.Errorf("outcome: area %q bucket %s holds %d readings but no %s", in.Area, b.Start.UTC().Format(time.RFC3339), b.Count, def.Aggregation)
		}
		if !finite(*v) {
			return AreaResult{}, 0, fmt.Errorf("outcome: area %q bucket %s has a non-finite %s", in.Area, b.Start.UTC().Format(time.RFC3339), def.Aggregation)
		}
		if holds(def.Comparator, *v, def.Threshold) {
			good++
		} else {
			bad++
		}
	}
	total := span.Count
	ar := AreaResult{Area: in.Area, Good: good, Bad: bad, Empty: total - good - bad, Total: total}
	ar.Compliance = float64(good) / float64(total)
	ar.Coverage = float64(good+bad) / float64(total)
	short := float64(total - good) // bad and empty alike
	ar.BudgetRemaining = 1 - short/((1-def.Objective)*float64(total))
	switch {
	case good+bad == 0:
		ar.Status = StatusNoData
	case ar.Compliance < def.Objective-epsilon:
		ar.Status = StatusBreached
	case ar.BudgetRemaining < AtRiskBudget-epsilon:
		ar.Status = StatusAtRisk
	default:
		ar.Status = StatusOK
	}
	return ar, int(stray), nil
}

func pick(a Aggregation, b Bucket) *float64 {
	switch a {
	case Avg:
		return b.Avg
	case Min:
		return b.Min
	default:
		return b.Max
	}
}

func holds(c Comparator, v, threshold float64) bool {
	switch c {
	case Less:
		return v < threshold
	case LessEqual:
		return v <= threshold
	case Greater:
		return v > threshold
	default:
		return v >= threshold
	}
}

func equityOf(def Definition, areas []AreaResult) Equity {
	var eq Equity
	var judged []AreaResult
	var good, total int64
	for _, a := range areas {
		good += a.Good
		total += a.Total
		if a.Status == StatusNoData {
			eq.Excluded++
			continue
		}
		judged = append(judged, a)
	}
	eq.Evaluated = len(judged)
	eq.Complete = eq.Excluded == 0
	if total > 0 {
		eq.Pooled = float64(good) / float64(total)
	}
	if len(judged) < 2 {
		return eq
	}
	sort.Slice(judged, func(i, j int) bool {
		if judged[i].Compliance != judged[j].Compliance {
			return judged[i].Compliance < judged[j].Compliance
		}
		return judged[i].Area < judged[j].Area
	})
	eq.Computed = true
	eq.Worst, eq.WorstArea = judged[0].Compliance, judged[0].Area
	n := len(judged)
	if n%2 == 1 {
		eq.Median = judged[n/2].Compliance
	} else {
		eq.Median = (judged[n/2-1].Compliance + judged[n/2].Compliance) / 2
	}
	eq.Spread = eq.Median - eq.Worst
	eq.Breached = def.MaxEquitySpread > 0 && eq.Spread > def.MaxEquitySpread+epsilon
	return eq
}

// rank orders statuses by how much attention they need. NO_DATA outranks AT_RISK because an
// area that cannot be seen is a worse state to be in than one that is merely close to its limit,
// and ranks below BREACHED because a known failure is the more urgent.
func rank(s Status) int {
	switch s {
	case StatusOK:
		return 0
	case StatusAtRisk:
		return 1
	case StatusNoData:
		return 2
	default:
		return 3
	}
}

func worstStatus(areas []AreaResult) Status {
	worst := StatusOK
	for _, a := range areas {
		if rank(a.Status) > rank(worst) {
			worst = a.Status
		}
	}
	return worst
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func floorDiv(a, b int64) int64 {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}

func ceilDiv(a, b int64) int64 { return -floorDiv(-a, b) }
