// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/devicechain-io/dc-microservice/calcfield"
	"github.com/devicechain-io/dc-microservice/integrity"
	"github.com/devicechain-io/dc-microservice/limit"
)

// Draft-formula history preview (ADR-082, technical spec §4a): evaluate a DRAFT calculated
// metric's formula over a device's stored readings, for the author of the formula and nobody
// else. It is a read: nothing it computes is written, streamed or handed to detection, and it
// sees only what the caller can already read.
//
// 🔑 THE FORMULA TRAVELS IN THE REQUEST. event-management holds no profiles, and its one client
// of device-management is a service-token background sweep; using that on a user's read would be
// a confused deputy. So the console sends the draft's formula and the types of the inputs it
// reads, and this compiles them with core/calcfield under the same static cost gate the
// resolver's formulas pass. A caller who sends some other formula learns nothing more than the
// readings they could already list.
//
// 🔴 WHAT A RESULT IS, AND IS NOT. Inputs are read back from storage, which keeps a reading as
// decimal(20,8) (calcfield.StoredDecimals places): a result is what the formula gives for the
// STORED inputs. The value the resolver stores is computed once, at resolve, from the device's
// own full-precision string — the two differ wherever an input had more than eight decimal
// places (V·I with a current of 0.000000015 A computes 0 here). So a result is labelled as
// previewed from stored inputs, and never called the value that will be stored.

// MaxCalculatedSamples is the most samples one calculatedEvents read evaluates. A range that
// holds more is refused with LIMIT_EXCEEDED, never truncated: the console narrows the range.
// It matches MaxMeasurementRows; about 14 hours of a 1 Hz device, or 6 days at 0.1 Hz.
const MaxCalculatedSamples = 50_000

// calculatedSampleCap is the cap CalculatedEvents applies: MaxCalculatedSamples, lowered only by
// this package's tests to reach the refusal without writing fifty thousand rows.
var calculatedSampleCap = MaxCalculatedSamples

// MaxCalculatedSpan is the longest range one calculatedEvents read covers, the console's
// longest window. It is checked before any query runs: the read's cost is the device's rows
// in range, and without a span bound a request naming years would have the database read
// and sort a device's whole history before the sample cap could refuse it.
const MaxCalculatedSpan = 7 * 24 * time.Hour

// MaxCalculatedInputValues is the most input values one read returns, across all samples. The
// sample cap alone does not bound the response, which carries each sample's inputs.
const MaxCalculatedInputValues = 250_000

// calculatedInputValueCap is the cap CalculatedEvents applies: MaxCalculatedInputValues,
// lowered only by this package's tests.
var calculatedInputValueCap = MaxCalculatedInputValues

// CalculatedInput is one input a draft formula reads: a measured metric's key and data type.
type CalculatedInput struct {
	Key      string
	DataType string
}

// CalculatedConstant is one per-device constant a draft formula reads as `device.name`: the
// number the device's metadata holds, as text. The caller (the console) reads it from the device
// it is previewing; this service holds no device records.
type CalculatedConstant struct {
	Key   string
	Value string
}

// CalculatedEventsRequest is a draft formula and the device and range to evaluate it over.
type CalculatedEventsRequest struct {
	DeviceToken string
	Expression  string
	DataType    string
	Inputs      []CalculatedInput
	MinValue    *float64
	MaxValue    *float64
	// Constants are the device's current values for the constants the formula reads, at most
	// calcfield.MaxProfileConstants. One the formula reads and this omits is absent (the samples
	// are skipped as missing_input), one it does not read is ignored. Every sample is evaluated
	// with the same constants: they are the device's current ones, not the ones its history was
	// recorded under.
	Constants []CalculatedConstant
	From      time.Time
	To        time.Time
}

// CalculatedInputValue is one input of one sample, as the formula read it: the stored value
// rendered by calcfield.FormatStored.
type CalculatedInputValue struct {
	Key   string
	Value string
}

// CalculatedSample is one evaluated sample: the readings of one event that share one instant.
// Exactly one of Value or SkippedReason is set.
type CalculatedSample struct {
	OccurredTime  time.Time
	EventId       []byte
	Inputs        []CalculatedInputValue
	Value         *string
	SkippedReason *string
}

// CalculatedSkipCount is how many samples a formula skipped for one reason.
type CalculatedSkipCount struct {
	Reason string
	Count  int
}

// CalculatedEventsResult is every evaluated sample in the range, oldest first, and the tally:
// Computed samples produced a value, and Skipped counts the rest by reason (non-zero reasons
// only, in calcfield.SkipReasons order), so a gap in the series is never mistaken for data.
type CalculatedEventsResult struct {
	Samples  []CalculatedSample
	Computed int
	Skipped  []CalculatedSkipCount
}

// compileDraftFormula builds the environment of the request's inputs and compiles its formula,
// refusing with the author's message what the resolver would never run.
func compileDraftFormula(req CalculatedEventsRequest) (*calcfield.Formula, *calcfield.Env, map[string]calcfield.Type, error) {
	invalid := func(format string, args ...any) error {
		return integrity.NewRefusal(integrity.ClassInvalid, fmt.Sprintf(format, args...))
	}
	if req.DeviceToken == "" {
		return nil, nil, nil, invalid("deviceToken is required")
	}
	if !req.To.After(req.From) {
		return nil, nil, nil, invalid("to must be after from")
	}
	if span := req.To.Sub(req.From); span > MaxCalculatedSpan {
		// Whole hours, rounded up, so a span just over the cap never reports as the cap.
		return nil, nil, nil, limit.Exceeded("rangeHours", int((span+time.Hour-1)/time.Hour), int(MaxCalculatedSpan/time.Hour))
	}
	if len(req.Inputs) == 0 {
		return nil, nil, nil, invalid("inputs must name at least one metric the formula reads")
	}
	if len(req.Inputs) > calcfield.MaxInputs {
		return nil, nil, nil, limit.Exceeded("inputs", len(req.Inputs), calcfield.MaxInputs)
	}
	if len(req.Constants) > calcfield.MaxProfileConstants {
		return nil, nil, nil, limit.Exceeded("constants", len(req.Constants), calcfield.MaxProfileConstants)
	}
	resultType, ok := calcfield.ParseType(req.DataType)
	if !ok {
		return nil, nil, nil, invalid("dataType %q cannot be calculated; use DOUBLE, INT or BOOLEAN", req.DataType)
	}
	inputs := make([]calcfield.Input, 0, len(req.Inputs))
	types := make(map[string]calcfield.Type, len(req.Inputs))
	for _, in := range req.Inputs {
		t, ok := calcfield.ParseType(in.DataType)
		if !ok {
			return nil, nil, nil, invalid("input %q: dataType %q is not DOUBLE, INT or BOOLEAN", in.Key, in.DataType)
		}
		inputs = append(inputs, calcfield.Input{Key: in.Key, Type: t})
		types[in.Key] = t
	}
	env, err := calcfield.NewEnv(inputs)
	if err != nil {
		return nil, nil, nil, invalid("%s", err.Error())
	}
	if req.MinValue != nil && req.MaxValue != nil && *req.MinValue > *req.MaxValue {
		return nil, nil, nil, invalid("minValue %v is above maxValue %v", *req.MinValue, *req.MaxValue)
	}
	f, err := env.Compile(calcfield.Definition{Expression: req.Expression, Type: resultType,
		Min: req.MinValue, Max: req.MaxValue})
	if err != nil {
		var ce *calcfield.CompileError
		var cost *calcfield.CostError
		switch {
		case errors.As(err, &ce):
			return nil, nil, nil, invalid("the formula does not compile: %v", ce.Err)
		case errors.As(err, &cost):
			return nil, nil, nil, invalid("the formula's estimated cost %d is above the ceiling of %d", cost.EstimatedMax, cost.Ceiling)
		}
		return nil, nil, nil, invalid("%s", err.Error())
	}
	// Only the inputs the formula reads are fetched and returned: an input it declares but
	// never reads costs rows and response size and changes no value.
	read := make(map[string]calcfield.Type, len(types))
	for _, k := range f.InputKeys() {
		read[k] = types[k]
	}
	if len(read) == 0 {
		return nil, nil, nil, invalid("the formula reads none of its inputs, so there are no stored readings to evaluate it over")
	}
	return f, env, read, nil
}

// storedInputRow is one stored reading of an input.
type storedInputRow struct {
	EventId      []byte
	OccurredTime time.Time
	Name         string
	Value        sql.NullFloat64
}

// CalculatedEvents evaluates a draft formula over every stored sample of one device in
// [From, To] and returns each sample's inputs and outcome. A sample is the readings of the
// inputs the formula READS that share one event and one instant — (event_id, occurred_time), the same
// sample the resolver evaluates — so a batch of readings taken at different times is several
// samples, as it was at resolve. An event that carries none of the formula's inputs is no
// sample at all, so missing_input counts a sample that holds some of them and lacks another,
// never an event that holds only declared inputs the formula does not read.
//
// It reads through DB(ctx) and the MeasurementEvent model, so the tenant predicate and a scoped
// caller's scope rule apply as they do to measurementEvents: a sample is wholly visible or
// wholly invisible, since scope is per event.
//
// The read is bounded three ways, and refuses rather than truncates at each: the range is at
// most MaxCalculatedSpan, checked before the query; the cap+1'th sample, and the input value
// past MaxCalculatedInputValues, stop it with LIMIT_EXCEEDED as it streams. The span is the
// bound on DATABASE cost: the input names lead the only index that fits, so the planner may
// sort every row of the device in range before the first one streams, and stopping early
// then saves only the evaluation and the response.
func (api *Api) CalculatedEvents(ctx context.Context, req CalculatedEventsRequest) (*CalculatedEventsResult, error) {
	formula, env, types, err := compileDraftFormula(req)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(types))
	for k := range types {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	// Ordered by sample, then name and row identity, so a sample's rows are adjacent and a key
	// a sample holds twice (two readings of one batch at one instant) resolves the same way on
	// every read: the first row by payload id.
	rows, err := api.RDB.DB(ctx).Model(&MeasurementEvent{}).
		Select("event_id, occurred_time, name, value").
		Where("device_token = ? AND name IN ? AND occurred_time >= ? AND occurred_time <= ?",
			req.DeviceToken, keys, req.From, req.To).
		Order("occurred_time ASC, event_id ASC, name ASC, payload_id ASC").
		Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	constants, err := formulaConstants(formula, req.Constants)
	if err != nil {
		return nil, err
	}
	ev := newSampleEvaluator(formula, env, types, constants)
	defer ev.release()
	var cur *storedInputRow
	for rows.Next() {
		var row storedInputRow
		if err := rows.Scan(&row.EventId, &row.OccurredTime, &row.Name, &row.Value); err != nil {
			return nil, err
		}
		if cur == nil || !row.OccurredTime.Equal(cur.OccurredTime) || !bytes.Equal(row.EventId, cur.EventId) {
			if cur != nil {
				ev.finish()
			}
			if len(ev.result.Samples) >= calculatedSampleCap {
				return nil, limit.Exceeded("samples", calculatedSampleCap+1, calculatedSampleCap)
			}
			ev.begin(row.EventId, row.OccurredTime)
			cur = &row
		}
		ev.add(row.Name, row.Value)
		if ev.values > calculatedInputValueCap {
			return nil, limit.Exceeded("inputValues", ev.values, calculatedInputValueCap)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if cur != nil {
		ev.finish()
	}
	return ev.done(), nil
}

// sampleEvaluator evaluates a formula over samples given one stored row at a time: begin a
// sample, add its rows, finish it. It is the whole evaluation, apart from the read, so it is
// tested on literal rows without a database.
type sampleEvaluator struct {
	formula *calcfield.Formula
	types   map[string]calcfield.Type
	sample  *calcfield.Sample
	skipped map[calcfield.SkipReason]int
	result  CalculatedEventsResult
	open    *CalculatedSample
	values  int // input values recorded across every sample
	// constants are the device's, shared by every sample of the read.
	constants []calcfield.Constant
}

func newSampleEvaluator(f *calcfield.Formula, env *calcfield.Env, types map[string]calcfield.Type,
	constants []calcfield.Constant) *sampleEvaluator {
	return &sampleEvaluator{formula: f, types: types, sample: env.Sample(), constants: constants,
		skipped: map[calcfield.SkipReason]int{}, result: CalculatedEventsResult{Samples: []CalculatedSample{}}}
}

// formulaConstants is the request's constants the formula reads, parsed once as DOUBLEs: text
// that is not a number is an unusable constant (invalid_input), never a zero. A name given twice
// is refused rather than resolved by order.
func formulaConstants(f *calcfield.Formula, given []CalculatedConstant) ([]calcfield.Constant, error) {
	read := f.ConstantKeys()
	var out []calcfield.Constant
	seen := make(map[string]bool, len(given))
	for _, c := range given {
		if seen[c.Key] {
			return nil, integrity.NewRefusal(integrity.ClassInvalid, fmt.Sprintf("constants name %q more than once", c.Key))
		}
		seen[c.Key] = true
		if slices.Contains(read, c.Key) {
			out = append(out, calcfield.ConstantFromString(c.Key, c.Value))
		}
	}
	return out, nil
}

func (e *sampleEvaluator) begin(eventId []byte, at time.Time) {
	e.sample.Reset()
	e.sample.UseConstants(e.constants)
	e.open = &CalculatedSample{OccurredTime: at.UTC(), EventId: eventId, Inputs: []CalculatedInputValue{}}
}

// add records one stored reading. A key already set in the sample keeps its first value; a
// row with no value (never written by the store, which refuses a non-numeric reading) is left
// absent, so a formula reading it skips as a missing input.
func (e *sampleEvaluator) add(name string, v sql.NullFloat64) {
	t, ok := e.types[name]
	if !ok || !v.Valid {
		return
	}
	for _, in := range e.open.Inputs {
		if in.Key == name {
			return
		}
	}
	raw := calcfield.FormatStored(t, v.Float64)
	_, _ = e.sample.Set(name, raw) // a value that does not parse is recorded on the sample
	e.open.Inputs = append(e.open.Inputs, CalculatedInputValue{Key: name, Value: raw})
	e.values++
}

func (e *sampleEvaluator) finish() {
	out := e.formula.Eval(e.sample)
	if out.OK() {
		v := out.Value
		e.open.Value = &v
		e.result.Computed++
	} else {
		r := string(out.Skip)
		e.open.SkippedReason = &r
		e.skipped[out.Skip]++
	}
	e.result.Samples = append(e.result.Samples, *e.open)
	e.open = nil
}

func (e *sampleEvaluator) done() *CalculatedEventsResult {
	e.result.Skipped = []CalculatedSkipCount{}
	for _, r := range calcfield.SkipReasons {
		if n := e.skipped[r]; n > 0 {
			e.result.Skipped = append(e.result.Skipped, CalculatedSkipCount{Reason: string(r), Count: n})
		}
	}
	return &e.result
}

func (e *sampleEvaluator) release() { e.sample.Release() }
