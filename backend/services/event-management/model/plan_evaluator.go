// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"database/sql"
	"fmt"

	"github.com/devicechain-io/dc-microservice/calcfield"
)

// PlanEvaluator evaluates a calcfield.Plan over samples of STORED readings, one row at a time:
// Begin a sample, Add its rows, Eval it. It is the evaluation apart from the read, shared by the
// draft-formula preview (a one-step plan) and by calculated-metric backfill (a profile's plan),
// and it is tested on literal rows without a database.
//
// 🔑 A SAMPLE IS THE LIVE SAMPLE. The resolver's sample is every measurement of one message that
// shares an exact instant; the stored form of that is the rows of one event at one
// occurred_time, which is how both callers group before they Add. The evaluator does not group:
// it is told where a sample starts.
//
// It evaluates the closure of the keys it was built for (calcfield.Plan.Closure), in plan order,
// each step through Plan.Eval exactly as the resolver's Calculation.Append runs it, so a
// chained metric reads the value its dependency produced (as the declared type, the string the
// live event carries) and a dependency that produced nothing is absent to it. The
// differences from live are the INPUTS, which are read back from storage (rounded to
// calcfield.StoredDecimals places, not the device's string) and the constants, which are the
// device's current ones.
type PlanEvaluator struct {
	plan      *calcfield.Plan
	types     map[string]calcfield.Type // the measured inputs a row may set
	steps     []int                     // plan step indices to evaluate, in plan order
	keys      []string                  // the key of each of steps
	sample    *calcfield.Sample
	constants []calcfield.Constant
	seen      []string // names set in the open sample
	out       []StepOutcome
}

// StepOutcome is what one evaluated calculated metric produced for one sample: a Value, or a
// Skip (with Err when there is an underlying error). The slice Eval returns is reused.
type StepOutcome struct {
	Key   string
	OK    bool
	Value string
	Skip  calcfield.SkipReason
	Err   error
	// NotApplicable is a step the sample carries none of the inputs of (Plan.Applicable): it
	// was not evaluated and is NOT a skip - OK is false and Skip is empty. Callers count it
	// nowhere, exactly as live resolution reports nothing for it.
	NotApplicable bool
}

// NewPlanEvaluator returns an evaluator of the closure of keys over plan. The error is
// Plan.Closure's: a key the plan lacks, or one that (or whose dependency) cannot run.
func NewPlanEvaluator(plan *calcfield.Plan, keys []string, constants []calcfield.Constant) (*PlanEvaluator, error) {
	closure, err := plan.Closure(keys)
	if err != nil {
		return nil, err
	}
	env := plan.Env()
	types := make(map[string]calcfield.Type)
	for _, in := range env.Inputs() {
		if !env.IsCalculated(in.Key) {
			types[in.Key] = in.Type
		}
	}
	return newPlanEvaluator(plan, closure, types, constants)
}

// newPlanEvaluator is NewPlanEvaluator over a closure already computed, restricting the rows
// that may be Added to types.
func newPlanEvaluator(plan *calcfield.Plan, closure []string, types map[string]calcfield.Type,
	constants []calcfield.Constant) (*PlanEvaluator, error) {
	e := &PlanEvaluator{plan: plan, types: types, constants: constants, sample: plan.Env().Sample(),
		keys: closure, steps: make([]int, len(closure)), out: make([]StepOutcome, len(closure))}
	for i, k := range closure {
		idx, ok := plan.StepIndex(k)
		if !ok {
			e.sample.Release()
			return nil, fmt.Errorf("calculated metric %q is not in the plan", k)
		}
		e.steps[i] = idx
	}
	return e, nil
}

// Begin starts a new sample, discarding the previous one's values.
func (e *PlanEvaluator) Begin() {
	e.sample.Reset()
	e.sample.UseConstants(e.constants)
	e.seen = e.seen[:0]
}

// Add records one stored reading into the open sample. It reports the value as the formula
// reads it (calcfield.FormatStored) and true when the reading was taken. A name the plan does
// not read as a measured input, a row with no value (never written by the store, which refuses
// a non-numeric reading) and a name the sample already holds are not taken: a name a sample
// holds twice keeps its first row, the caller's order. A value that does not parse is recorded
// on the sample, so the formulas that read it skip as invalid_input.
func (e *PlanEvaluator) Add(name string, v sql.NullFloat64) (string, bool) {
	t, ok := e.types[name]
	if !ok || !v.Valid {
		return "", false
	}
	for _, n := range e.seen {
		if n == name {
			return "", false
		}
	}
	raw := calcfield.FormatStored(t, v.Float64)
	_, _ = e.sample.Set(name, raw)
	e.seen = append(e.seen, name)
	return raw, true
}

// Eval evaluates the closure against the open sample, in order, and returns one outcome per
// step. The slice is the evaluator's own and is overwritten by the next Eval.
func (e *PlanEvaluator) Eval() []StepOutcome {
	for i, idx := range e.steps {
		if !e.plan.Applicable(e.sample, idx) {
			e.out[i] = StepOutcome{Key: e.keys[i], NotApplicable: true}
			continue
		}
		r := e.plan.Eval(e.sample, idx)
		e.out[i] = StepOutcome{Key: e.keys[i], OK: r.OK(), Value: r.Value, Skip: r.Skip, Err: r.Err}
	}
	return e.out
}

// Release returns the evaluator's sample to its pool; the evaluator must not be used after.
func (e *PlanEvaluator) Release() { e.sample.Release() }
