// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/calcfield"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/governance"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/limit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubCeiling answers a fixed calculated-metrics ceiling, or an error, and counts calls.
type stubCeiling struct {
	n     int
	err   error
	calls int
}

func (s *stubCeiling) Resolve(context.Context, string) (int, error) {
	s.calls++
	return s.n, s.err
}

type calcFixture struct {
	t   *testing.T
	api *Api
	ctx context.Context
}

func newCalcFixture(t *testing.T) *calcFixture {
	t.Helper()
	api := newPublishEmitTestApi(t)
	ctx := core.WithTenant(context.Background(), "acme")
	p := &DeviceProfile{}
	p.Token = "meter"
	require.NoError(t, api.RDB.DB(ctx).Create(p).Error)
	return &calcFixture{t: t, api: api, ctx: ctx}
}

// metric creates a metric on the fixture's profile through the real create path. An empty
// expression makes it measured.
func (f *calcFixture) metric(key, dataType, expression string, opts ...func(*MetricDefinitionCreateRequest)) *MetricDefinition {
	f.t.Helper()
	req := &MetricDefinitionCreateRequest{Token: "md-" + key, DeviceProfileToken: "meter", MetricKey: key, DataType: dataType}
	if expression != "" {
		req.Expression = &expression
	}
	for _, o := range opts {
		o(req)
	}
	m, err := f.api.CreateMetricDefinition(f.ctx, req)
	require.NoError(f.t, err)
	return m
}

func (f *calcFixture) publish() error {
	_, err := f.api.PublishDeviceProfile(f.ctx, "meter", nil, nil, "tester")
	return err
}

func (f *calcFixture) versions() int64 {
	var n int64
	require.NoError(f.t, f.api.RDB.DB(f.ctx).Model(&DeviceProfileVersion{}).Count(&n).Error)
	return n
}

func codeOf(err error) string {
	var coded interface{ Extensions() map[string]any }
	if errors.As(err, &coded) {
		if c, ok := coded.Extensions()["code"].(string); ok {
			return c
		}
	}
	return ""
}

func TestCreateMetricExpressionWriteChecks(t *testing.T) {
	f := newCalcFixture(t)

	measured := f.metric("voltage", "DOUBLE", "")
	assert.False(t, measured.Calculated())
	calculated := f.metric("power", "DOUBLE", "voltage * 2.0")
	assert.True(t, calculated.Calculated())

	for name, expr := range map[string]string{"blank": "   ", "empty": ""} {
		e := expr
		_, err := f.api.CreateMetricDefinition(f.ctx, &MetricDefinitionCreateRequest{Token: "md-" + name,
			DeviceProfileToken: "meter", MetricKey: name, DataType: "DOUBLE", Expression: &e})
		require.Errorf(t, err, "a %s expression must be refused, not stored as a calculated metric", name)
		assert.Equal(t, "INVALID_VALUE", codeOf(err))
	}
	long := strings.Repeat("voltage + ", 110) + "voltage"
	_, err := f.api.CreateMetricDefinition(f.ctx, &MetricDefinitionCreateRequest{Token: "md-long",
		DeviceProfileToken: "meter", MetricKey: "long", DataType: "DOUBLE", Expression: &long})
	require.Error(t, err)
	assert.Equal(t, "LIMIT_EXCEEDED", codeOf(err))
}

// TestCalculatedNessIsFixedAtCreation pins ADR-082 decision 1 on the update path. The
// calculated side's three states are driven by the partial-update suite; this is the
// measured side, which no field kind there describes.
func TestCalculatedNessIsFixedAtCreation(t *testing.T) {
	f := newCalcFixture(t)
	f.metric("voltage", "DOUBLE", "")
	f.metric("power", "DOUBLE", "voltage * 2.0")

	_, err := f.api.UpdateMetricDefinition(f.ctx, "md-voltage",
		&MetricDefinitionUpdateRequest{Expression: dcgraphql.OptionalStringOf("voltage * 3.0")})
	require.Error(t, err, "a measured metric must not become calculated")
	assert.Equal(t, "INVALID_VALUE", codeOf(err))

	got, err := f.api.UpdateMetricDefinition(f.ctx, "md-voltage",
		&MetricDefinitionUpdateRequest{Expression: dcgraphql.ClearedString()})
	require.NoError(t, err, "a null on a measured metric asks for what it already is")
	assert.False(t, got.Calculated())

	_, err = f.api.UpdateMetricDefinition(f.ctx, "md-power",
		&MetricDefinitionUpdateRequest{Expression: dcgraphql.ClearedString()})
	require.Error(t, err, "a calculated metric must not become measured")

	_, err = f.api.UpdateMetricDefinition(f.ctx, "md-power",
		&MetricDefinitionUpdateRequest{Expression: dcgraphql.OptionalStringOf("  ")})
	require.Error(t, err, "a blank formula must be refused")

	rows, err := f.api.MetricDefinitionsByToken(f.ctx, []string{"md-voltage", "md-power"})
	require.NoError(t, err)
	for _, r := range rows {
		assert.Equal(t, r.MetricKey == "power", r.Calculated(), "%s changed kind", r.MetricKey)
	}
}

// TestAProfileWithoutCalculatedMetricsNeverReadsTheCeiling pins that the gate costs a
// formula-free profile nothing beyond a parse: its publish neither reads the tenant's
// ceiling nor is refused by a ceiling of zero.
func TestAProfileWithoutCalculatedMetricsNeverReadsTheCeiling(t *testing.T) {
	f := newCalcFixture(t)
	ceiling := &stubCeiling{err: errors.New("must not be called")}
	f.api.CalculatedMetricsCeiling = ceiling
	f.metric("voltage", "DOUBLE", "")

	require.NoError(t, f.publish())
	assert.Zero(t, ceiling.calls)
	assert.EqualValues(t, 1, f.versions())
}

// TestThePublishGateReportsEveryFormulaProblem drives gate items 1–3 and the range and
// allowed-values rules together, and asserts the refusal names every one of them, keyed
// by metric, without the ceiling ever being consulted.
func TestThePublishGateReportsEveryFormulaProblem(t *testing.T) {
	f := newCalcFixture(t)
	ceiling := &stubCeiling{n: governance.MaxCalculatedMetricsPerProfile}
	f.api.CalculatedMetricsCeiling = ceiling
	f.metric("voltage", "DOUBLE", "")
	f.metric("current", "DOUBLE", "")
	f.metric("count", "INT", "")
	f.metric("power", "DOUBLE", "voltage * current")
	f.metric("chain", "DOUBLE", "power * 2.0")                 // reads a calculated metric: allowed (chaining)
	f.metric("typed", "DOUBLE", "count + 1")                   // INT formula for a DOUBLE metric
	f.metric("nosuch", "DOUBLE", "voltage * missing")          // undeclared input
	f.metric("constant", "DOUBLE", "1.0 + 2.0")                // reads no input
	f.metric("syntax", "DOUBLE", "voltage *")                  // does not parse
	f.metric("clock", "BOOLEAN", "voltage > 1.0 && now() > 0") // outside the language
	f.metric("enumed", "DOUBLE", "voltage * 1.0", func(r *MetricDefinitionCreateRequest) {
		e := `["1","2"]`
		r.Enum = &e
	})
	f.metric("empty", "DOUBLE", "voltage * 1.0", func(r *MetricDefinitionCreateRequest) {
		lo, hi := 10.0, 1.0
		r.MinValue, r.MaxValue = &lo, &hi
	})

	err := f.publish()
	require.Error(t, err)
	assert.Equal(t, "INVALID_VALUE", codeOf(err))
	msg := err.Error()
	assert.NotContains(t, msg, `"chain"`, "a chain of calculated metrics is not a problem")
	for _, key := range []string{"typed", "nosuch", "constant", "syntax", "clock", "enumed", "empty"} {
		assert.Containsf(t, msg, fmt.Sprintf("%q", key), "the refusal does not name %s", key)
	}
	assert.Contains(t, msg, "7 calculated metric problem(s)")
	assert.NotContains(t, msg, `"power"`, "a valid formula is not a problem")
	assert.Zero(t, ceiling.calls, "a refused formula is refused before the ceiling is read")
	assert.Zero(t, f.versions())
}

func TestThePublishGateEnforcesTheCeiling(t *testing.T) {
	setup := func(t *testing.T) *calcFixture {
		f := newCalcFixture(t)
		f.metric("voltage", "DOUBLE", "")
		f.metric("power", "DOUBLE", "voltage * 2.0")
		f.metric("half", "DOUBLE", "voltage / 2.0")
		return f
	}

	t.Run("nothing wired is the platform default, off", func(t *testing.T) {
		f := setup(t)
		err := f.publish()
		var ce *CalculatedMetricsCeilingError
		require.ErrorAs(t, err, &ce)
		assert.Equal(t, 0, ce.Ceiling)
		assert.Equal(t, limit.Code, codeOf(err))
		assert.Contains(t, err.Error(), "not enabled for this tenant's tier")
		assert.Zero(t, f.versions())
	})
	t.Run("below the count", func(t *testing.T) {
		f := setup(t)
		f.api.CalculatedMetricsCeiling = &stubCeiling{n: 1}
		var ce *CalculatedMetricsCeilingError
		require.ErrorAs(t, f.publish(), &ce)
		assert.Equal(t, 2, ce.Count)
		assert.Equal(t, 1, ce.Ceiling)
		assert.Zero(t, f.versions())
	})
	t.Run("an unreadable ceiling refuses rather than guessing", func(t *testing.T) {
		f := setup(t)
		f.api.CalculatedMetricsCeiling = &stubCeiling{n: 32, err: errors.New("user-management down")}
		err := f.publish()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "could not be read")
		assert.NotContains(t, err.Error(), "user-management down", "the cause is logged, not served")
		assert.Zero(t, f.versions())
	})
	t.Run("at the ceiling, it publishes", func(t *testing.T) {
		f := setup(t)
		f.api.CalculatedMetricsCeiling = &stubCeiling{n: 2}
		require.NoError(t, f.publish())
		assert.EqualValues(t, 1, f.versions(), "two calculated metrics within a ceiling of two reach a published version")
	})
}

// TestTheGateRefusesAboveThePlatformMaximumBeforeCompiling pins that the platform maximum
// is checked before any formula is compiled, so a profile with hundreds of (here broken)
// formulas is refused for its count, cheaply.
func TestTheGateRefusesAboveThePlatformMaximumBeforeCompiling(t *testing.T) {
	f := newCalcFixture(t)
	f.api.CalculatedMetricsCeiling = &stubCeiling{n: governance.MaxCalculatedMetricsPerProfile}
	for i := 0; i <= governance.MaxCalculatedMetricsPerProfile; i++ {
		f.metric(fmt.Sprintf("c%02d", i), "DOUBLE", "not a formula (")
	}
	var ce *CalculatedMetricsCeilingError
	require.ErrorAs(t, f.publish(), &ce)
	assert.Equal(t, governance.MaxCalculatedMetricsPerProfile+1, ce.Count)
	assert.Equal(t, governance.MaxCalculatedMetricsPerProfile, ce.Ceiling)
}

// TestDuplicateKeysAreAProblem drives gate item 6 on the snapshot shape directly: the
// draft table refuses a duplicate key at write, so only a snapshot built some other way
// could carry one, and the gate must still not admit it.
func TestDuplicateKeysAreAProblem(t *testing.T) {
	expr := func(s string) *MetricDefinition {
		m := &MetricDefinition{MetricKey: "power", DataType: "DOUBLE"}
		m.Expression.String, m.Expression.Valid = s, true
		return m
	}
	p := newProfileFormulas([]*MetricDefinition{
		{MetricKey: "voltage", DataType: "DOUBLE"}, expr("voltage * 2.0"), expr("voltage * 3.0"),
	})
	problems := p.problems()
	require.NotEmpty(t, problems)
	assert.Contains(t, problems[0], `metric key "power" is declared more than once`)
}

func TestTestMetricFormula(t *testing.T) {
	f := newCalcFixture(t)
	f.metric("voltage", "DOUBLE", "", func(r *MetricDefinitionCreateRequest) { u := "V"; r.Unit = &u })
	f.metric("current", "DOUBLE", "", func(r *MetricDefinitionCreateRequest) { u := "A"; r.Unit = &u })
	f.metric("power", "DOUBLE", "voltage * current")

	run := func(t *testing.T, req MetricFormulaTestRequest) *MetricFormulaTestResult {
		t.Helper()
		req.ProfileToken = "meter"
		res, err := f.api.TestMetricFormula(f.ctx, &req)
		require.NoError(t, err)
		return res
	}
	sample := []MetricValueInput{{Key: "voltage", Value: "230"}, {Key: "current", Value: "4.5"}}

	t.Run("a value, formatted as it would be stored", func(t *testing.T) {
		res := run(t, MetricFormulaTestRequest{Expression: "voltage * current / 1000.0", DataType: "DOUBLE", Sample: sample})
		require.Nil(t, res.CompileError)
		require.NotNil(t, res.Value)
		assert.Equal(t, "1.035", *res.Value)
		assert.Nil(t, res.SkippedReason)
		assert.Equal(t, []string{"voltage", "current"}, res.Inputs)
		require.NotNil(t, res.EstimatedCost)
		assert.Positive(t, *res.EstimatedCost)
	})
	t.Run("a missing input skips, never a default", func(t *testing.T) {
		res := run(t, MetricFormulaTestRequest{Expression: "voltage * current", DataType: "DOUBLE",
			Sample: []MetricValueInput{{Key: "voltage", Value: "230"}}})
		assert.Nil(t, res.Value)
		require.NotNil(t, res.SkippedReason)
		assert.Equal(t, "missing_input", *res.SkippedReason)
	})
	t.Run("a range skip", func(t *testing.T) {
		hi := 100.0
		res := run(t, MetricFormulaTestRequest{Expression: "voltage * current", DataType: "DOUBLE", MaxValue: &hi, Sample: sample})
		require.NotNil(t, res.SkippedReason)
		assert.Equal(t, "range", *res.SkippedReason)
	})
	t.Run("a compile error is data, not a request error", func(t *testing.T) {
		res := run(t, MetricFormulaTestRequest{Expression: "voltage *", DataType: "DOUBLE", Sample: sample})
		require.NotNil(t, res.CompileError)
		assert.Nil(t, res.Value)
		assert.Nil(t, res.SkippedReason)
		assert.Empty(t, res.Inputs)
	})
	t.Run("reading a calculated metric evaluates the chain", func(t *testing.T) {
		res := run(t, MetricFormulaTestRequest{Expression: "power * 2.0", DataType: "DOUBLE", Sample: sample})
		require.Nil(t, res.CompileError)
		require.NotNil(t, res.Value)
		assert.Equal(t, "2070", *res.Value) // power is 230 * 4.5 = 1035, computed from the sample
		assert.Equal(t, []string{"power"}, res.Inputs)
		assert.Contains(t, strings.Join(res.Warnings, "|"), "reads the calculated metric(s) power")
	})
	t.Run("a typed value for a calculated metric is ignored with a warning", func(t *testing.T) {
		res := run(t, MetricFormulaTestRequest{Expression: "power * 2.0", DataType: "DOUBLE",
			Sample: append(append([]MetricValueInput{}, sample...), MetricValueInput{Key: "power", Value: "1"})})
		require.NotNil(t, res.Value)
		assert.Equal(t, "2070", *res.Value)
		assert.Contains(t, strings.Join(res.Warnings, "|"), `the sample value for "power" is ignored`)
	})
	t.Run("a formula reading itself through a cycle is refused", func(t *testing.T) {
		key := "power"
		res := run(t, MetricFormulaTestRequest{MetricKey: &key, Expression: "power + voltage", DataType: "DOUBLE", Sample: sample})
		require.NotNil(t, res.CompileError)
		assert.Contains(t, *res.CompileError, "power -> power")
	})
	t.Run("the formula under test replaces the draft's own", func(t *testing.T) {
		key := "power"
		res := run(t, MetricFormulaTestRequest{MetricKey: &key, Expression: "voltage + current", DataType: "DOUBLE", Sample: sample})
		require.Nil(t, res.CompileError)
		assert.Equal(t, "234.5", *res.Value)
	})
	t.Run("an unknown data type", func(t *testing.T) {
		res := run(t, MetricFormulaTestRequest{Expression: "voltage", DataType: "STRING", Sample: sample})
		require.NotNil(t, res.CompileError)
	})
	t.Run("warnings", func(t *testing.T) {
		res := run(t, MetricFormulaTestRequest{Expression: "voltage * current", DataType: "DOUBLE",
			Sample: append([]MetricValueInput{{Key: "power", Value: "9"}}, sample...)})
		joined := strings.Join(res.Warnings, "\n")
		assert.Contains(t, joined, `"power" is ignored`, "a device-sent calculated key is not an input")
		assert.Contains(t, joined, "12 digits before the decimal point", "unbounded DOUBLE result")
		assert.Contains(t, joined, "voltage (V), current (A)", "inputs with units")

		lo, hi := -1e6, 1e6
		res = run(t, MetricFormulaTestRequest{Expression: "voltage > 1.0", DataType: "BOOLEAN", MinValue: &lo, MaxValue: &hi, Sample: sample})
		for _, w := range res.Warnings {
			assert.NotContains(t, w, "decimal point", "a BOOLEAN result needs no storage range")
		}
	})
	t.Run("a key the published version has as measured", func(t *testing.T) {
		g := newCalcFixture(t)
		g.metric("voltage", "DOUBLE", "")
		g.metric("power", "DOUBLE", "")
		require.NoError(t, g.publish())
		key := "power"
		res, err := g.api.TestMetricFormula(g.ctx, &MetricFormulaTestRequest{ProfileToken: "meter", MetricKey: &key,
			Expression: "voltage", DataType: "DOUBLE", Sample: sample[:1]})
		require.NoError(t, err)
		// The draft still has power as measured, so this formula is refused for its key...
		require.NotNil(t, res.CompileError)

		// ...and once the draft makes it calculated, the warning says what publishing would do.
		require.NoError(t, g.api.RDB.DB(g.ctx).Where("metric_key = ?", "power").Delete(&MetricDefinition{}).Error)
		g.metric("power2", "DOUBLE", "voltage") // unrelated; the profile keeps a calculated metric
		res, err = g.api.TestMetricFormula(g.ctx, &MetricFormulaTestRequest{ProfileToken: "meter", MetricKey: &key,
			Expression: "voltage", DataType: "DOUBLE", Sample: sample[:1]})
		require.NoError(t, err)
		require.Nil(t, res.CompileError)
		assert.Contains(t, strings.Join(res.Warnings, "\n"), "declares \"power\" as a measured metric")
	})
	t.Run("request refusals", func(t *testing.T) {
		_, err := f.api.TestMetricFormula(f.ctx, &MetricFormulaTestRequest{ProfileToken: "meter", Expression: "voltage",
			DataType: "DOUBLE", Sample: []MetricValueInput{{Key: "voltage", Value: "1"}, {Key: "voltage", Value: "2"}}})
		assert.Equal(t, "INVALID_VALUE", codeOf(err))

		many := make([]MetricValueInput, 257)
		for i := range many {
			many[i] = MetricValueInput{Key: fmt.Sprintf("k%d", i), Value: "1"}
		}
		_, err = f.api.TestMetricFormula(f.ctx, &MetricFormulaTestRequest{ProfileToken: "meter", Expression: "voltage",
			DataType: "DOUBLE", Sample: many})
		_, isLimit := limit.As(err)
		assert.True(t, isLimit, "got %v", err)

		_, err = f.api.TestMetricFormula(f.ctx, &MetricFormulaTestRequest{ProfileToken: "nope", Expression: "voltage",
			DataType: "DOUBLE"})
		assert.Error(t, err)
	})
}

// TestThePublishGateRefusesAProfileOverTheSummedCostCeiling pins gate item 4's per-profile
// half: formulas that each pass the per-formula ceiling are still refused together when
// their summed estimate exceeds the profile ceiling.
func TestThePublishGateRefusesAProfileOverTheSummedCostCeiling(t *testing.T) {
	f := newCalcFixture(t)
	f.api.CalculatedMetricsCeiling = &stubCeiling{n: governance.MaxCalculatedMetricsPerProfile}
	for _, k := range []string{"a", "b", "c", "d"} {
		f.metric(k, "DOUBLE", "")
	}
	const term = "math.max(a, b) * math.min(c, d) + math.abs(a - b) * math.abs(c - d) + (a > b ? c : d)"
	const formula = term + " + " + term + " + " + term
	res, err := f.api.TestMetricFormula(f.ctx, &MetricFormulaTestRequest{ProfileToken: "meter",
		Expression: formula, DataType: "DOUBLE"})
	require.NoError(t, err)
	require.Nil(t, res.CompileError)
	cost := *res.EstimatedCost
	require.LessOrEqual(t, cost, int(calcfield.CostCeiling))
	n := int(calcfield.ProfileCostCeiling)/cost + 1
	require.LessOrEqualf(t, n, governance.MaxCalculatedMetricsPerProfile,
		"formula cost %d too small to exceed the profile ceiling within the platform maximum", cost)
	for i := 0; i < n; i++ {
		f.metric(fmt.Sprintf("calc%02d", i), "DOUBLE", formula)
	}
	err = f.publish()
	require.Error(t, err)
	assert.Equal(t, "INVALID_VALUE", codeOf(err))
	assert.Contains(t, err.Error(), fmt.Sprint(calcfield.ProfileCostCeiling))
	assert.Contains(t, err.Error(), "1 calculated metric problem(s)")
	assert.Zero(t, f.versions())

	// Contrast: one fewer formula fits under the profile ceiling and publishes.
	_, err = f.api.DeleteMetricDefinition(f.ctx, fmt.Sprintf("md-calc%02d", n-1))
	require.NoError(t, err)
	require.NoError(t, f.publish()) // (n-1)*cost <= the ceiling by the choice of n
	assert.EqualValues(t, 1, f.versions())
}

// TestTheStorageBoundWarningFollowsTheStoredMagnitude pins warning (b) of the test panel at
// its walls: a range strictly inside ±1e12 is quiet, and either bound at the storage
// magnitude, or missing, warns.
func TestTheStorageBoundWarningFollowsTheStoredMagnitude(t *testing.T) {
	f := newCalcFixture(t)
	f.metric("voltage", "DOUBLE", "")
	warns := func(lo, hi *float64) bool {
		t.Helper()
		res, err := f.api.TestMetricFormula(f.ctx, &MetricFormulaTestRequest{ProfileToken: "meter",
			Expression: "voltage * 2.0", DataType: "DOUBLE", MinValue: lo, MaxValue: hi})
		require.NoError(t, err)
		require.Nil(t, res.CompileError)
		return strings.Contains(strings.Join(res.Warnings, "\n"), "12 digits before the decimal point")
	}
	fp := func(v float64) *float64 { return &v }
	assert.False(t, warns(fp(-999999999999), fp(999999999999)), "a range inside storage must not warn")
	assert.True(t, warns(fp(-1), fp(1e12)), "a max at the storage magnitude must warn")
	assert.True(t, warns(fp(-1e12), fp(1)), "a min at the storage magnitude must warn")
	assert.True(t, warns(nil, fp(1)), "a missing min must warn")
	assert.True(t, warns(fp(1), nil), "a missing max must warn")
}

// TestTheGateRefusesAFormulaOverMoreMeasuredMetricsThanItCanDeclare pins the publish-time
// answer to a profile with more measured metrics than a formula environment declares
// (calcfield.MaxInputs): its calculated metrics are refused with a message saying so, rather
// than published and then never computed. The resolver's backstop for the same case is
// TestTooManyMeasuredMetricsSkipEveryFormula.
func TestTheGateRefusesAFormulaOverMoreMeasuredMetricsThanItCanDeclare(t *testing.T) {
	f := newCalcFixture(t)
	f.api.CalculatedMetricsCeiling = &stubCeiling{n: 1}
	for i := 0; i <= calcfield.MaxInputs; i++ {
		f.metric(fmt.Sprintf("m%03d", i), "DOUBLE", "")
	}
	f.metric("sum", "DOUBLE", "m000 + m001")
	err := f.publish()
	require.Error(t, err)
	assert.Equal(t, "INVALID_VALUE", codeOf(err))
	assert.Contains(t, err.Error(), "cannot be declared as formula inputs")
	assert.Contains(t, err.Error(), fmt.Sprintf("exceeds the maximum of %d", calcfield.MaxInputs))
	assert.Zero(t, f.versions())
}

func TestThePublishGateAdmitsAChain(t *testing.T) {
	f := newCalcFixture(t)
	f.api.CalculatedMetricsCeiling = &stubCeiling{n: governance.MaxCalculatedMetricsPerProfile}
	f.metric("voltage", "DOUBLE", "")
	f.metric("current", "DOUBLE", "")
	f.metric("kva", "DOUBLE", "voltage * current / 1000.0")
	f.metric("kw", "DOUBLE", "kva * 0.9")
	f.metric("pf", "DOUBLE", "kw / kva")
	require.NoError(t, f.publish())
	assert.EqualValues(t, 1, f.versions())
}

func TestThePublishGateRefusesACycleAndNamesIt(t *testing.T) {
	f := newCalcFixture(t)
	f.api.CalculatedMetricsCeiling = &stubCeiling{n: governance.MaxCalculatedMetricsPerProfile}
	f.metric("voltage", "DOUBLE", "")
	f.metric("a", "DOUBLE", "c + voltage")
	f.metric("b", "DOUBLE", "a + voltage")
	f.metric("c", "DOUBLE", "b + voltage")
	f.metric("self", "DOUBLE", "self + voltage")
	err := f.publish()
	require.Error(t, err)
	assert.Equal(t, "INVALID_VALUE", codeOf(err))
	assert.Contains(t, err.Error(), "a -> c -> b -> a")
	assert.Contains(t, err.Error(), "self -> self")
	assert.Contains(t, err.Error(), "2 calculated metric problem(s)", "a ring is one problem however long")
	assert.Zero(t, f.versions())
}

func TestThePublishGateRefusesAChainDeeperThanTheLimit(t *testing.T) {
	build := func(n int) *calcFixture {
		f := newCalcFixture(t)
		f.api.CalculatedMetricsCeiling = &stubCeiling{n: governance.MaxCalculatedMetricsPerProfile}
		f.metric("voltage", "DOUBLE", "")
		prev := "voltage"
		for i := 1; i <= n; i++ {
			key := fmt.Sprintf("c%d", i)
			f.metric(key, "DOUBLE", prev+" + 1.0")
			prev = key
		}
		return f
	}
	ok := build(calcfield.MaxChainDepth)
	require.NoError(t, ok.publish())

	f := build(calcfield.MaxChainDepth + 1)
	err := f.publish()
	require.Error(t, err)
	assert.Equal(t, "INVALID_VALUE", codeOf(err))
	assert.Contains(t, err.Error(), `"c5" ends a chain of 5 calculated metrics, c5 -> c4 -> c3 -> c2 -> c1`)
	assert.Zero(t, f.versions())
}

// The static cost of a chain is the sum of its formulas, each of which runs once per sample,
// and it counts against the profile ceiling like any other formulas.
func TestAChainsCostCountsTowardTheProfileCeiling(t *testing.T) {
	f := newCalcFixture(t)
	f.api.CalculatedMetricsCeiling = &stubCeiling{n: governance.MaxCalculatedMetricsPerProfile}
	f.metric("voltage", "DOUBLE", "")
	f.metric("current", "DOUBLE", "")
	one := "math.sqrt(math.abs(%[1]s * current + %[1]s)) + math.sqrt(math.abs(current * %[1]s + current)) + math.sqrt(math.abs(%[1]s - current))"
	heavy := one + " + " + one
	prev := "voltage"
	for i := 0; i < 32; i++ { // eight chains of four, each formula well under its own ceiling
		key := fmt.Sprintf("h%02d", i)
		f.metric(key, "DOUBLE", fmt.Sprintf(heavy, prev))
		if i%calcfield.MaxChainDepth == calcfield.MaxChainDepth-1 {
			prev = "voltage"
		} else {
			prev = key
		}
	}
	err := f.publish()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "together, above the ceiling of 1000")
}
