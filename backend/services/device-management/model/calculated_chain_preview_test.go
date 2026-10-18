// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"
	"testing"

	"github.com/devicechain-io/dc-microservice/calcfield"
	"github.com/devicechain-io/dc-microservice/governance"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testFormula(t *testing.T, f *calcFixture, req MetricFormulaTestRequest) *MetricFormulaTestResult {
	t.Helper()
	req.ProfileToken = "meter"
	res, err := f.api.TestMetricFormula(f.ctx, &req)
	require.NoError(t, err)
	return res
}

// A constant an UPSTREAM formula reads is one the evaluation needs: it is listed, and the
// typed value reaches the upstream formula rather than being ignored as unread.
func TestTestMetricFormulaListsAndUsesTheChainsConstants(t *testing.T) {
	f := newCalcFixture(t)
	f.metric("voltage", "DOUBLE", "")
	f.metric("calibrated", "DOUBLE", "voltage * device.calibration")
	res := testFormula(t, f, MetricFormulaTestRequest{Expression: "calibrated * 2.0", DataType: "DOUBLE",
		Sample:    []MetricValueInput{{Key: "voltage", Value: "100"}},
		Constants: []MetricValueInput{{Key: "calibration", Value: "1.5"}}})
	require.Nil(t, res.CompileError)
	assert.Equal(t, []string{"calibration"}, res.Constants)
	require.NotNil(t, res.Value, "warnings: %v", res.Warnings)
	assert.Equal(t, "300", *res.Value)
	for _, w := range res.Warnings {
		assert.NotContains(t, w, `constant value for "calibration" is ignored`)
	}
}

// A formula with no key yet that ends a chain too deep is named as "this formula", never by
// the placeholder it is planned under.
func TestTestMetricFormulaNamesAnUnkeyedTooDeepFormula(t *testing.T) {
	f := newCalcFixture(t)
	f.metric("voltage", "DOUBLE", "")
	prev := "voltage"
	for i := 1; i <= calcfield.MaxChainDepth; i++ {
		key := fmt.Sprintf("c%d", i)
		f.metric(key, "DOUBLE", prev+" + 1.0")
		prev = key
	}
	res := testFormula(t, f, MetricFormulaTestRequest{Expression: prev + " + 1.0", DataType: "DOUBLE"})
	require.NotNil(t, res.CompileError)
	assert.Equal(t, "the formula ends a chain of 5 calculated metrics, this formula -> c4 -> c3 -> c2 -> c1 "+
		"(each reads the next), above the maximum of 4", *res.CompileError)
	assert.NotContains(t, *res.CompileError, "_formula_under_test")
}

// Testing one formula compiles every calculated metric of the draft, so it is bounded by the
// platform maximum, as the publish gate is.
func TestTestMetricFormulaIsBoundedByThePlatformMaximum(t *testing.T) {
	f := newCalcFixture(t)
	f.metric("voltage", "DOUBLE", "")
	for i := 0; i < governance.MaxCalculatedMetricsPerProfile; i++ {
		f.metric(fmt.Sprintf("c%02d", i), "DOUBLE", "voltage + 1.0")
	}
	// Replacing one of the draft's own formulas stays at the maximum.
	key := "c00"
	res := testFormula(t, f, MetricFormulaTestRequest{MetricKey: &key, Expression: "voltage * 2.0", DataType: "DOUBLE"})
	require.Nil(t, res.CompileError)
	// A new one would be one more than any profile may publish.
	res = testFormula(t, f, MetricFormulaTestRequest{Expression: "voltage * 2.0", DataType: "DOUBLE"})
	require.NotNil(t, res.CompileError)
	assert.Contains(t, *res.CompileError, fmt.Sprintf("%d calculated metrics with this one, above the maximum of %d",
		governance.MaxCalculatedMetricsPerProfile+1, governance.MaxCalculatedMetricsPerProfile))
}

// The bound on formula inputs is on the MEASURED metrics: a profile with as many as a formula may
// read publishes its calculated metrics, as it did before calculated metrics were declared too.
func TestThePublishGateAdmitsTheMostMeasuredMetricsWithAChain(t *testing.T) {
	f := newCalcFixture(t)
	f.api.CalculatedMetricsCeiling = &stubCeiling{n: governance.MaxCalculatedMetricsPerProfile}
	for i := 0; i < calcfield.MaxInputs; i++ {
		f.metric(fmt.Sprintf("m%03d", i), "DOUBLE", "")
	}
	f.metric("sum", "DOUBLE", "m000 + m255")
	f.metric("twice", "DOUBLE", "sum * 2.0")
	require.NoError(t, f.publish())
	assert.EqualValues(t, 1, f.versions())
}
