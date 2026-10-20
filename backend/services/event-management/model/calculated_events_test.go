// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/calcfield"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/eventtype"
	"github.com/devicechain-io/dc-microservice/integrity"
	"github.com/devicechain-io/dc-microservice/limit"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// The draft-formula history preview (calculatedEvents): sample grouping, the skip policy and
// tally, the refusals, and that it writes nothing.

var calcAt = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

func newCalculatedTestApi(t *testing.T) *Api {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, db.AutoMigrate(&MeasurementEvent{}))
	return NewApi(&rdb.RdbManager{Database: db})
}

// seedReading writes one stored reading.
func seedReading(t *testing.T, api *Api, tenant, device, event string, at time.Time, name string, v float64) {
	t.Helper()
	row := &MeasurementEvent{EventId: []byte(event), PayloadId: []byte(event + "/" + name + "/" + at.String()),
		DeviceToken: device, EventType: eventtype.Measurement, OccurredTime: at, Name: name,
		Value: sql.NullFloat64{Float64: v, Valid: true}}
	require.NoError(t, api.RDB.DB(core.WithTenant(context.Background(), tenant)).Create(row).Error)
}

func powerRequest() CalculatedEventsRequest {
	return CalculatedEventsRequest{
		DeviceToken: "meter-1", Expression: "voltage * current / 1000.0", DataType: "DOUBLE",
		Inputs: []CalculatedInput{{Key: "voltage", DataType: "DOUBLE"}, {Key: "current", DataType: "DOUBLE"}},
		From:   calcAt.Add(-time.Hour), To: calcAt.Add(time.Hour),
	}
}

func acme() context.Context { return core.WithTenant(context.Background(), "acme") }

// A sample is the readings of one event at one instant: two readings of one event at the same
// time are one sample, a batch's readings at two times are two, and readings the formula does
// not read are not inputs. Samples come back oldest first, each with what the formula read.
func TestCalculatedEventsGroupsSamplesByEventAndInstant(t *testing.T) {
	api := newCalculatedTestApi(t)
	seedReading(t, api, "acme", "meter-1", "ev1", calcAt, "voltage", 230)
	seedReading(t, api, "acme", "meter-1", "ev1", calcAt, "current", 10)
	seedReading(t, api, "acme", "meter-1", "ev1", calcAt, "temp", 99) // not an input
	// A batch: one event, two instants.
	seedReading(t, api, "acme", "meter-1", "ev2", calcAt.Add(time.Minute), "voltage", 231)
	seedReading(t, api, "acme", "meter-1", "ev2", calcAt.Add(time.Minute), "current", 2)
	seedReading(t, api, "acme", "meter-1", "ev2", calcAt.Add(2*time.Minute), "voltage", 232)
	// Another device, and another tenant's same device token: neither is read.
	seedReading(t, api, "acme", "meter-2", "ev3", calcAt, "voltage", 1)
	seedReading(t, api, "globex", "meter-1", "ev4", calcAt, "voltage", 1)
	seedReading(t, api, "globex", "meter-1", "ev4", calcAt, "current", 1)

	res, err := api.CalculatedEvents(acme(), powerRequest())
	require.NoError(t, err)
	require.Len(t, res.Samples, 3)

	first := res.Samples[0]
	assert.Equal(t, "ev1", string(first.EventId))
	assert.True(t, first.OccurredTime.Equal(calcAt))
	assert.ElementsMatch(t, []CalculatedInputValue{{"voltage", "230"}, {"current", "10"}}, first.Inputs)
	require.NotNil(t, first.Value)
	assert.Equal(t, "2.3", *first.Value)
	assert.Nil(t, first.SkippedReason)

	assert.Equal(t, "0.462", *res.Samples[1].Value)
	require.NotNil(t, res.Samples[2].SkippedReason)
	assert.Equal(t, "missing_input", *res.Samples[2].SkippedReason, "the batch's second instant has no current")
	assert.Nil(t, res.Samples[2].Value)

	assert.Equal(t, 2, res.Computed)
	assert.Equal(t, []CalculatedSkipCount{{Reason: "missing_input", Count: 1}}, res.Skipped)
}

// The full skip policy applies — range, division, a non-finite result, an INT input stored
// with a fraction — and every skip is tallied by reason, in the fixed reason order.
func TestCalculatedEventsAppliesTheSkipPolicy(t *testing.T) {
	api := newCalculatedTestApi(t)
	seedReading(t, api, "acme", "meter-1", "a", calcAt, "v", 10)
	seedReading(t, api, "acme", "meter-1", "a", calcAt, "n", 2)
	seedReading(t, api, "acme", "meter-1", "b", calcAt.Add(time.Second), "v", 10)
	seedReading(t, api, "acme", "meter-1", "b", calcAt.Add(time.Second), "n", 0) // v / 0
	seedReading(t, api, "acme", "meter-1", "c", calcAt.Add(2*time.Second), "v", 1000)
	seedReading(t, api, "acme", "meter-1", "c", calcAt.Add(2*time.Second), "n", 1) // above max
	seedReading(t, api, "acme", "meter-1", "d", calcAt.Add(3*time.Second), "v", 1)
	seedReading(t, api, "acme", "meter-1", "d", calcAt.Add(3*time.Second), "n", 1.5) // INT with a fraction

	ceiling := 100.0
	res, err := api.CalculatedEvents(acme(), CalculatedEventsRequest{
		DeviceToken: "meter-1", Expression: "v / double(n)", DataType: "DOUBLE", MaxValue: &ceiling,
		Inputs: []CalculatedInput{{Key: "v", DataType: "DOUBLE"}, {Key: "n", DataType: "INT"}},
		From:   calcAt, To: calcAt.Add(time.Minute),
	})
	require.NoError(t, err)
	got := make([]string, len(res.Samples))
	for i, s := range res.Samples {
		if s.Value != nil {
			got[i] = *s.Value
		} else {
			got[i] = "skip:" + *s.SkippedReason
		}
	}
	assert.Equal(t, []string{"5", "skip:non_finite", "skip:range", "skip:invalid_input"}, got)
	assert.Equal(t, 1, res.Computed)
	assert.Equal(t, []CalculatedSkipCount{{"invalid_input", 1}, {"non_finite", 1}, {"range", 1}}, res.Skipped)
	assert.Equal(t, "1.5", res.Samples[3].Inputs[0].Value, "the input is shown as the formula read it")
}

// More samples than the cap are refused, never truncated; at the cap they are all returned.
func TestCalculatedEventsRefusesAboveTheSampleCap(t *testing.T) {
	old := calculatedSampleCap
	calculatedSampleCap = 3
	t.Cleanup(func() { calculatedSampleCap = old })

	api := newCalculatedTestApi(t)
	for i, ev := range []string{"a", "b", "c"} {
		at := calcAt.Add(time.Duration(i) * time.Second)
		seedReading(t, api, "acme", "meter-1", ev, at, "voltage", 1)
		seedReading(t, api, "acme", "meter-1", ev, at, "current", 1)
	}
	res, err := api.CalculatedEvents(acme(), powerRequest())
	require.NoError(t, err)
	assert.Len(t, res.Samples, 3)

	seedReading(t, api, "acme", "meter-1", "d", calcAt.Add(3*time.Second), "voltage", 1)
	_, err = api.CalculatedEvents(acme(), powerRequest())
	var le *limit.Error
	require.True(t, errors.As(err, &le), "want LIMIT_EXCEEDED, got %v", err)
	assert.Equal(t, "samples", le.What)
	assert.Equal(t, 3, le.Max)
}

// A formula the resolver would never run is refused with the author's reason, before any read.
func TestCalculatedEventsRefusesWhatCannotRun(t *testing.T) {
	api := newCalculatedTestApi(t)
	for name, mutate := range map[string]func(*CalculatedEventsRequest){
		"no device":           func(r *CalculatedEventsRequest) { r.DeviceToken = "" },
		"backwards range":     func(r *CalculatedEventsRequest) { r.From, r.To = r.To, r.From },
		"empty range":         func(r *CalculatedEventsRequest) { r.To = r.From },
		"reads no input":      func(r *CalculatedEventsRequest) { r.Expression = "1.0" },
		"no inputs":           func(r *CalculatedEventsRequest) { r.Inputs = nil },
		"not a formula type":  func(r *CalculatedEventsRequest) { r.DataType = "STRING" },
		"input of no type":    func(r *CalculatedEventsRequest) { r.Inputs[0].DataType = "STRING" },
		"duplicate input":     func(r *CalculatedEventsRequest) { r.Inputs[1] = r.Inputs[0] },
		"does not type-check": func(r *CalculatedEventsRequest) { r.Expression = "voltage > 1.0" },
		"undeclared name":     func(r *CalculatedEventsRequest) { r.Expression = "humidity * 2.0" },
		"reads the clock":     func(r *CalculatedEventsRequest) { r.Expression = "double(now())" },
		"too long":            func(r *CalculatedEventsRequest) { r.Expression = "voltage" + repeatPlus(600) },
		"bounds reversed": func(r *CalculatedEventsRequest) {
			lo, hi := 10.0, 1.0
			r.MinValue, r.MaxValue = &lo, &hi
		},
	} {
		t.Run(name, func(t *testing.T) {
			req := powerRequest()
			mutate(&req)
			_, err := api.CalculatedEvents(acme(), req)
			var refusal *integrity.Refusal
			require.True(t, errors.As(err, &refusal), "want an invalid-request refusal, got %v", err)
		})
	}
}

func repeatPlus(n int) string {
	b := make([]byte, 0, n*4)
	for i := 0; i < n; i++ {
		b = append(b, " + 1.0"...)
	}
	return string(b)
}

// TestCalculatedEventsWritesNothing: the preview is a read. Every write a gorm statement can
// make — create, update, delete, a raw statement — is counted while it runs, and none is made;
// the stored readings are as they were.
func TestCalculatedEventsWritesNothing(t *testing.T) {
	api := newCalculatedTestApi(t)
	seedReading(t, api, "acme", "meter-1", "ev1", calcAt, "voltage", 230)
	seedReading(t, api, "acme", "meter-1", "ev1", calcAt, "current", 10)

	writes := 0
	count := func(*gorm.DB) { writes++ }
	cb := api.RDB.Database.Callback()
	require.NoError(t, cb.Create().Before("gorm:create").Register("test:count_create", count))
	require.NoError(t, cb.Update().Before("gorm:update").Register("test:count_update", count))
	require.NoError(t, cb.Delete().Before("gorm:delete").Register("test:count_delete", count))
	require.NoError(t, cb.Raw().Before("gorm:raw").Register("test:count_raw", count))

	// The negative control: the counter sees a write when one is made.
	seedReading(t, api, "acme", "meter-1", "control", calcAt.Add(-2*time.Hour), "voltage", 1)
	require.Equal(t, 1, writes, "the write counter does not see writes")
	writes = 0

	res, err := api.CalculatedEvents(acme(), powerRequest())
	require.NoError(t, err)
	require.Len(t, res.Samples, 1)
	assert.Zero(t, writes, "calculatedEvents made a write")

	var stored []MeasurementEvent
	require.NoError(t, api.RDB.DB(acme()).Order("name").Find(&stored).Error)
	require.Len(t, stored, 3)
	for _, row := range stored {
		assert.NotEqual(t, "power", row.Name)
	}
}

// The evaluation, on literal rows: a key a sample holds twice keeps its first value, and a row
// with no value is left absent.
func TestSampleEvaluatorOnLiteralRows(t *testing.T) {
	f, env, types, err := compileDraftFormula(CalculatedEventsRequest{DeviceToken: "d", Expression: "a + b",
		DataType: "INT", Inputs: []CalculatedInput{{"a", "INT"}, {"b", "INT"}},
		From: calcAt, To: calcAt.Add(time.Second)})
	require.NoError(t, err)
	ev := newSampleEvaluator(f, env, types, nil)
	defer ev.release()
	ev.begin([]byte("e1"), calcAt)
	ev.add("a", sql.NullFloat64{Float64: 1, Valid: true})
	ev.add("a", sql.NullFloat64{Float64: 100, Valid: true})
	ev.add("b", sql.NullFloat64{Float64: 2, Valid: true})
	ev.finish()
	ev.begin([]byte("e2"), calcAt)
	ev.add("a", sql.NullFloat64{Float64: 1, Valid: true})
	ev.add("b", sql.NullFloat64{})
	ev.finish()
	res := ev.done()
	require.Len(t, res.Samples, 2)
	assert.Equal(t, "3", *res.Samples[0].Value)
	assert.Equal(t, []CalculatedInputValue{{"a", "1"}, {"b", "2"}}, res.Samples[0].Inputs)
	assert.Equal(t, string(calcfield.SkipMissingInput), *res.Samples[1].SkippedReason)
}

// The span is bounded before any query runs. The api has no measurement table, so a request
// that reaches its read fails with the database's error: exactly MaxCalculatedSpan reaches
// it, and one nanosecond more is refused with LIMIT_EXCEEDED without a query.
func TestCalculatedEventsRefusesASpanAboveTheCapBeforeReading(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	api := NewApi(&rdb.RdbManager{Database: db})

	req := powerRequest()
	req.To = req.From.Add(MaxCalculatedSpan)
	_, err = api.CalculatedEvents(acme(), req)
	require.Error(t, err)
	var le *limit.Error
	require.False(t, errors.As(err, &le), "a span of exactly the cap was refused: %v", err)
	assert.Contains(t, err.Error(), "no such table", "the control: a span at the cap reaches the read")

	req.To = req.From.Add(MaxCalculatedSpan + time.Nanosecond)
	_, err = api.CalculatedEvents(acme(), req)
	require.True(t, errors.As(err, &le), "want LIMIT_EXCEEDED, got %v", err)
	assert.Equal(t, "rangeHours", le.What)
	assert.Equal(t, 168, le.Max)
	assert.Equal(t, 169, le.Got)
}

// Only the inputs the formula READS are fetched and returned. temp is declared and never
// read: an event holding only temp is no sample (so it is not a missing_input skip), a
// sample's temp reading is not among its inputs, and an event holding one read input and
// not the other is a missing_input skip.
func TestCalculatedEventsReturnsOnlyTheInputsTheFormulaReads(t *testing.T) {
	api := newCalculatedTestApi(t)
	seedReading(t, api, "acme", "meter-1", "both", calcAt, "voltage", 200)
	seedReading(t, api, "acme", "meter-1", "both", calcAt, "current", 5)
	seedReading(t, api, "acme", "meter-1", "both", calcAt, "temp", 21)
	seedReading(t, api, "acme", "meter-1", "tempOnly", calcAt.Add(time.Second), "temp", 22)
	seedReading(t, api, "acme", "meter-1", "voltageOnly", calcAt.Add(2*time.Second), "voltage", 201)
	req := powerRequest()
	req.Inputs = append(req.Inputs, CalculatedInput{Key: "temp", DataType: "DOUBLE"})

	res, err := api.CalculatedEvents(acme(), req)
	require.NoError(t, err)
	require.Len(t, res.Samples, 2, "an event with only an unread input became a sample")
	assert.Equal(t, []CalculatedInputValue{{"current", "5"}, {"voltage", "200"}}, res.Samples[0].Inputs)
	assert.Equal(t, "1", *res.Samples[0].Value)
	assert.Equal(t, []byte("voltageOnly"), res.Samples[1].EventId)
	assert.Equal(t, string(calcfield.SkipMissingInput), *res.Samples[1].SkippedReason)
	assert.Equal(t, []CalculatedSkipCount{{Reason: string(calcfield.SkipMissingInput), Count: 1}}, res.Skipped)
}

// The input values returned across all samples are capped as the read streams: at the cap
// the read answers, one value past it is refused, never truncated.
func TestCalculatedEventsRefusesAboveTheInputValueCap(t *testing.T) {
	old := calculatedInputValueCap
	calculatedInputValueCap = 4
	t.Cleanup(func() { calculatedInputValueCap = old })

	api := newCalculatedTestApi(t)
	for i, ev := range []string{"a", "b"} {
		at := calcAt.Add(time.Duration(i) * time.Second)
		seedReading(t, api, "acme", "meter-1", ev, at, "voltage", 1)
		seedReading(t, api, "acme", "meter-1", ev, at, "current", 1)
	}
	res, err := api.CalculatedEvents(acme(), powerRequest())
	require.NoError(t, err)
	assert.Len(t, res.Samples, 2)

	seedReading(t, api, "acme", "meter-1", "c", calcAt.Add(2*time.Second), "voltage", 1)
	_, err = api.CalculatedEvents(acme(), powerRequest())
	var le *limit.Error
	require.True(t, errors.As(err, &le), "want LIMIT_EXCEEDED, got %v", err)
	assert.Equal(t, "inputValues", le.What)
	assert.Equal(t, 4, le.Max)
}

// A draft formula that reads a per-device constant is evaluated with the constants the request
// carries, the same for every sample; one it omits is absent and one it carries but the formula
// does not read is ignored.
func TestCalculatedEventsEvaluatesWithTheDevicesConstants(t *testing.T) {
	api := newCalculatedTestApi(t)
	seedReading(t, api, "acme", "meter-1", "ev1", calcAt, "voltage", 230)
	seedReading(t, api, "acme", "meter-1", "ev1", calcAt, "current", 10)
	seedReading(t, api, "acme", "meter-1", "ev2", calcAt.Add(time.Minute), "voltage", 100)
	seedReading(t, api, "acme", "meter-1", "ev2", calcAt.Add(time.Minute), "current", 2)

	req := powerRequest()
	req.Expression = "voltage * current / 1000.0 * device.calibration"

	req.Constants = []CalculatedConstant{{Key: "calibration", Value: "2"}, {Key: "unread", Value: "9"}}
	res, err := api.CalculatedEvents(acme(), req)
	require.NoError(t, err)
	require.Len(t, res.Samples, 2)
	assert.Equal(t, "4.6", *res.Samples[0].Value)
	assert.Equal(t, "0.4", *res.Samples[1].Value)
	assert.Equal(t, 2, res.Computed)

	req.Constants = nil
	res, err = api.CalculatedEvents(acme(), req)
	require.NoError(t, err)
	assert.Zero(t, res.Computed)
	require.Len(t, res.Skipped, 1)
	assert.Equal(t, CalculatedSkipCount{Reason: "missing_input", Count: 2}, res.Skipped[0])

	req.Constants = []CalculatedConstant{{Key: "calibration", Value: "not a number"}}
	res, err = api.CalculatedEvents(acme(), req)
	require.NoError(t, err)
	require.Len(t, res.Skipped, 1)
	assert.Equal(t, CalculatedSkipCount{Reason: "invalid_input", Count: 2}, res.Skipped[0])
}

func TestCalculatedEventsRefusesBadConstants(t *testing.T) {
	api := newCalculatedTestApi(t)
	req := powerRequest()
	req.Expression = "voltage * device.k"
	req.Constants = []CalculatedConstant{{Key: "k", Value: "1"}, {Key: "k", Value: "2"}}
	_, err := api.CalculatedEvents(acme(), req)
	require.Error(t, err)
	assert.ErrorContains(t, err, `"k" more than once`)

	req.Constants = make([]CalculatedConstant, calcfield.MaxProfileConstants+1)
	_, err = api.CalculatedEvents(acme(), req)
	require.Error(t, err)
	var le *limit.Error
	require.True(t, errors.As(err, &le), "%v", err)
}

// A draft formula that reads a CALCULATED metric (chaining) reads the value stored under that
// metric's key like any input: the stored calculated value is a reading of the event. The
// result is evaluated by the same Formula.Eval the resolver runs.
func TestCalculatedEventsReadsAStoredCalculatedMetric(t *testing.T) {
	api := newCalculatedTestApi(t)
	seedReading(t, api, "acme", "meter-1", "e1", calcAt, "voltage", 230)
	seedReading(t, api, "acme", "meter-1", "e1", calcAt, "current", 10)
	seedReading(t, api, "acme", "meter-1", "e1", calcAt, "power_kw", 2.3) // stored, calculated
	req := CalculatedEventsRequest{
		DeviceToken: "meter-1", Expression: "power_kw * 2.0", DataType: "DOUBLE",
		Inputs: []CalculatedInput{{Key: "power_kw", DataType: "DOUBLE"}},
		From:   calcAt.Add(-time.Hour), To: calcAt.Add(time.Hour),
	}
	res, err := api.CalculatedEvents(acme(), req)
	require.NoError(t, err)
	require.Len(t, res.Samples, 1)
	require.NotNil(t, res.Samples[0].Value)
	assert.Equal(t, "4.6", *res.Samples[0].Value)
	assert.Equal(t, []CalculatedInputValue{{Key: "power_kw", Value: "2.3"}}, res.Samples[0].Inputs)
}
