// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/outcome"
)

var outcomeNow = time.Date(2026, 11, 7, 12, 7, 30, 0, time.UTC)

type fakeReader struct {
	calls []MeasurementAggregationCriteria
	// perArea is the max value of every hourly bucket, by area token; absent => no buckets.
	perArea map[string][]float64
	fail    map[string]error
}

func (f *fakeReader) BucketedMeasurements(_ context.Context, c MeasurementAggregationCriteria) ([]MeasurementBucket, error) {
	f.calls = append(f.calls, c)
	token := *c.AnchorToken
	if err := f.fail[token]; err != nil {
		return nil, err
	}
	var out []MeasurementBucket
	for i, v := range f.perArea[token] {
		out = append(out, MeasurementBucket{
			BucketStart: c.StartTime.Add(time.Duration(i) * time.Hour),
			Name:        *c.Name,
			Avg:         sql.NullFloat64{Float64: v, Valid: true},
			Min:         sql.NullFloat64{Float64: v, Valid: true},
			Max:         sql.NullFloat64{Float64: v, Valid: true},
			Count:       12,
		})
	}
	return out, nil
}

func outcomeDef() outcome.Definition {
	return outcome.Definition{
		Token: "aq", Metric: "pm25", Aggregation: outcome.Max, Comparator: outcome.LessEqual, Threshold: 15,
		Areas:  []string{"d1", "d2", "d3", "d4", "d5"},
		Window: 10 * time.Hour, Interval: time.Hour, Objective: 0.8,
	}
}

func ones(n int, v float64) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = v
	}
	return out
}

// THE CONTROL: the pooled, city-wide figure passes while one district fails. Without
// per-area evaluation there is nothing to say which district, or that any is failing.
func TestEvaluateOutcomeSurfacesAFailingDistrictBehindAPassingPool(t *testing.T) {
	bad := append(ones(5, 99), ones(5, 1)...) // half the hours over the limit
	r := &fakeReader{perArea: map[string][]float64{
		"d1": ones(10, 1), "d2": ones(10, 1), "d3": ones(10, 1), "d4": ones(10, 1), "d5": bad,
	}}
	// The pool is 45/50 = 0.9, over the 0.8 objective.
	res, err := EvaluateOutcome(context.Background(), r, outcomeDef(), outcomeNow)
	if err != nil {
		t.Fatal(err)
	}
	if res.Equity.Pooled < 0.8 {
		t.Fatalf("pooled = %v: the control needs it passing", res.Equity.Pooled)
	}
	if res.Status != outcome.StatusBreached || res.Areas[4].Status != outcome.StatusBreached || res.Equity.WorstArea != "d5" {
		t.Fatalf("status %s, d5 %s, worst %s", res.Status, res.Areas[4].Status, res.Equity.WorstArea)
	}
}

func TestEvaluateOutcomeAsksForExactlyTheSpanPerArea(t *testing.T) {
	r := &fakeReader{perArea: map[string][]float64{}}
	if _, err := EvaluateOutcome(context.Background(), r, outcomeDef(), outcomeNow); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 5 {
		t.Fatalf("%d reads, want one per area", len(r.calls))
	}
	for i, c := range r.calls {
		if *c.AnchorType != "area" || *c.AnchorToken != outcomeDef().Areas[i] || *c.Name != "pm25" || c.IntervalSeconds != 3600 {
			t.Fatalf("call %d = %+v", i, c)
		}
		if want := time.Date(2026, 11, 7, 2, 0, 0, 0, time.UTC); !c.StartTime.Equal(want) {
			t.Fatalf("start = %s", c.StartTime)
		}
		// Closed range: it must stop before the still-filling 12:00 bucket opens.
		if !c.EndTime.Before(time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)) {
			t.Fatalf("end = %s", c.EndTime)
		}
	}
}

func TestEvaluateOutcomeFailsWholeOnAReadError(t *testing.T) {
	boom := errors.New("store down")
	r := &fakeReader{fail: map[string]error{"d3": boom}}
	if _, err := EvaluateOutcome(context.Background(), r, outcomeDef(), outcomeNow); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}

func TestEvaluateOutcomeSilenceIsNoDataNotAPass(t *testing.T) {
	res, err := EvaluateOutcome(context.Background(), &fakeReader{}, outcomeDef(), outcomeNow)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != outcome.StatusNoData {
		t.Fatalf("status = %s", res.Status)
	}
}

func TestEvaluateOutcomeRefusesAnInvalidDefinitionBeforeReading(t *testing.T) {
	r := &fakeReader{}
	d := outcomeDef()
	d.Objective = 1
	if _, err := EvaluateOutcome(context.Background(), r, d, outcomeNow); err == nil {
		t.Fatal("accepted")
	}
	if len(r.calls) != 0 {
		t.Fatal("read before refusing")
	}
}

func TestEvaluateOutcomeIgnoresAnotherMeasurementsBuckets(t *testing.T) {
	r := &foreignReader{}
	d := outcomeDef()
	d.Areas = []string{"d1"}
	res, err := EvaluateOutcome(context.Background(), r, d, outcomeNow)
	if err != nil {
		t.Fatal(err)
	}
	if res.Areas[0].Status != outcome.StatusNoData {
		t.Fatalf("area = %+v", res.Areas[0])
	}
}

type foreignReader struct{}

func (foreignReader) BucketedMeasurements(_ context.Context, c MeasurementAggregationCriteria) ([]MeasurementBucket, error) {
	return []MeasurementBucket{{BucketStart: *c.StartTime, Name: "temperature", Max: sql.NullFloat64{Float64: 1, Valid: true}, Count: 3}}, nil
}

// While the experiment is off the evaluation is refused and the store is never touched.
func TestEvaluateOutcomeIsRefusedWhileOff(t *testing.T) {
	api := &Api{} // OutcomesEnabled is false by default; a read here would dereference a nil RDB.
	_, err := api.EvaluateOutcome(context.Background(), outcomeDef(), outcomeNow)
	var off OutcomesUnavailable
	if !errors.As(err, &off) {
		t.Fatalf("err = %v", err)
	}
	if off.Extensions()["code"] != CodeUnavailable {
		t.Fatalf("code = %v", off.Extensions())
	}
}

func TestOutcomesAreOffByDefault(t *testing.T) {
	if (Api{}).OutcomesEnabled {
		t.Fatal("the experiment must default to off")
	}
}
