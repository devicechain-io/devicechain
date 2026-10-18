// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	gql "github.com/graph-gophers/graphql-go"
	"github.com/stretchr/testify/require"
)

// THE WIRE HALF OF CALCULATED-METRIC AUTHORING. The model tests pin what the gate and
// testMetricFormula decide; this pins that the schema carries expression in and
// calculated/expression out under those names, and that testMetricFormula answers through
// the real schema with its result fields.
func TestGraphQLAuthorsAndTestsACalculatedMetric(t *testing.T) {
	ctx := profileLocationCtx(t)
	execProfileGql(t, ctx, createProfileMutation, map[string]any{"request": map[string]any{"token": "meter"}})

	const create = `mutation ($request: MetricDefinitionCreateRequest) {
	  createMetricDefinition(request: $request) { metricKey calculated expression }
	}`
	execProfileGql(t, ctx, create, map[string]any{"request": map[string]any{
		"token": "md-v", "deviceProfileToken": "meter", "metricKey": "voltage", "dataType": "DOUBLE"}})
	data := execProfileGql(t, ctx, create, map[string]any{"request": map[string]any{
		"token": "md-p", "deviceProfileToken": "meter", "metricKey": "power", "dataType": "DOUBLE",
		"expression": "voltage * 2.0"}})
	var created struct {
		M struct {
			Calculated bool    `json:"calculated"`
			Expression *string `json:"expression"`
		} `json:"createMetricDefinition"`
	}
	require.NoError(t, json.Unmarshal(data, &created))
	require.True(t, created.M.Calculated)
	require.NotNil(t, created.M.Expression)
	require.Equal(t, "voltage * 2.0", *created.M.Expression)

	data = execProfileGql(t, ctx, `query {
	  testMetricFormula(profileToken: "meter", metricKey: "power", expression: "voltage * 2.0", dataType: "DOUBLE",
	    sample: [{key: "voltage", value: "21.5"}]) {
	    compileError value skippedReason inputs warnings estimatedCost
	  }
	}`, nil)
	var tested struct {
		R struct {
			CompileError  *string  `json:"compileError"`
			Value         *string  `json:"value"`
			SkippedReason *string  `json:"skippedReason"`
			Inputs        []string `json:"inputs"`
			Warnings      []string `json:"warnings"`
			EstimatedCost *int     `json:"estimatedCost"`
		} `json:"testMetricFormula"`
	}
	require.NoError(t, json.Unmarshal(data, &tested))
	require.Nil(t, tested.R.CompileError)
	require.NotNil(t, tested.R.Value)
	require.Equal(t, "43", *tested.R.Value)
	require.Nil(t, tested.R.SkippedReason)
	require.Equal(t, []string{"voltage"}, tested.R.Inputs)
	require.NotNil(t, tested.R.EstimatedCost)
}

const testMetricFormulaQuery = `query {
  testMetricFormula(profileToken: "meter", expression: "voltage * 2.0", dataType: "DOUBLE",
    sample: [{key: "voltage", value: "1"}]) { value compileError }
}`

// testMetricFormula is an authoring tool: a member limited to part of the tenant is refused
// by the resolver itself (Authorize, not AuthorizeCatalogue), and a caller without
// device:read is refused outright.
func TestTestMetricFormulaRefusesScopedAndUnauthorizedCallers(t *testing.T) {
	base := profileLocationCtx(t)
	execProfileGql(t, base, createProfileMutation, map[string]any{"request": map[string]any{"token": "meter"}})
	execProfileGql(t, base, `mutation ($request: MetricDefinitionCreateRequest) {
	  createMetricDefinition(request: $request) { token }
	}`, map[string]any{"request": map[string]any{
		"token": "md-v", "deviceProfileToken": "meter", "metricKey": "voltage", "dataType": "DOUBLE"}})
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})

	scoped := auth.WithClaims(base, &auth.Claims{TokenType: auth.TokenTypeAccess,
		Authorities: []string{string(auth.DeviceRead)}, ScopeAnchors: []string{"customer:acme-corp"}})
	res := schema.Exec(scoped, testMetricFormulaQuery, "", nil)
	require.Len(t, res.Errors, 1)
	require.Equal(t, auth.ErrScopeUnsupported.Error(), res.Errors[0].Message,
		"a scoped member was not refused by the resolver itself")

	unauthorized := auth.WithClaims(base, &auth.Claims{TokenType: auth.TokenTypeAccess,
		Authorities: []string{string(auth.DeviceWrite)}})
	res = schema.Exec(unauthorized, testMetricFormulaQuery, "", nil)
	require.Len(t, res.Errors, 1)
	require.True(t, errors.Is(res.Errors[0].ResolverError, auth.ErrForbidden), "got %v", res.Errors[0])

	// Contrast: the same query under the fixture's unscoped device:read answers.
	execProfileGql(t, base, testMetricFormulaQuery, nil)
}

// The wire half of per-device constants: testMetricFormula takes `constants` (optional) and
// answers with the names the formula reads, under those names.
func TestGraphQLTestsAFormulaOverDeviceConstants(t *testing.T) {
	ctx := profileLocationCtx(t)
	execProfileGql(t, ctx, createProfileMutation, map[string]any{"request": map[string]any{"token": "meter"}})
	execProfileGql(t, ctx, `mutation ($request: MetricDefinitionCreateRequest) {
	  createMetricDefinition(request: $request) { token }
	}`, map[string]any{"request": map[string]any{
		"token": "md-v", "deviceProfileToken": "meter", "metricKey": "voltage", "dataType": "DOUBLE"}})

	type result struct {
		R struct {
			CompileError  *string  `json:"compileError"`
			Value         *string  `json:"value"`
			SkippedReason *string  `json:"skippedReason"`
			Constants     []string `json:"constants"`
		} `json:"testMetricFormula"`
	}
	run := func(constants string) result {
		data := execProfileGql(t, ctx, `query {
		  testMetricFormula(profileToken: "meter", expression: "voltage * device.calibration", dataType: "DOUBLE",
		    sample: [{key: "voltage", value: "100"}]`+constants+`) { compileError value skippedReason constants }
		}`, nil)
		var out result
		require.NoError(t, json.Unmarshal(data, &out))
		return out
	}
	with := run(`, constants: [{key: "calibration", value: "1.02"}]`)
	require.Nil(t, with.R.CompileError)
	require.NotNil(t, with.R.Value)
	require.Equal(t, "102", *with.R.Value)
	require.Equal(t, []string{"calibration"}, with.R.Constants)

	without := run(``)
	require.Nil(t, without.R.Value)
	require.NotNil(t, without.R.SkippedReason)
	require.Equal(t, "missing_input", *without.R.SkippedReason)
}

// A formula that reads a calculated metric answers through the schema with the chain it depends
// on and the measured metrics a sample must supply.
func TestGraphQLTestsAFormulaOverAChain(t *testing.T) {
	ctx := profileLocationCtx(t)
	execProfileGql(t, ctx, createProfileMutation, map[string]any{"request": map[string]any{"token": "meter"}})
	const create = `mutation ($request: MetricDefinitionCreateRequest) {
	  createMetricDefinition(request: $request) { metricKey }
	}`
	for _, m := range []map[string]any{
		{"token": "md-v", "metricKey": "voltage", "dataType": "DOUBLE"},
		{"token": "md-c", "metricKey": "current", "dataType": "DOUBLE"},
		{"token": "md-p", "metricKey": "power", "dataType": "DOUBLE", "expression": "voltage * current"},
		{"token": "md-k", "metricKey": "kw", "dataType": "DOUBLE", "expression": "power / 1000.0"},
	} {
		m["deviceProfileToken"] = "meter"
		execProfileGql(t, ctx, create, map[string]any{"request": m})
	}
	data := execProfileGql(t, ctx, `query {
	  testMetricFormula(profileToken: "meter", expression: "kw * 2.0", dataType: "DOUBLE",
	    sample: [{key: "voltage", value: "230"}, {key: "current", value: "10"}]) {
	    compileError value inputs chain measuredInputs
	  }
	}`, nil)
	var tested struct {
		R struct {
			CompileError   *string  `json:"compileError"`
			Value          *string  `json:"value"`
			Inputs         []string `json:"inputs"`
			Chain          []string `json:"chain"`
			MeasuredInputs []string `json:"measuredInputs"`
		} `json:"testMetricFormula"`
	}
	require.NoError(t, json.Unmarshal(data, &tested))
	require.Nil(t, tested.R.CompileError)
	require.NotNil(t, tested.R.Value)
	require.Equal(t, "4.6", *tested.R.Value)
	require.Equal(t, []string{"kw"}, tested.R.Inputs)
	require.Equal(t, []string{"power", "kw"}, tested.R.Chain)
	require.Equal(t, []string{"voltage", "current"}, tested.R.MeasuredInputs)
}
