// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-device-management/model"
	dmtest "github.com/devicechain-io/dc-device-management/test"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/devicechain-io/dc-microservice/credential/credentialtest"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// The integration surface served end to end: the parsed schema, the resolvers, the API and the
// store. The properties an operator and an ingest path rely on: the bearer token is in the
// response that issued it and nowhere else, a misnamed or retired input field is a request
// error, integrationAuth answers null to every failure alike, and every operation needs
// device:write (device:read to read) UNSCOPED.

const (
	wireCreateIntegration = `mutation ($request: IntegrationCreateRequest!) {
  createIntegration(request: $request) {
    issuedToken
    integration { token type enabled identity linkMetrics tokenIssuedAt undecodedCount deviceTypes { token } }
  } }`
	wireRotateIntegration = `mutation ($token: String!) {
  rotateIntegrationToken(token: $token) { issuedToken integration { token } } }`
	wireIntegrationAuth = `query ($t: String!, $s: String!) {
  integrationAuth(token: $t, secret: $s) { token enabled type deviceTypes { token } } }`
	wireReadIntegration   = `query ($t: String!) { integration(token: $t) { token enabled deviceTypes { token } } }`
	wireListIntegrations  = `query { integrations(criteria: {pageNumber: 1, pageSize: 10}) { results { token } } }`
	wireDeleteIntegration = `mutation ($t: String!) { deleteIntegration(token: $t) }`
)

func newIntegrationWireFixture(t *testing.T) scopeFixture {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatal(err)
	}
	f := seedScopeFixture(t, db, &model.ProvisioningProfile{}, &model.Integration{}, &model.IntegrationDeviceType{},
		&model.DeviceAlias{}, &model.IntegrationUnregisteredDevice{})
	f.api.DeviceSecretKey = dmtest.DeviceSecretKey()
	f.api.EnrollmentKeys = dmtest.EnrollmentKeys()
	checker, err := credential.NewChecker(credentialtest.NewStore(), map[credential.Kind]credential.Policy{
		credential.KindIntegration: {Unthrottled: true},
	}, credential.WithIntegrationSecretKey(f.api.EnrollmentKeys.Integration))
	if err != nil {
		t.Fatal(err)
	}
	f.api.IntegrationChecker = checker
	return f
}

func createIntegrationVars(token string, extra map[string]any) map[string]any {
	req := map[string]any{"token": token, "type": "CHIRPSTACK_V4", "enabled": true, "deviceTypeTokens": []any{"pump"}}
	for k, v := range extra {
		req[k] = v
	}
	return map[string]any{"request": req}
}

func TestCreateIntegrationReturnsTheTokenOnceAndNeverAgain(t *testing.T) {
	f := newIntegrationWireFixture(t)
	ctx := f.operator()

	created := serve(t, ctx, wireCreateIntegration, createIntegrationVars("chirp-1", map[string]any{"linkMetrics": true}))
	if len(created.errs) > 0 {
		t.Fatalf("create: %v", created.errs[0].Message)
	}
	result := created.data["createIntegration"].(map[string]any)
	issued, _ := result["issuedToken"].(string)
	if len(issued) != 43 {
		t.Fatalf("issuedToken = %q, want 43 base64url characters", issued)
	}
	integration := result["integration"].(map[string]any)
	if integration["type"] != "CHIRPSTACK_V4" || integration["identity"] != "DEV_EUI" || integration["linkMetrics"] != true ||
		integration["enabled"] != true || integration["undecodedCount"] != float64(0) || integration["tokenIssuedAt"] == "" {
		t.Fatalf("integration = %v", integration)
	}
	scope := integration["deviceTypes"].([]any)
	if len(scope) != 1 || scope[0].(map[string]any)["token"] != "pump" {
		t.Fatalf("deviceTypes = %v", scope)
	}

	// The type has no field that could return the token again, and nothing a read returns
	// contains it.
	for _, field := range []string{"issuedToken", "tokenDigest", "secret", "bearerToken"} {
		got := serve(t, ctx, `query { integration(token: "chirp-1") { token `+field+` } }`, nil)
		if len(got.errs) == 0 {
			t.Fatalf("Integration.%s is selectable", field)
		}
	}
	read := serve(t, ctx, wireReadIntegration, map[string]any{"t": "chirp-1"})
	if len(read.errs) > 0 || strings.Contains(toJSON(t, read.data), issued) {
		t.Fatalf("a read returned the token or failed: %v %v", read.errs, read.data)
	}

	// A misnamed or retired field is a request error, not a half-configured integration.
	for _, bad := range []map[string]any{{"secret": "chosen-by-me"}, {"deviceTypes": []any{"pump"}}, {"deviceTypeToken": "pump"}} {
		if got := serve(t, ctx, wireCreateIntegration, createIntegrationVars("chirp-2", bad)); len(got.errs) == 0 {
			t.Fatalf("createIntegration accepted %v", bad)
		}
	}
	for _, field := range []string{`token: "x"`, `type: "TTS_V3"`, `tokenDigest: "x"`} {
		if got := serve(t, ctx, `mutation { updateIntegration(token: "chirp-1", request: {`+field+`}) { token } }`, nil); len(got.errs) == 0 {
			t.Fatalf("updateIntegration accepted %s", field)
		}
	}
	rows, err := f.api.IntegrationsByToken(ctx, []string{"chirp-2"})
	if err != nil || len(rows) != 0 {
		t.Fatalf("a refused create left a row: %v %v", rows, err)
	}
}

// integrationAuth answers null to every failure, the integration to a token that matches, and
// reports a disabled one as disabled.
func TestIntegrationAuthOverTheWire(t *testing.T) {
	f := newIntegrationWireFixture(t)
	ctx := f.operator()
	issued := serve(t, ctx, wireCreateIntegration, createIntegrationVars("chirp-1", nil)).
		data["createIntegration"].(map[string]any)["issuedToken"].(string)

	ok := serve(t, service(f), wireIntegrationAuth, map[string]any{"t": "chirp-1", "s": issued})
	if len(ok.errs) > 0 {
		t.Fatalf("integrationAuth: %v", ok.errs[0].Message)
	}
	got, _ := ok.data["integrationAuth"].(map[string]any)
	if got == nil || got["token"] != "chirp-1" || got["enabled"] != true || got["type"] != "CHIRPSTACK_V4" {
		t.Fatalf("the right token: %v", ok.data)
	}

	for name, vars := range map[string]map[string]any{
		"a wrong token":          {"t": "chirp-1", "s": differentToken(issued)},
		"an empty token":         {"t": "chirp-1", "s": ""},
		"an unknown integration": {"t": "nope", "s": issued},
	} {
		res := serve(t, service(f), wireIntegrationAuth, vars)
		if len(res.errs) > 0 || res.data["integrationAuth"] != nil {
			t.Errorf("%s: want null and no error, got %v %v", name, res.data, res.errs)
		}
	}

	serve(t, ctx, `mutation { updateIntegration(token: "chirp-1", request: {enabled: false}) { token } }`, nil)
	res := serve(t, service(f), wireIntegrationAuth, map[string]any{"t": "chirp-1", "s": issued})
	disabled, _ := res.data["integrationAuth"].(map[string]any)
	if len(res.errs) > 0 || disabled == nil || disabled["enabled"] != false {
		t.Fatalf("a disabled integration with its token: %v %v", res.data, res.errs)
	}
	res = serve(t, service(f), wireIntegrationAuth, map[string]any{"t": "chirp-1", "s": "wrong"})
	if len(res.errs) > 0 || res.data["integrationAuth"] != nil {
		t.Fatalf("a disabled integration without its token must look like any failure: %v %v", res.data, res.errs)
	}
}

func TestRotateIntegrationTokenOverTheWire(t *testing.T) {
	f := newIntegrationWireFixture(t)
	ctx := f.operator()
	first := serve(t, ctx, wireCreateIntegration, createIntegrationVars("chirp-1", nil)).
		data["createIntegration"].(map[string]any)["issuedToken"].(string)

	rotated := serve(t, ctx, wireRotateIntegration, map[string]any{"token": "chirp-1"})
	if len(rotated.errs) > 0 {
		t.Fatalf("rotate: %v", rotated.errs[0].Message)
	}
	second := rotated.data["rotateIntegrationToken"].(map[string]any)["issuedToken"].(string)
	if len(second) != 43 || second == first {
		t.Fatalf("rotation returned %q after %q", second, first)
	}
	if res := serve(t, service(f), wireIntegrationAuth, map[string]any{"t": "chirp-1", "s": first}); res.data["integrationAuth"] != nil {
		t.Fatal("the old token authenticates after a rotation")
	}
	if res := serve(t, service(f), wireIntegrationAuth, map[string]any{"t": "chirp-1", "s": second}); res.data["integrationAuth"] == nil {
		t.Fatal("the rotated token does not authenticate")
	}
	if bad := serve(t, ctx, wireRotateIntegration, map[string]any{"token": "nope"}); len(bad.errs) == 0 {
		t.Fatal("rotating an unknown integration succeeded")
	}
}

// Every write needs device:write UNSCOPED and every read device:read unscoped: an anonymous
// caller, a viewer (writes) and a customer-scoped member holding the authority are refused.
func TestIntegrationsNeedUnscopedDeviceAuthorities(t *testing.T) {
	f := newIntegrationWireFixture(t)
	admin := f.operator()
	serve(t, admin, wireCreateIntegration, createIntegrationVars("chirp-1", nil))

	scoped := f.withClaims(&auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: scopeTenant,
		Authorities: []string{string(auth.DeviceRead), string(auth.DeviceWrite)}, ScopeAnchors: []string{"customer:acme"}})
	viewer := f.withClaims(&auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: scopeTenant,
		Authorities: []string{string(auth.DeviceRead)}})
	nothing := f.withClaims(&auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: scopeTenant})

	type call struct {
		query string
		vars  map[string]any
	}
	writes := map[string]call{
		"create": {wireCreateIntegration, createIntegrationVars("chirp-9", nil)},
		"update": {`mutation { updateIntegration(token: "chirp-1", request: {name: "x"}) { token } }`, nil},
		"rotate": {wireRotateIntegration, map[string]any{"token": "chirp-1"}},
		"delete": {wireDeleteIntegration, map[string]any{"t": "chirp-1"}},
	}
	for name, c := range writes {
		for who, ctx := range map[string]context.Context{"viewer": viewer, "scoped writer": scoped} {
			if got := serve(t, ctx, c.query, c.vars); len(got.errs) == 0 {
				t.Errorf("%s was admitted for a %s", name, who)
			}
		}
	}
	reads := map[string]call{
		"integration":  {wireReadIntegration, map[string]any{"t": "chirp-1"}},
		"integrations": {wireListIntegrations, nil},
	}
	for name, c := range reads {
		for who, ctx := range map[string]context.Context{"scoped reader": scoped, "no authority": nothing} {
			if got := serve(t, ctx, c.query, c.vars); len(got.errs) == 0 {
				t.Errorf("%s was admitted for a caller with %s", name, who)
			}
		}
		if got := serve(t, viewer, c.query, c.vars); len(got.errs) > 0 {
			t.Errorf("the control: %s was refused for a viewer holding device:read: %v", name, got.errs[0].Message)
		}
	}
	// Nothing the refused writes attempted landed.
	rows, err := f.api.IntegrationsByToken(admin, []string{"chirp-9", "chirp-1"})
	if err != nil || len(rows) != 1 || rows[0].Token != "chirp-1" || rows[0].Name.Valid {
		t.Fatalf("only the first integration, untouched, should exist: %v %v", rows, err)
	}
	// The control: the administrator is admitted to every write.
	for _, name := range []string{"create", "update", "rotate", "delete"} { // delete last: the rest need the row
		c := writes[name]
		if got := serve(t, admin, c.query, c.vars); len(got.errs) > 0 {
			t.Errorf("the control: %s was refused for the administrator: %v", name, got.errs[0].Message)
		}
	}
}

// The tenant's tier bounds the count, and the refusal carries LIMIT_EXCEEDED.
func TestCreateIntegrationOverTheTierCeilingIsLimitExceeded(t *testing.T) {
	f := newIntegrationWireFixture(t)
	f.api.IntegrationsCeiling = ceilingOf(1)
	admin := f.operator()
	if got := serve(t, admin, wireCreateIntegration, createIntegrationVars("one", nil)); len(got.errs) > 0 {
		t.Fatalf("the first: %v", got.errs[0].Message)
	}
	got := serve(t, admin, wireCreateIntegration, createIntegrationVars("two", nil))
	if len(got.errs) == 0 || got.errs[0].Extensions["code"] != "LIMIT_EXCEEDED" {
		t.Fatalf("the second: %v", got.errs)
	}
}

// A device type an integration is scoped to is refused deletion with REFERENCE_VIOLATION.
func TestDeletingAScopedDeviceTypeIsAReferenceViolation(t *testing.T) {
	f := newIntegrationWireFixture(t)
	admin := f.operator()
	if got := serve(t, admin, `mutation { createDeviceType(request: {token: "valve"}) { token } }`, nil); len(got.errs) > 0 {
		t.Fatalf("device type: %v", got.errs[0].Message)
	}
	serve(t, admin, wireCreateIntegration, createIntegrationVars("chirp-1", map[string]any{"deviceTypeTokens": []any{"valve"}}))

	del := serve(t, admin, `mutation { deleteDeviceType(token: "valve") }`, nil)
	if len(del.errs) == 0 || del.errs[0].Extensions["code"] != "REFERENCE_VIOLATION" ||
		!strings.Contains(del.errs[0].Message, "integration(s) are scoped to device type") {
		t.Fatalf("deleting a scoped device type: %v", del.errs)
	}
	serve(t, admin, wireDeleteIntegration, map[string]any{"t": "chirp-1"})
	if got := serve(t, admin, `mutation { deleteDeviceType(token: "valve") }`, nil); len(got.errs) > 0 || got.data["deleteDeviceType"] != true {
		t.Fatalf("delete after the integration is gone: %v %v", got.data, got.errs)
	}
}

// service is a caller holding a service token with device:read, the only kind of caller
// integrationAuth admits.
func service(f scopeFixture) context.Context {
	return f.withClaims(&auth.Claims{TokenType: auth.TokenTypeService, Tenant: scopeTenant,
		Authorities: []string{string(auth.DeviceRead)}})
}

// integrationAuth is the service plane: a user's access token is refused whatever it holds,
// a service token needs the authority, and only the combination is admitted.
func TestIntegrationAuthIsServiceOnly(t *testing.T) {
	f := newIntegrationWireFixture(t)
	admin := f.operator()
	issued := serve(t, admin, wireCreateIntegration, createIntegrationVars("chirp-1", nil)).
		data["createIntegration"].(map[string]any)["issuedToken"].(string)
	vars := map[string]any{"t": "chirp-1", "s": issued}

	// The paired positive.
	if got := serve(t, service(f), wireIntegrationAuth, vars); len(got.errs) > 0 || got.data["integrationAuth"] == nil {
		t.Fatalf("a service token with device:read: %v %v", got.data, got.errs)
	}
	refused := map[string]context.Context{
		"an access token holding device:read": f.withClaims(&auth.Claims{TokenType: auth.TokenTypeAccess,
			Tenant: scopeTenant, Authorities: []string{string(auth.DeviceRead)}}),
		"a tenant administrator": admin,
		"a service token without device:read": f.withClaims(&auth.Claims{TokenType: auth.TokenTypeService,
			Tenant: scopeTenant, Authorities: []string{string(auth.EventRead)}}),
		"a scoped service token": f.withClaims(&auth.Claims{TokenType: auth.TokenTypeService, Tenant: scopeTenant,
			Authorities: []string{string(auth.DeviceRead)}, ScopeAnchors: []string{"customer:acme"}}),
	}
	for who, ctx := range refused {
		got := serve(t, ctx, wireIntegrationAuth, vars)
		if len(got.errs) == 0 || got.data["integrationAuth"] != nil {
			t.Errorf("integrationAuth was admitted for %s: %v %v", who, got.data, got.errs)
		}
	}
}

// differentToken returns a token of the same shape that is certainly not the one given. Issued
// tokens are random base64url, so "overwrite the first character with a fixed one" is the
// original again one time in 64.
func differentToken(token string) string {
	first := byte('A')
	if token[0] == first {
		first = 'B'
	}
	return string(first) + token[1:]
}
