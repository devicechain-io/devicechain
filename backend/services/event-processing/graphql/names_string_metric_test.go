// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"fmt"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A rule that names a STRING metric never fires, and the gate says so when the caller sent the
// profile's metric types. A warning never changes Valid.

const (
	modeRule = `{"name":"mode","type":"threshold","severity":"major","when":{"cel":"\"mode\" in m && m[\"mode\"] > 1.0"},"actions":[{"type":"raiseAlarm","raiseAlarm":{"alarmKey":"mode"}}]}`
	tempRule = `{"name":"temp","type":"threshold","severity":"major","when":{"cel":"\"t\" in m && m[\"t\"] > 80.0"},"actions":[{"type":"raiseAlarm","raiseAlarm":{"alarmKey":"temp"}}]}`
)

func metricsArg(m ...detectionMetricInput) *[]detectionMetricInput { return &m }

func validate(t *testing.T, args validateArgs) (*DetectionRuleValidationResultResolver, error) {
	t.Helper()
	return (&SchemaResolver{}).ValidateDetectionRules(authedContext(auth.DeviceRead), args)
}

func TestValidateDetectionRules_WarnsWhenARuleNamesAStringMetric(t *testing.T) {
	res, err := validate(t, validateArgs{
		Rules: []detectionRuleInput{{Token: "m", Definition: modeRule}, {Token: "t", Definition: tempRule}},
		Metrics: metricsArg(
			detectionMetricInput{Key: "mode", DataType: "STRING"},
			detectionMetricInput{Key: "t", DataType: "DOUBLE"}),
	})
	require.NoError(t, err)
	assert.True(t, res.Valid(), "a warning must never make the batch invalid")
	require.Len(t, res.Warnings(), 1)
	w := res.Warnings()[0]
	assert.Equal(t, int32(0), w.Index())
	assert.Equal(t, "m", w.Token())
	assert.Equal(t, "namesStringMetric", w.Code())
	assert.Equal(t, []string{"mode"}, w.Params())
	assert.Contains(t, w.Message(), `"mode"`)
}

// The other direction: no warning without the types, with a numeric type, or for a key the rule
// does not name.
func TestValidateDetectionRules_NoStringMetricWarningWithoutCause(t *testing.T) {
	rules := []detectionRuleInput{{Token: "m", Definition: modeRule}}
	for name, metrics := range map[string]*[]detectionMetricInput{
		"types omitted":      nil,
		"empty list":         metricsArg(),
		"declared numeric":   metricsArg(detectionMetricInput{Key: "mode", DataType: "DOUBLE"}),
		"string key unnamed": metricsArg(detectionMetricInput{Key: "other", DataType: "STRING"}),
	} {
		res, err := validate(t, validateArgs{Rules: rules, Metrics: metrics})
		require.NoError(t, err, name)
		assert.True(t, res.Valid(), name)
		assert.Empty(t, res.Warnings(), name)
	}
}

func TestValidateDetectionRules_MetricListFailsClosed(t *testing.T) {
	rules := []detectionRuleInput{{Token: "m", Definition: modeRule}}
	_, err := validate(t, validateArgs{Rules: rules, Metrics: metricsArg(detectionMetricInput{Key: "mode", DataType: "TEXT"})})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"mode"`)

	_, err = validate(t, validateArgs{Rules: rules, Metrics: metricsArg(
		detectionMetricInput{Key: "mode", DataType: "STRING"}, detectionMetricInput{Key: "mode", DataType: "DOUBLE"})})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "conflicting")

	// The same key listed twice with the same type is not a conflict.
	_, err = validate(t, validateArgs{Rules: rules, Metrics: metricsArg(
		detectionMetricInput{Key: "mode", DataType: "string"}, detectionMetricInput{Key: "mode", DataType: "STRING"})})
	require.NoError(t, err)

	atCap := make([]detectionMetricInput, MaxMetricsPerValidation)
	for i := range atCap {
		atCap[i] = detectionMetricInput{Key: fmt.Sprintf("k%d", i), DataType: "DOUBLE"}
	}
	res, err := validate(t, validateArgs{Rules: rules, Metrics: &atCap})
	require.NoError(t, err)
	require.True(t, res.Valid(), "exactly the documented metric limit must be accepted")

	over := make([]detectionMetricInput, MaxMetricsPerValidation+1)
	for i := range over {
		over[i] = detectionMetricInput{Key: "k", DataType: "DOUBLE"}
	}
	_, err = validate(t, validateArgs{Rules: rules, Metrics: &over})
	require.Error(t, err)
}
