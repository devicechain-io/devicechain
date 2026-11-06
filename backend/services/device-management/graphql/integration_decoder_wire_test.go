// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/devicechain-io/dc-device-management/model"
	dmtest "github.com/devicechain-io/dc-device-management/test"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/devicechain-io/dc-microservice/credential/credentialtest"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// integrationDecoder served end to end: the service-plane question the ingest path asks about a
// device type's payload decoder.

const wireIntegrationDecoder = `query ($i: String!, $s: String!, $t: String!) {
  integrationDecoder(integration: $i, secret: $s, deviceTypeToken: $t) {
    present input maxReadings estimatedCost profileVersionToken } }`

func newIntegrationDecoderFixture(t *testing.T) (scopeFixture, string) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	f := seedScopeFixture(t, db, &model.ProvisioningProfile{}, &model.Integration{}, &model.IntegrationDeviceType{},
		&model.DeviceAlias{}, &model.IntegrationUnregisteredDevice{}, &model.GeoFence{}, &model.GeoFenceSetVersion{},
		&model.GeoFenceGeometryBlob{}, &model.ProfileDecoderSample{}, &model.DetectionRule{}, &model.DetectionRuleScopeRef{})
	f.api.DeviceSecretKey = dmtest.DeviceSecretKey()
	f.api.EnrollmentKeys = dmtest.EnrollmentKeys()
	checker, err := credential.NewChecker(credentialtest.NewStore(), map[credential.Kind]credential.Policy{
		credential.KindIntegration: {Unthrottled: true},
	}, credential.WithIntegrationSecretKey(f.api.EnrollmentKeys.Integration))
	require.NoError(t, err)
	f.api.IntegrationChecker = checker

	ctx := f.operator()
	_, err = f.api.CreateDeviceProfile(ctx, &model.DeviceProfileCreateRequest{Token: "meter"})
	require.NoError(t, err)
	_, err = f.api.CreateMetricDefinition(ctx, &model.MetricDefinitionCreateRequest{
		Token: "temp-def", DeviceProfileToken: "meter", MetricKey: "temp", DataType: "DOUBLE"})
	require.NoError(t, err)
	_, err = f.api.UpdateDeviceType(ctx, "pump", &model.DeviceTypeUpdateRequest{ProfileToken: dcgraphql.OptionalStringOf("meter")})
	require.NoError(t, err)
	issued := serve(t, ctx, wireCreateIntegration, createIntegrationVars("chirp-1", nil)).
		data["createIntegration"].(map[string]any)["issuedToken"].(string)
	return f, issued
}

func (f scopeFixture) publishDecoder(t *testing.T, decoder string) {
	t.Helper()
	ctx := f.operator()
	if decoder == "" {
		_, err := f.api.ClearDeviceProfileDecoder(ctx, "meter")
		require.NoError(t, err)
	} else {
		_, err := f.api.SetDeviceProfileDecoder(ctx, "meter", []byte(decoder))
		require.NoError(t, err)
	}
	_, err := f.api.PublishDeviceProfile(ctx, "meter", nil, nil, "t")
	require.NoError(t, err)
}

func decoderPlanOf(t *testing.T, res served) map[string]any {
	t.Helper()
	require.Empty(t, res.errs)
	plan, _ := res.data["integrationDecoder"].(map[string]any)
	return plan
}

const wireFrameDecoder = `{"kind":"CEL","envVersion":1,"input":"FRAME","sections":[
  {"name":"t","outputs":[{"key":"temp","expr":"double(i16be(frame, 0)) / 10.0"}]}],
  "vectors":[{"name":"v","input":{"frameHex":"00D7"},"expect":{"temp":21.5}}]}`

func TestIntegrationDecoderAnswersTheActiveVersionsDecoder(t *testing.T) {
	f, issued := newIntegrationDecoderFixture(t)
	svc := service(f)
	ask := func(secret, deviceType string) served {
		return serve(t, svc, wireIntegrationDecoder, map[string]any{"i": "chirp-1", "s": secret, "t": deviceType})
	}

	// No published version yet: no decoder, said as an answer.
	none := decoderPlanOf(t, ask(issued, "pump"))
	require.NotNil(t, none)
	assert.Equal(t, false, none["present"])
	assert.Nil(t, none["input"])

	f.publishDecoder(t, wireFrameDecoder)
	got := decoderPlanOf(t, ask(issued, "pump"))
	assert.Equal(t, true, got["present"])
	assert.Equal(t, "FRAME", got["input"])
	assert.EqualValues(t, 1, got["maxReadings"])
	assert.Greater(t, got["estimatedCost"], float64(0))
	assert.Equal(t, "meter@1", got["profileVersionToken"])

	// A publish without it withdraws it; the question is answered by the active version.
	f.publishDecoder(t, "")
	gone := decoderPlanOf(t, ask(issued, "pump"))
	assert.Equal(t, false, gone["present"])
	assert.Equal(t, "", gone["profileVersionToken"])
}

// Indistinguishable failures: a wrong secret, an unknown integration and a switched-off one are
// all null, and a type the integration does not reach is "no decoder", never its decoder.
func TestIntegrationDecoderTellsAStrangerNothing(t *testing.T) {
	f, issued := newIntegrationDecoderFixture(t)
	f.publishDecoder(t, wireFrameDecoder)
	svc := service(f)
	ask := func(integration, secret, deviceType string) served {
		return serve(t, svc, wireIntegrationDecoder, map[string]any{"i": integration, "s": secret, "t": deviceType})
	}

	require.NotNil(t, decoderPlanOf(t, ask("chirp-1", issued, "pump")), "the paired positive")

	for name, res := range map[string]served{
		"a wrong secret":         ask("chirp-1", differentToken(issued), "pump"),
		"an unknown integration": ask("nope", issued, "pump"),
		"an empty secret":        ask("chirp-1", "", "pump"),
	} {
		assert.Empty(t, res.errs, name)
		assert.Nil(t, res.data["integrationDecoder"], name)
	}

	// A device type that has a decoder but is outside the integration's scope.
	ctx := f.operator()
	_, err := f.api.CreateDeviceType(ctx, &model.DeviceTypeCreateRequest{Token: "other", ProfileToken: ptr("meter")})
	require.NoError(t, err)
	outside := decoderPlanOf(t, ask("chirp-1", issued, "other"))
	require.NotNil(t, outside, "an out-of-scope type is an answer, not a failed credential")
	assert.Equal(t, false, outside["present"], "an out-of-scope type revealed its decoder")
	assert.Equal(t, false, decoderPlanOf(t, ask("chirp-1", issued, "no-such-type"))["present"])

	serve(t, ctx, `mutation { updateIntegration(token: "chirp-1", request: {enabled: false}) { token } }`, nil)
	off := ask("chirp-1", issued, "pump")
	assert.Empty(t, off.errs)
	assert.Nil(t, off.data["integrationDecoder"], "a switched-off integration was answered")
}

func ptr(s string) *string { return &s }

// A decoder this build cannot run is still PRESENT: the uplink has to reach the resolver, which
// records the failure the tenant can see, instead of being flattened here as though there were
// none.
func TestIntegrationDecoderReportsAnUnusableDecoderAsPresent(t *testing.T) {
	f, issued := newIntegrationDecoderFixture(t)
	f.publishDecoder(t, wireFrameDecoder)
	var row model.DeviceProfileVersion
	db := f.db.WithContext(f.operator())
	require.NoError(t, db.Where("version = ?", 1).First(&row).Error)
	var snap map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(row.Snapshot, &snap))
	snap["decoder"] = json.RawMessage(`{"kind":"CEL","envVersion":2,"input":"DECODED","sections":[]}`)
	raw, err := json.Marshal(snap)
	require.NoError(t, err)
	require.NoError(t, db.Model(&model.DeviceProfileVersion{}).Where("id = ?", row.ID).Update("snapshot", raw).Error)

	got := decoderPlanOf(t, serve(t, service(f), wireIntegrationDecoder, map[string]any{"i": "chirp-1", "s": issued, "t": "pump"}))
	assert.Equal(t, true, got["present"])
	assert.Equal(t, "DECODED", got["input"])
	assert.EqualValues(t, 0, got["maxReadings"])
	assert.Equal(t, "meter@1", got["profileVersionToken"])
}

// The service plane: a user's access token is refused whatever it holds.
func TestIntegrationDecoderIsServiceOnly(t *testing.T) {
	f, issued := newIntegrationDecoderFixture(t)
	f.publishDecoder(t, wireFrameDecoder)
	vars := map[string]any{"i": "chirp-1", "s": issued, "t": "pump"}

	got := serve(t, service(f), wireIntegrationDecoder, vars)
	require.Empty(t, got.errs, "the paired positive")
	require.NotNil(t, got.data["integrationDecoder"])

	refused := map[string]context.Context{
		"an access token holding device:read": f.withClaims(&auth.Claims{TokenType: auth.TokenTypeAccess,
			Tenant: scopeTenant, Authorities: []string{string(auth.DeviceRead)}}),
		"a tenant administrator": f.operator(),
		"a service token without device:read": f.withClaims(&auth.Claims{TokenType: auth.TokenTypeService,
			Tenant: scopeTenant, Authorities: []string{string(auth.EventRead)}}),
		"a scoped service token": f.withClaims(&auth.Claims{TokenType: auth.TokenTypeService, Tenant: scopeTenant,
			Authorities: []string{string(auth.DeviceRead)}, ScopeAnchors: []string{"customer:acme"}}),
	}
	for who, ctx := range refused {
		res := serve(t, ctx, wireIntegrationDecoder, vars)
		if len(res.errs) == 0 || res.data["integrationDecoder"] != nil {
			t.Errorf("integrationDecoder was admitted for %s: %v %v", who, res.data, res.errs)
		}
	}
}
