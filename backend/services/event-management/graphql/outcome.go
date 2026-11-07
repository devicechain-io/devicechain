// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/integrity"
	"github.com/devicechain-io/dc-microservice/outcome"
)

// EXPERIMENT: outcome objects. See core/outcome for what the figures mean.

// OutcomeDefinitionInput is OutcomeDefinitionInput.
type OutcomeDefinitionInput struct {
	Token           string
	Metric          string
	Aggregation     string
	Comparator      string
	Threshold       float64
	Areas           []string
	WindowSeconds   int32
	IntervalSeconds int32
	Objective       float64
	MinSamples      *int32
	MaxEquitySpread *float64
}

// toOutcomeDefinition converts the wire form. It does not validate: the evaluation does, once,
// so the wire and the library cannot disagree about what is acceptable.
func toOutcomeDefinition(in OutcomeDefinitionInput) outcome.Definition {
	def := outcome.Definition{
		Token:       in.Token,
		Metric:      in.Metric,
		Aggregation: outcome.Aggregation(in.Aggregation),
		Comparator:  outcome.Comparator(in.Comparator),
		Threshold:   in.Threshold,
		Areas:       in.Areas,
		Window:      time.Duration(in.WindowSeconds) * time.Second,
		Interval:    time.Duration(in.IntervalSeconds) * time.Second,
		Objective:   in.Objective,
	}
	if in.MinSamples != nil {
		def.MinSamples = int64(*in.MinSamples)
	}
	if in.MaxEquitySpread != nil {
		def.MaxEquitySpread = *in.MaxEquitySpread
	}
	return def
}

// EvaluateOutcome judges an outcome. Gated on EventRead like the bucketed read it is built on;
// a scoped caller reads through the same scoped path, so sees only what that read shows them.
func (r *SchemaResolver) EvaluateOutcome(ctx context.Context, args struct {
	Definition OutcomeDefinitionInput
}) (*OutcomeEvaluationResolver, error) {
	if _, err := auth.AuthorizeScoped(ctx, auth.EventRead); err != nil {
		return nil, err
	}
	def := toOutcomeDefinition(args.Definition)
	// Refused ahead of everything: an input no range of data could make valid is the
	// caller's to fix, and says so before the experiment's own switch is consulted.
	if err := def.Validate(); err != nil {
		return nil, integrity.NewRefusal(integrity.ClassInvalid, err.Error())
	}
	res, err := r.GetApi(ctx).EvaluateOutcome(ctx, def, time.Now())
	if err != nil {
		return nil, err
	}
	return &OutcomeEvaluationResolver{token: def.Token, m: res}, nil
}

// OutcomeEvaluationResolver resolves OutcomeEvaluation.
type OutcomeEvaluationResolver struct {
	token string
	m     outcome.Result
}

func (r *OutcomeEvaluationResolver) Token() string           { return r.token }
func (r *OutcomeEvaluationResolver) Status() string          { return string(r.m.Status) }
func (r *OutcomeEvaluationResolver) ObjectiveStatus() string { return string(r.m.ObjectiveStatus) }
func (r *OutcomeEvaluationResolver) StrayBuckets() int32     { return int32(r.m.Stray) }

func (r *OutcomeEvaluationResolver) Span() *OutcomeSpanResolver {
	return &OutcomeSpanResolver{m: r.m.Span}
}

func (r *OutcomeEvaluationResolver) Areas() []*OutcomeAreaStatusResolver {
	out := make([]*OutcomeAreaStatusResolver, len(r.m.Areas))
	for i := range r.m.Areas {
		out[i] = &OutcomeAreaStatusResolver{m: r.m.Areas[i]}
	}
	return out
}

func (r *OutcomeEvaluationResolver) Equity() *OutcomeEquityResolver {
	return &OutcomeEquityResolver{m: r.m.Equity}
}

// OutcomeSpanResolver resolves OutcomeSpan.
type OutcomeSpanResolver struct{ m outcome.Span }

func (r *OutcomeSpanResolver) Start() string { return r.m.Start().UTC().Format(time.RFC3339) }
func (r *OutcomeSpanResolver) End() string   { return r.m.End().UTC().Format(time.RFC3339) }
func (r *OutcomeSpanResolver) IntervalSeconds() int32 {
	return int32(r.m.Interval / time.Second)
}
func (r *OutcomeSpanResolver) BucketCount() int32 { return int32(r.m.Count) }

// OutcomeAreaStatusResolver resolves OutcomeAreaStatus.
type OutcomeAreaStatusResolver struct{ m outcome.AreaResult }

func (r *OutcomeAreaStatusResolver) Area() string             { return r.m.Area }
func (r *OutcomeAreaStatusResolver) Status() string           { return string(r.m.Status) }
func (r *OutcomeAreaStatusResolver) GoodBuckets() int32       { return int32(r.m.Good) }
func (r *OutcomeAreaStatusResolver) BadBuckets() int32        { return int32(r.m.Bad) }
func (r *OutcomeAreaStatusResolver) EmptyBuckets() int32      { return int32(r.m.Empty) }
func (r *OutcomeAreaStatusResolver) TotalBuckets() int32      { return int32(r.m.Total) }
func (r *OutcomeAreaStatusResolver) Compliance() float64      { return r.m.Compliance }
func (r *OutcomeAreaStatusResolver) Coverage() float64        { return r.m.Coverage }
func (r *OutcomeAreaStatusResolver) BudgetRemaining() float64 { return r.m.BudgetRemaining }

// OutcomeEquityResolver resolves OutcomeEquity.
type OutcomeEquityResolver struct{ m outcome.Equity }

func (r *OutcomeEquityResolver) EvaluatedAreas() int32 { return int32(r.m.Evaluated) }
func (r *OutcomeEquityResolver) ExcludedAreas() int32  { return int32(r.m.Excluded) }
func (r *OutcomeEquityResolver) SpreadUndefined() bool { return !r.m.Computed }
func (r *OutcomeEquityResolver) Complete() bool        { return r.m.Complete }
func (r *OutcomeEquityResolver) Pooled() float64       { return r.m.Pooled }
func (r *OutcomeEquityResolver) Breached() bool        { return r.m.Breached }

func (r *OutcomeEquityResolver) Worst() *float64  { return r.ifComputed(r.m.Worst) }
func (r *OutcomeEquityResolver) Median() *float64 { return r.ifComputed(r.m.Median) }
func (r *OutcomeEquityResolver) Spread() *float64 { return r.ifComputed(r.m.Spread) }

func (r *OutcomeEquityResolver) WorstArea() *string {
	if !r.m.Computed {
		return nil
	}
	return &r.m.WorstArea
}

func (r *OutcomeEquityResolver) ifComputed(v float64) *float64 {
	if !r.m.Computed {
		return nil
	}
	return &v
}
