// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	gql "github.com/graph-gophers/graphql-go"
	gqlerrors "github.com/graph-gophers/graphql-go/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// The wire half of payload decoders: authoring, the test door, the function library and the
// sample boundary, through the real schema.

const wireDecoder = `{"kind":"CEL","envVersion":1,"input":"FRAME","sections":[
  {"name":"telemetry","when":"fPort == 2","outputs":[
    {"key":"temperature","expr":"double(i16be(frame, 0)) / 10.0"},
    {"key":"door","expr":"bit(frame, 2, 0)"}]}],
  "vectors":[{"name":"room","input":{"frameHex":"00D701","fPort":2},"expect":{"temperature":21.5,"door":1}}]}`

type decoderWire struct {
	t      *testing.T
	api    *model.Api
	schema *gql.Schema
}

func newDecoderWire(t *testing.T) *decoderWire {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, db.AutoMigrate(&model.DeviceProfile{}, &model.DeviceProfileVersion{}, &model.DeviceProfileActivation{},
		&model.MetricDefinition{}, &model.CommandDefinition{}, &model.DetectionRule{}, &model.DeviceType{},
		&model.DetectionRuleScopeRef{}, &model.ProfileDecoderSample{}, &model.Integration{}, &model.IntegrationDeviceType{}))
	return &decoderWire{t: t, api: model.NewApi(&rdb.RdbManager{Database: db}),
		schema: gql.MustParseSchema(SchemaContent, &SchemaResolver{})}
}

// as builds a caller of the tenant holding exactly the authorities (and scope anchors).
func (w *decoderWire) as(tenant string, anchors []string, authorities ...auth.Authority) context.Context {
	strs := make([]string, 0, len(authorities))
	for _, a := range authorities {
		strs = append(strs, string(a))
	}
	ctx := core.WithTenant(context.Background(), tenant)
	ctx = auth.WithClaims(ctx, &auth.Claims{TokenType: auth.TokenTypeAccess, Authorities: strs, ScopeAnchors: anchors})
	return context.WithValue(ctx, gqlcore.ContextApiKey, w.api)
}

func (w *decoderWire) admin(tenant string) context.Context {
	return w.as(tenant, nil, auth.DeviceRead, auth.DeviceWrite)
}

func (w *decoderWire) run(ctx context.Context, query string, vars map[string]any) (json.RawMessage, []*gqlerrors.QueryError) {
	w.t.Helper()
	resp := w.schema.Exec(ctx, query, "", vars)
	return resp.Data, resp.Errors
}

func (w *decoderWire) mustRun(ctx context.Context, query string, vars map[string]any) json.RawMessage {
	w.t.Helper()
	data, errs := w.run(ctx, query, vars)
	require.Empty(w.t, errs, "graphql errors")
	return data
}

// seed creates profile "meter" with temperature (DOUBLE) and door (INT) for the tenant.
func (w *decoderWire) seed(tenant string) {
	w.t.Helper()
	ctx := w.admin(tenant)
	w.mustRun(ctx, createProfileMutation, map[string]any{"request": map[string]any{"token": "meter"}})
	for _, m := range [][2]string{{"temperature", "DOUBLE"}, {"door", "INT"}} {
		w.mustRun(ctx, `mutation ($request: MetricDefinitionCreateRequest) { createMetricDefinition(request: $request) { token } }`,
			map[string]any{"request": map[string]any{"token": "md-" + m[0], "deviceProfileToken": "meter", "metricKey": m[0], "dataType": m[1]}})
	}
}

const setDecoderMutation = `mutation ($t: String!, $d: String!) {
  setDeviceProfileDecoder(profileToken: $t, document: $d) {
    token decoder { kind envVersion input document maxReadings estimatedCost }
  }
}`

func code(e *gqlerrors.QueryError) any {
	if e.Extensions == nil {
		return nil
	}
	return e.Extensions["code"]
}

func TestGraphQLSetsReadsAndClearsADecoder(t *testing.T) {
	w := newDecoderWire(t)
	w.seed("acme")
	ctx := w.admin("acme")

	data := w.mustRun(ctx, setDecoderMutation, map[string]any{"t": "meter", "d": wireDecoder})
	var set struct {
		P struct {
			Decoder *struct {
				Kind          string `json:"kind"`
				EnvVersion    int    `json:"envVersion"`
				Input         string `json:"input"`
				Document      string `json:"document"`
				MaxReadings   int    `json:"maxReadings"`
				EstimatedCost int    `json:"estimatedCost"`
			} `json:"decoder"`
		} `json:"setDeviceProfileDecoder"`
	}
	require.NoError(t, json.Unmarshal(data, &set))
	require.NotNil(t, set.P.Decoder)
	assert.Equal(t, "CEL", set.P.Decoder.Kind)
	assert.Equal(t, 1, set.P.Decoder.EnvVersion)
	assert.Equal(t, "FRAME", set.P.Decoder.Input)
	assert.Equal(t, 2, set.P.Decoder.MaxReadings)
	assert.Positive(t, set.P.Decoder.EstimatedCost)
	assert.JSONEq(t, wireDecoder, set.P.Decoder.Document)

	// It reads back from the profile.
	data = w.mustRun(ctx, `query { deviceProfilesByToken(tokens: ["meter"]) { decoder { kind maxReadings } } }`, nil)
	assert.Contains(t, string(data), `"maxReadings":2`)

	data = w.mustRun(ctx, `mutation { clearDeviceProfileDecoder(profileToken: "meter") { decoder { kind } } }`, nil)
	assert.Contains(t, string(data), `"decoder":null`)
}

func TestGraphQLRefusalsCarryTheirCodes(t *testing.T) {
	w := newDecoderWire(t)
	w.seed("acme")
	ctx := w.admin("acme")

	_, errs := w.run(ctx, setDecoderMutation, map[string]any{"t": "meter",
		"d": `{"kind":"CEL","envVersion":1,"input":"FRAME","sections":[{"name":"s","outputs":[{"key":"nope","expr":"1"}]}]}`})
	require.Len(t, errs, 1)
	assert.Equal(t, model.CodeInvalidDecoder, code(errs[0]))
	assert.Contains(t, errs[0].Message, "sections[0].outputs[0].key")

	_, errs = w.run(ctx, setDecoderMutation, map[string]any{"t": "meter",
		"d": `{"kind":"WASM","envVersion":1,"input":"FRAME","sections":[]}`})
	require.Len(t, errs, 1)
	assert.Equal(t, model.CodeUnsupportedDecoderKind, code(errs[0]))
}

func TestGraphQLAuthoringNeedsDeviceWriteAndAnUnscopedCaller(t *testing.T) {
	w := newDecoderWire(t)
	w.seed("acme")

	for name, ctx := range map[string]context.Context{
		"read only": w.as("acme", nil, auth.DeviceRead),
		"scoped":    w.as("acme", []string{"customer:acme-corp"}, auth.DeviceRead, auth.DeviceWrite),
	} {
		_, errs := w.run(ctx, setDecoderMutation, map[string]any{"t": "meter", "d": wireDecoder})
		require.Len(t, errs, 1, name)
		assert.True(t, errors.Is(errs[0].ResolverError, auth.ErrForbidden), "%s: %v", name, errs[0])
		_, errs = w.run(ctx, `mutation { clearDeviceProfileDecoder(profileToken: "meter") { token } }`, nil)
		require.Len(t, errs, 1, name)
	}
	// Contrast: nothing was written by the refused calls, and the admin may write.
	w.mustRun(w.admin("acme"), setDecoderMutation, map[string]any{"t": "meter", "d": wireDecoder})
}

func TestGraphQLAVersionCarriesTheDecoderFrozenAtPublish(t *testing.T) {
	w := newDecoderWire(t)
	w.seed("acme")
	ctx := w.admin("acme")
	w.mustRun(ctx, setDecoderMutation, map[string]any{"t": "meter", "d": wireDecoder})
	_, err := w.api.PublishDeviceProfile(ctx, "meter", nil, nil, "tester")
	require.NoError(t, err)
	w.mustRun(ctx, `mutation { clearDeviceProfileDecoder(profileToken: "meter") { token } }`, nil)

	data := w.mustRun(ctx, `query { deviceProfilesByToken(tokens: ["meter"]) {
	  decoder { kind }
	  activeProfileVersion { version decoder { kind input maxReadings estimatedCost document } } } }`, nil)
	var out struct {
		P []struct {
			Decoder *struct{ Kind string } `json:"decoder"`
			V       struct {
				Decoder *struct {
					Kind        string `json:"kind"`
					Input       string `json:"input"`
					MaxReadings int    `json:"maxReadings"`
					Document    string `json:"document"`
				} `json:"decoder"`
			} `json:"activeProfileVersion"`
		} `json:"deviceProfilesByToken"`
	}
	require.NoError(t, json.Unmarshal(data, &out))
	require.Len(t, out.P, 1)
	assert.Nil(t, out.P[0].Decoder, "the draft was cleared after the publish")
	require.NotNil(t, out.P[0].V.Decoder, "the published version lost its decoder")
	assert.Equal(t, "FRAME", out.P[0].V.Decoder.Input)
	assert.Equal(t, 2, out.P[0].V.Decoder.MaxReadings)
	assert.JSONEq(t, wireDecoder, out.P[0].V.Decoder.Document)
}

const testDecoderQuery = `query ($input: PayloadDecoderTestInput!) {
  testPayloadDecoder(input: $input) {
    ok ignored section uplinks cost
    readings { key value }
    skipped { key reason }
    error { reason path message offset }
    vectors { name ok error cost }
  }
}`

type testResult struct {
	R struct {
		OK       bool    `json:"ok"`
		Section  *string `json:"section"`
		Readings []struct{ Key, Value string }
		Error    *struct {
			Reason, Path, Message string
			Offset                *int
		}
		Vectors []struct {
			Name string
			OK   bool
		}
	} `json:"testPayloadDecoder"`
}

func (w *decoderWire) test(ctx context.Context, input map[string]any) testResult {
	w.t.Helper()
	var out testResult
	require.NoError(w.t, json.Unmarshal(w.mustRun(ctx, testDecoderQuery, map[string]any{"input": input}), &out))
	return out
}

func TestGraphQLTestsADecoderOverAFrame(t *testing.T) {
	w := newDecoderWire(t)
	w.seed("acme")
	ctx := w.admin("acme")
	w.mustRun(ctx, setDecoderMutation, map[string]any{"t": "meter", "d": wireDecoder})

	res := w.test(ctx, map[string]any{"profileToken": "meter", "frameHex": "00D701", "fPort": 2})
	require.True(t, res.R.OK)
	require.NotNil(t, res.R.Section)
	assert.Equal(t, "telemetry", *res.R.Section)
	assert.Equal(t, "temperature", res.R.Readings[0].Key)
	assert.Equal(t, "21.5", res.R.Readings[0].Value)
	require.Len(t, res.R.Vectors, 1)
	assert.True(t, res.R.Vectors[0].OK)

	res = w.test(ctx, map[string]any{"profileToken": "meter", "frameHex": "00D701", "fPort": 9})
	require.False(t, res.R.OK)
	require.NotNil(t, res.R.Error)
	assert.Equal(t, "no_section", res.R.Error.Reason)

	// A document that does not compile is a result, with the expression named.
	res = w.test(ctx, map[string]any{"profileToken": "meter", "frameHex": "00",
		"document": `{"kind":"CEL","envVersion":1,"input":"FRAME","sections":[{"name":"s","outputs":[{"key":"door","expr":"matches(\"a\", \"b\")"}]}]}`})
	require.False(t, res.R.OK)
	require.NotNil(t, res.R.Error)
	assert.Equal(t, "compile_failed", res.R.Error.Reason)
	assert.Contains(t, res.R.Error.Message, "sections[0].outputs[0].expr")
}

func TestGraphQLTestsAWebhookBodyWithTheFullContext(t *testing.T) {
	w := newDecoderWire(t)
	w.seed("acme")
	ctx := w.admin("acme")
	w.mustRun(ctx, setDecoderMutation, map[string]any{"t": "meter", "d": wireDecoder})
	in := &model.Integration{Type: "GENERIC_JSON", Enabled: true, Identity: "ALIAS:custom.sensor", TokenDigest: "x"}
	in.Token = "hook"
	g := []byte(`{"idFrom":"header:X-Device","framePointer":"/data","frameEncoding":"HEX"}`)
	gj := gormJSON(g)
	in.Generic = &gj
	require.NoError(t, w.api.RDB.DB(ctx).Create(in).Error)

	// The device id is in a header: without it the codec refuses, so headers reach the codec.
	body := `{"data":"00D701"}`
	res := w.test(ctx, map[string]any{"profileToken": "meter", "webhookBody": body, "integrationToken": "hook",
		"contentType": "application/json"})
	require.False(t, res.R.OK)
	require.NotNil(t, res.R.Error)
	assert.Equal(t, "input_malformed", res.R.Error.Reason)
	assert.Contains(t, res.R.Error.Message, "X-Device")

	res = w.test(ctx, map[string]any{"profileToken": "meter", "webhookBody": body, "integrationToken": "hook",
		"contentType": "application/json", "headers": []any{map[string]any{"name": "X-Device", "value": "pump-7"}}})
	require.False(t, res.R.OK, "a generic item has no fPort, so the fPort == 2 section does not match")
	assert.Equal(t, "no_section", res.R.Error.Reason)
}

func TestGraphQLServesTheFunctionLibrary(t *testing.T) {
	w := newDecoderWire(t)
	data := w.mustRun(w.as("acme", nil, auth.DeviceRead), `query { payloadDecoderFunctions { name signature description } }`, nil)
	var out struct {
		F []struct{ Name, Signature, Description string } `json:"payloadDecoderFunctions"`
	}
	require.NoError(t, json.Unmarshal(data, &out))
	names := map[string]bool{}
	for _, f := range out.F {
		names[f.Name] = true
		assert.NotEmpty(t, f.Signature, f.Name)
		assert.NotEmpty(t, f.Description, f.Name)
	}
	for _, want := range []string{"u8", "i16be", "f32le", "bit", "bcd", "slice", "lpp.decode", "hex.decode"} {
		assert.True(t, names[want], "the library does not list %s", want)
	}

	_, errs := w.run(w.as("acme", nil, auth.DeviceWrite), `query { payloadDecoderFunctions { name } }`, nil)
	require.Len(t, errs, 1)
	assert.True(t, errors.Is(errs[0].ResolverError, auth.ErrForbidden))
	_, errs = w.run(w.as("acme", []string{"customer:x"}, auth.DeviceRead), `query { payloadDecoderFunctions { name } }`, nil)
	require.Len(t, errs, 1)
}

func TestGraphQLTestDoorNeedsDeviceReadAndAnUnscopedCaller(t *testing.T) {
	w := newDecoderWire(t)
	w.seed("acme")
	input := map[string]any{"profileToken": "meter", "document": wireDecoder, "frameHex": "00D701", "fPort": 2}
	for name, ctx := range map[string]context.Context{
		"no read": w.as("acme", nil, auth.DeviceWrite),
		"scoped":  w.as("acme", []string{"customer:x"}, auth.DeviceRead),
	} {
		_, errs := w.run(ctx, testDecoderQuery, map[string]any{"input": input})
		require.Len(t, errs, 1, name)
	}
	w.mustRun(w.as("acme", nil, auth.DeviceRead), testDecoderQuery, map[string]any{"input": input})
}

func TestGraphQLTestDoorIsRateLimitedPerTenant(t *testing.T) {
	w := newDecoderWire(t)
	w.seed("acme")
	w.seed("other")
	saved := decoderTestLimiter
	decoderTestLimiter = core.NewTenantRateLimiter(core.StaticCeiling(0.001, 2))
	t.Cleanup(func() { decoderTestLimiter = saved })

	input := map[string]any{"profileToken": "meter", "document": wireDecoder}
	acme := w.as("acme", nil, auth.DeviceRead)
	w.mustRun(acme, testDecoderQuery, map[string]any{"input": input})
	w.mustRun(acme, testDecoderQuery, map[string]any{"input": input})
	_, errs := w.run(acme, testDecoderQuery, map[string]any{"input": input})
	require.Len(t, errs, 1)
	assert.Equal(t, model.CodeRateLimited, code(errs[0]))
	// One tenant's flood leaves the other's allowance alone.
	w.mustRun(w.as("other", nil, auth.DeviceRead), testDecoderQuery, map[string]any{"input": input})
}

// ---- A17 on the wire ------------------------------------------------------------------------

const probeDecoder = `{"kind":"CEL","envVersion":1,"input":"FRAME","sections":[{"name":"s","outputs":[
  {"key":"door","expr":"u8(frame, 0)"}]}]}`

func (w *decoderWire) sample(tenant string) string {
	w.t.Helper()
	ctx := w.admin(tenant)
	p, err := w.api.DeviceProfilesByToken(ctx, []string{"meter"})
	require.NoError(w.t, err)
	s := &model.ProfileDecoderSample{DeviceProfileId: p[0].ID, ProfileVersion: 1, DeviceToken: "d", Reason: "eval_error",
		Input: []byte(`{"frameHex":"2a0102","fPort":2}`)}
	s.TenantId = tenant
	require.NoError(w.t, w.api.RDB.DB(ctx).Create(s).Error)
	return fmt.Sprint(s.ID)
}

func TestGraphQLASampleIsEvaluatedOnlyForDeviceWriteOfTheOwningTenant(t *testing.T) {
	w := newDecoderWire(t)
	w.seed("acme")
	w.seed("other")
	acmeSample := w.sample("acme")
	input := func(id string) map[string]any {
		return map[string]any{"profileToken": "meter", "document": probeDecoder, "sampleId": id}
	}

	// The owning tenant's DeviceWrite caller reads byte 0 (0x2A = 42) out through evaluation.
	res := w.test(w.admin("acme"), input(acmeSample))
	require.True(t, res.R.OK)
	assert.Equal(t, "42", res.R.Readings[0].Value)

	// NEGATIVE CONTROL 1: DeviceRead alone must not extract it.
	_, errs := w.run(w.as("acme", nil, auth.DeviceRead), testDecoderQuery, map[string]any{"input": input(acmeSample)})
	require.Len(t, errs, 1)
	assert.True(t, errors.Is(errs[0].ResolverError, auth.ErrForbidden), "got %v", errs[0])

	// A scoped DeviceWrite caller is refused too.
	_, errs = w.run(w.as("acme", []string{"customer:x"}, auth.DeviceRead, auth.DeviceWrite), testDecoderQuery, map[string]any{"input": input(acmeSample)})
	require.Len(t, errs, 1)

	// NEGATIVE CONTROL 2: another tenant's DeviceWrite caller does not find it.
	_, errs = w.run(w.admin("other"), testDecoderQuery, map[string]any{"input": input(acmeSample)})
	require.Len(t, errs, 1)
	assert.True(t, errors.Is(errs[0].ResolverError, gorm.ErrRecordNotFound), "got %v", errs[0])

	// A sample id that is not a number is the caller's mistake, not a probe.
	_, errs = w.run(w.admin("acme"), testDecoderQuery, map[string]any{"input": input("abc")})
	require.Len(t, errs, 1)
}

func gormJSON(b []byte) datatypes.JSON { return datatypes.JSON(b) }
