// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"time"

	"github.com/devicechain-io/dc-microservice/integrity"
	"github.com/devicechain-io/dc-microservice/outcome"
)

// EXPERIMENT: outcome objects. An outcome is evaluated here from the bucketed measurements the
// service already serves; nothing about an outcome is stored, so there is no migration and
// nothing to roll back. The arithmetic lives in core/outcome; this file is only the adapter
// that fetches each area's buckets and hands them over.

// OutcomeEvaluationTimeout bounds one evaluation end to end. An evaluation is up to
// outcome.MaxAreas anchored reads, each computed from the individual readings, so it is the
// one read in this service that is priced by what it names and not by one cursor.
const OutcomeEvaluationTimeout = 30 * time.Second

// OutcomesUnavailable is the refusal served while the experiment is off. It answers
// UNAVAILABLE: the instance's state, not the request's.
type OutcomesUnavailable struct{}

func (OutcomesUnavailable) Error() string {
	return "outcome evaluation is an experimental feature and is not enabled on this instance"
}

// Extensions gives the refusal its wire code.
func (OutcomesUnavailable) Extensions() map[string]any {
	return map[string]any{"code": CodeUnavailable}
}

// BucketReader is the one read an evaluation needs; *Api satisfies it.
type BucketReader interface {
	BucketedMeasurements(ctx context.Context, criteria MeasurementAggregationCriteria) ([]MeasurementBucket, error)
}

// EvaluateOutcome judges an outcome at now, or refuses while the experiment is off. When it is
// off no measurement is read.
func (api *Api) EvaluateOutcome(ctx context.Context, def outcome.Definition, now time.Time) (outcome.Result, error) {
	if !api.OutcomesEnabled {
		return outcome.Result{}, OutcomesUnavailable{}
	}
	return EvaluateOutcome(ctx, api, def, now)
}

// EvaluateOutcome fetches each named area's buckets through reader and judges them.
//
// Areas are read one after another, never in parallel, so one evaluation holds one pooled
// connection at a time. A read that fails fails the whole evaluation: an area whose read errored
// is not an area with no data, and answering as though it were would turn an outage into a
// verdict.
func EvaluateOutcome(ctx context.Context, reader BucketReader, def outcome.Definition, now time.Time) (outcome.Result, error) {
	if err := def.Validate(); err != nil {
		return outcome.Result{}, integrity.NewRefusal(integrity.ClassInvalid, err.Error())
	}
	span, err := outcome.SpanFor(def, now)
	if err != nil {
		return outcome.Result{}, integrity.NewRefusal(integrity.ClassInvalid, err.Error())
	}
	ctx, cancel := context.WithTimeout(ctx, OutcomeEvaluationTimeout)
	defer cancel()

	// The range is closed at both ends, so the last instant of the last bucket is the one
	// before the next bucket opens; a reading exactly on that boundary belongs to the bucket
	// that is still filling.
	start, end := span.Start(), span.End().Add(-time.Microsecond)
	areaType := "area"
	metric := def.Metric
	inputs := make([]outcome.AreaInput, 0, len(def.Areas))
	for _, area := range def.Areas {
		token := area
		got, err := reader.BucketedMeasurements(ctx, MeasurementAggregationCriteria{
			Name:            &metric,
			StartTime:       &start,
			EndTime:         &end,
			AnchorType:      &areaType,
			AnchorToken:     &token,
			IntervalSeconds: int64(span.Interval / time.Second),
		})
		if err != nil {
			return outcome.Result{}, err
		}
		in := outcome.AreaInput{Area: area}
		for _, b := range got {
			if b.Name != def.Metric {
				continue
			}
			in.Buckets = append(in.Buckets, outcome.Bucket{
				Start: b.BucketStart.UTC(),
				Count: b.Count,
				Avg:   nullable(b.Avg.Valid, b.Avg.Float64),
				Min:   nullable(b.Min.Valid, b.Min.Float64),
				Max:   nullable(b.Max.Valid, b.Max.Float64),
			})
		}
		inputs = append(inputs, in)
	}
	res, err := outcome.Evaluate(def, now, inputs)
	if err != nil {
		// The store's answer broke a rule the evaluator holds (a misaligned or duplicated
		// bucket): that is a defect to surface, not a verdict to invent.
		return outcome.Result{}, err
	}
	return res, nil
}

func nullable(valid bool, v float64) *float64 {
	if !valid {
		return nil
	}
	return &v
}
