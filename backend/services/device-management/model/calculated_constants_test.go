// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/calcfield"
	"github.com/devicechain-io/dc-microservice/governance"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Per-device constants at the publish gate: a formula reading `device.name` is checked by the
// same compile as any other, so what the gate admits is what the resolver compiles.
func TestThePublishGateAdmitsDeviceConstantsAndReportsTheirProblems(t *testing.T) {
	f := newCalcFixture(t)
	f.api.CalculatedMetricsCeiling = &stubCeiling{n: governance.MaxCalculatedMetricsPerProfile}
	f.metric("voltage", "DOUBLE", "")
	f.metric("count", "INT", "")
	f.metric("calibrated", "DOUBLE", "voltage * device.calibration")
	f.metric("guarded", "DOUBLE", `voltage * (has(device.gain) ? device.gain : 1.0)`)
	f.metric("whole", "INT", "count + int(device.step)")
	require.NoError(t, f.publish(), "constants in valid formulas publish")

	g := newCalcFixture(t)
	g.api.CalculatedMetricsCeiling = &stubCeiling{n: governance.MaxCalculatedMetricsPerProfile}
	g.metric("voltage", "DOUBLE", "")
	g.metric("count", "INT", "")
	g.metric("alone", "DOUBLE", "device.calibration * 2.0")          // a constant is not a measurement
	g.metric("typed", "INT", "count + device.step")                  // double constant in an INT formula
	g.metric("bare", "BOOLEAN", "voltage > 0.0 && device == device") // the whole map
	var many strings.Builder
	many.WriteString("voltage")
	for i := 0; i <= calcfield.MaxConstants; i++ {
		fmt.Fprintf(&many, " + device.c%d", i)
	}
	g.metric("toomany", "DOUBLE", many.String())
	err := g.publish()
	require.Error(t, err)
	assert.Equal(t, "INVALID_VALUE", codeOf(err))
	msg := err.Error()
	assert.Contains(t, msg, "4 calculated metric problem(s)")
	assert.Contains(t, msg, "reads no input")
	assert.Contains(t, msg, "produces double")
	assert.Contains(t, msg, "device may only be used as")
	assert.Contains(t, msg, "above the maximum of 8")
	assert.Zero(t, g.versions())
}

func TestThePublishGateBoundsAProfilesConstantsTogether(t *testing.T) {
	f := newCalcFixture(t)
	f.api.CalculatedMetricsCeiling = &stubCeiling{n: governance.MaxCalculatedMetricsPerProfile}
	f.metric("voltage", "DOUBLE", "")
	for m := 0; m <= calcfield.MaxProfileConstants/calcfield.MaxConstants; m++ {
		var sb strings.Builder
		sb.WriteString("voltage")
		for i := 0; i < calcfield.MaxConstants; i++ {
			fmt.Fprintf(&sb, " + device.k%d_%d", m, i)
		}
		f.metric(fmt.Sprintf("m%d", m), "DOUBLE", sb.String())
	}
	err := f.publish()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "distinct device constants together")
}

func TestTestMetricFormulaWithDeviceConstants(t *testing.T) {
	f := newCalcFixture(t)
	f.metric("voltage", "DOUBLE", "")
	run := func(t *testing.T, expr string, consts ...MetricValueInput) *MetricFormulaTestResult {
		t.Helper()
		res, err := f.api.TestMetricFormula(f.ctx, &MetricFormulaTestRequest{ProfileToken: "meter", Expression: expr,
			DataType: "DOUBLE", Sample: []MetricValueInput{{Key: "voltage", Value: "230"}}, Constants: consts})
		require.NoError(t, err)
		return res
	}
	t.Run("the constant typed is the constant read", func(t *testing.T) {
		res := run(t, "voltage * device.calibration", MetricValueInput{Key: "calibration", Value: "1.02"})
		require.Nil(t, res.CompileError)
		require.NotNil(t, res.Value)
		assert.Equal(t, "234.6", *res.Value)
		assert.Equal(t, []string{"calibration"}, res.Constants)
		assert.Equal(t, []string{"voltage"}, res.Inputs)
		require.Len(t, res.Warnings, 2, "the storage-bound warning and the constants note")
		assert.Contains(t, strings.Join(res.Warnings, "\n"), "read from the device's metadata when a reading is resolved")
		assert.Contains(t, strings.Join(res.Warnings, "\n"), "whatever the device is later edited to")
	})
	t.Run("a constant not typed is absent, as for a device without it", func(t *testing.T) {
		res := run(t, "voltage * device.calibration")
		require.NotNil(t, res.SkippedReason)
		assert.Equal(t, "missing_input", *res.SkippedReason)
	})
	t.Run("text that is not a number is invalid, never zero", func(t *testing.T) {
		res := run(t, "voltage * device.calibration", MetricValueInput{Key: "calibration", Value: "abc"})
		require.NotNil(t, res.SkippedReason)
		assert.Equal(t, "invalid_input", *res.SkippedReason)
	})
	t.Run("a constant the formula does not read is ignored with a warning", func(t *testing.T) {
		res := run(t, "voltage * 2.0", MetricValueInput{Key: "calibration", Value: "1.02"})
		require.NotNil(t, res.Value)
		assert.Empty(t, res.Constants)
		assert.Contains(t, strings.Join(res.Warnings, "\n"), `the constant value for "calibration" is ignored`)
	})
	t.Run("a formula with no constants lists none and warns of none", func(t *testing.T) {
		res := run(t, "voltage * 2.0")
		assert.Equal(t, []string{}, res.Constants)
		assert.NotContains(t, strings.Join(res.Warnings, "\n"), "device constants")
	})
	t.Run("a constant named twice is refused", func(t *testing.T) {
		_, err := f.api.TestMetricFormula(f.ctx, &MetricFormulaTestRequest{ProfileToken: "meter",
			Expression: "voltage * device.k", DataType: "DOUBLE",
			Constants: []MetricValueInput{{Key: "k", Value: "1"}, {Key: "k", Value: "2"}}})
		require.Error(t, err)
		assert.Equal(t, "INVALID_VALUE", codeOf(err))
	})
	t.Run("more constants than a profile may read are refused", func(t *testing.T) {
		consts := make([]MetricValueInput, calcfield.MaxProfileConstants+1)
		for i := range consts {
			consts[i] = MetricValueInput{Key: fmt.Sprintf("k%d", i), Value: "1"}
		}
		_, err := f.api.TestMetricFormula(f.ctx, &MetricFormulaTestRequest{ProfileToken: "meter",
			Expression: "voltage", DataType: "DOUBLE", Constants: consts})
		require.Error(t, err)
		assert.Equal(t, "LIMIT_EXCEEDED", codeOf(err))
	})
}
