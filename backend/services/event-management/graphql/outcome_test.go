// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/outcome"
	gqlerrors "github.com/graph-gophers/graphql-go/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const evalDoc = `query($d: OutcomeDefinitionInput!) { evaluateOutcome(definition: $d) { token status } }`

func evalVars(mut func(map[string]any)) map[string]any {
	d := map[string]any{
		"token": "aq", "metric": "pm25", "aggregation": "AVG", "comparator": "LTE", "threshold": 15.0,
		"areas": []any{"d1", "d2"}, "windowSeconds": 3600, "intervalSeconds": 300, "objective": 0.8,
	}
	if mut != nil {
		mut(d)
	}
	return map[string]any{"d": d}
}

func firstCode(t *testing.T, errs []*gqlerrors.QueryError) string {
	t.Helper()
	require.NotEmpty(t, errs)
	code, _ := errs[0].Extensions["code"].(string)
	return code
}

// While the experiment is off the query is refused as UNAVAILABLE. The Api has no store behind
// it, so a read would panic: the refusal is proven to come before any measurement is touched.
func TestEvaluateOutcomeIsUnavailableWhileOff(t *testing.T) {
	api, _ := newBackfillApi(t)
	resp := execAs(t, api, "acme", nil, []string{string(auth.EventRead)}, evalDoc, evalVars(nil))
	assert.Equal(t, "UNAVAILABLE", firstCode(t, resp.Errors))
	assert.Equal(t, "null", string(resp.Data))
}

func TestEvaluateOutcomeNeedsEventRead(t *testing.T) {
	api, _ := newBackfillApi(t)
	api.OutcomesEnabled = true
	resp := execAs(t, api, "acme", nil, []string{string(auth.DeviceRead)}, evalDoc, evalVars(nil))
	require.NotEmpty(t, resp.Errors)
	assert.NotEqual(t, "UNAVAILABLE", firstCode(t, resp.Errors))
}

func TestEvaluateOutcomeRefusesAnInvalidDefinition(t *testing.T) {
	api, _ := newBackfillApi(t)
	api.OutcomesEnabled = true
	for name, mut := range map[string]func(map[string]any){
		"objective one":    func(d map[string]any) { d["objective"] = 1.0 },
		"no areas":         func(d map[string]any) { d["areas"] = []any{} },
		"bad comparator":   func(d map[string]any) { d["comparator"] = "EQ" },
		"interval off day": func(d map[string]any) { d["intervalSeconds"] = 420 },
		"too many buckets": func(d map[string]any) { d["windowSeconds"] = 31 * 86400; d["intervalSeconds"] = 60 },
	} {
		resp := execAs(t, api, "acme", nil, []string{string(auth.EventRead)}, evalDoc, evalVars(mut))
		require.NotEmpty(t, resp.Errors, name)
		assert.Contains(t, resp.Errors[0].Message, "outcome:", name)
	}
}

// The experiment off beats nothing: an invalid definition is still the caller's to fix, and a
// valid one is refused for the instance's state.
func TestEvaluateOutcomeFlagOffStillAnswersValidationFirst(t *testing.T) {
	api, _ := newBackfillApi(t)
	resp := execAs(t, api, "acme", nil, []string{string(auth.EventRead)}, evalDoc, evalVars(func(d map[string]any) { d["objective"] = 2.0 }))
	require.NotEmpty(t, resp.Errors)
	assert.Contains(t, resp.Errors[0].Message, "outcome:")
}

func TestUndefinedSpreadIsNullOnTheWire(t *testing.T) {
	r := &OutcomeEquityResolver{m: outcome.Equity{Computed: false, Pooled: 0.5}}
	assert.Nil(t, r.Worst())
	assert.Nil(t, r.Median())
	assert.Nil(t, r.Spread())
	assert.Nil(t, r.WorstArea())
	assert.True(t, r.SpreadUndefined())

	r = &OutcomeEquityResolver{m: outcome.Equity{Computed: true, Worst: 0.25, Median: 0.75, Spread: 0.5, WorstArea: "d4"}}
	require.NotNil(t, r.Worst())
	assert.Equal(t, 0.25, *r.Worst())
	assert.Equal(t, "d4", *r.WorstArea())
	assert.False(t, r.SpreadUndefined())
}

func TestSpanAndAreaMapping(t *testing.T) {
	sp := outcome.Span{First: time.Date(2026, 11, 7, 11, 0, 0, 0, time.UTC), Interval: 5 * time.Minute, Count: 12}
	r := &OutcomeSpanResolver{m: sp}
	assert.Equal(t, "2026-11-07T11:00:00Z", r.Start())
	assert.Equal(t, "2026-11-07T12:00:00Z", r.End())
	assert.Equal(t, int32(300), r.IntervalSeconds())
	assert.Equal(t, int32(12), r.BucketCount())
}

func TestToOutcomeDefinitionMapsOptionalFields(t *testing.T) {
	ms, spread := int32(3), 0.2
	d := toOutcomeDefinition(OutcomeDefinitionInput{WindowSeconds: 3600, IntervalSeconds: 300, MinSamples: &ms, MaxEquitySpread: &spread})
	assert.Equal(t, time.Hour, d.Window)
	assert.Equal(t, 5*time.Minute, d.Interval)
	assert.Equal(t, int64(3), d.MinSamples)
	assert.Equal(t, 0.2, d.MaxEquitySpread)
	d = toOutcomeDefinition(OutcomeDefinitionInput{})
	assert.Zero(t, d.MinSamples)
	assert.Zero(t, d.MaxEquitySpread)
}
