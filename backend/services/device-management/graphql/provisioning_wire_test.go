// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"fmt"
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

// The provisioning-profile surface served end to end: the parsed schema, the resolvers, the API
// and the store. The properties are the ones an operator and an integrator rely on: the secret
// is in the response that issued it and nowhere else, a misnamed or retired input field is a
// request error and not a half-configured profile, and every write needs device:write
// UNSCOPED.

const (
	wireCreateProfileSecret = `mutation ($request: ProvisioningProfileCreateRequest) {
  createProvisioningProfile(request: $request) {
    issuedSecret
    profile { token provisionKey strategy enabled maxDevices secretIssued provisionedDeviceCount }
  } }`
	wireRotateProfileSecret = `mutation ($token: String!) {
  rotateProvisioningProfileSecret(token: $token) { issuedSecret profile { token secretIssued } } }`
	wireReadProfile = `query ($tokens: [String!]!) {
  provisioningProfilesByToken(tokens: $tokens) { token maxDevices secretIssued provisionedDeviceCount enabled } }`
	wireRevoke = `mutation ($t: String!) { revokeProvisionedDevices(profileToken: $t) }`
	wireReset  = `mutation ($t: String!) { resetDeviceProvisioning(deviceToken: $t) }`
)

func newProvisioningWireFixture(t *testing.T) scopeFixture {
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
	f := seedScopeFixture(t, db, &model.ProvisioningProfile{}, &model.DeviceCredential{})
	f.api.DeviceSecretKey = dmtest.DeviceSecretKey()
	f.api.EnrollmentKeys = dmtest.EnrollmentKeys()
	checker, err := credential.NewChecker(credentialtest.NewStore(), map[credential.Kind]credential.Policy{
		credential.KindProvisioningProfile:   {Unthrottled: true},
		credential.KindProvisionedCredential: {Unthrottled: true},
	}, credential.WithDeviceSecretKey(f.api.DeviceSecretKey), credential.WithProvisioningSecretKey(f.api.EnrollmentKeys.Provisioning))
	if err != nil {
		t.Fatal(err)
	}
	f.api.ProvisioningChecker = checker
	return f
}

func createProfileVars(token string, extra map[string]any) map[string]any {
	req := map[string]any{"token": token, "provisionKey": "key-" + token, "strategy": "ALLOW_NEW",
		"deviceTypeToken": "pump", "enabled": true}
	for k, v := range extra {
		req[k] = v
	}
	return map[string]any{"request": req}
}

func TestCreateProvisioningProfileReturnsTheGeneratedSecretOnceAndNeverAgain(t *testing.T) {
	f := newProvisioningWireFixture(t)
	ctx := f.operator()

	created := serve(t, ctx, wireCreateProfileSecret, createProfileVars("pp-1", map[string]any{"maxDevices": 7}))
	if len(created.errs) > 0 {
		t.Fatalf("create: %v", created.errs[0].Message)
	}
	result := created.data["createProvisioningProfile"].(map[string]any)
	secret, _ := result["issuedSecret"].(string)
	if len(secret) != 43 {
		t.Fatalf("issuedSecret = %q, want 43 base64url characters", secret)
	}
	profile := result["profile"].(map[string]any)
	if profile["secretIssued"] != true || profile["maxDevices"] != float64(7) || profile["provisionedDeviceCount"] != float64(0) {
		t.Fatalf("profile = %v", profile)
	}

	// The type has no field that could return it again, so a query naming one is refused at
	// validation; and nothing a read returns contains it.
	for _, field := range []string{"provisionSecret", "issuedSecret", "provisionSecretDigest"} {
		got := serve(t, ctx, `query { provisioningProfilesByToken(tokens: ["pp-1"]) { token `+field+` } }`, nil)
		if len(got.errs) == 0 {
			t.Fatalf("ProvisioningProfile.%s is selectable", field)
		}
	}
	read := serve(t, ctx, wireReadProfile, map[string]any{"tokens": []any{"pp-1"}})
	if len(read.errs) > 0 || strings.Contains(toJSON(t, read.data), secret) {
		t.Fatalf("a read returned the secret or failed: %v %v", read.errs, read.data)
	}

	// The input no longer has a provisionSecret: naming one is a request error, not a profile
	// created with a secret somebody chose.
	for _, op := range []string{wireCreateProfileSecret} {
		bad := serve(t, ctx, op, createProfileVars("pp-2", map[string]any{"provisionSecret": "chosen-by-me"}))
		if len(bad.errs) == 0 {
			t.Fatal("createProvisioningProfile accepted a provisionSecret")
		}
	}
	upd := serve(t, ctx, `mutation { updateProvisioningProfile(token: "pp-1", request: {provisionSecret: "x"}) { token } }`, nil)
	if len(upd.errs) == 0 {
		t.Fatal("updateProvisioningProfile accepted a provisionSecret")
	}
}

func TestRotateProvisioningProfileSecretOverTheWire(t *testing.T) {
	f := newProvisioningWireFixture(t)
	ctx := f.operator()
	first := serve(t, ctx, wireCreateProfileSecret, createProfileVars("pp-1", nil)).data["createProvisioningProfile"].(map[string]any)["issuedSecret"].(string)

	rotated := serve(t, ctx, wireRotateProfileSecret, map[string]any{"token": "pp-1"})
	if len(rotated.errs) > 0 {
		t.Fatalf("rotate: %v", rotated.errs[0].Message)
	}
	got := rotated.data["rotateProvisioningProfileSecret"].(map[string]any)
	second := got["issuedSecret"].(string)
	if len(second) != 43 || second == first {
		t.Fatalf("rotation returned %q after %q", second, first)
	}
	if got["profile"].(map[string]any)["secretIssued"] != true {
		t.Fatal("a rotation did not mark the secret as issued")
	}
	// The old secret no longer authenticates; the new one does.
	op := f.operator()
	if _, err := f.api.AuthenticateProvisioningProfile(op, "key-pp-1", first); err == nil {
		t.Fatal("the old secret authenticates after a rotation")
	}
	if _, err := f.api.AuthenticateProvisioningProfile(op, "key-pp-1", second); err != nil {
		t.Fatalf("the rotated secret does not authenticate: %v", err)
	}
	if bad := serve(t, ctx, wireRotateProfileSecret, map[string]any{"token": "nope"}); len(bad.errs) == 0 {
		t.Fatal("rotating an unknown profile succeeded")
	}
}

// Every write needs device:write UNSCOPED: a viewer, an anonymous caller and a customer-scoped
// member holding the authority are all refused, and nothing is written.
func TestProvisioningWritesNeedUnscopedDeviceWrite(t *testing.T) {
	f := newProvisioningWireFixture(t)
	admin := f.operator()
	serve(t, admin, wireCreateProfileSecret, createProfileVars("pp-1", nil))

	scopedWriter := f.withClaims(&auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: scopeTenant,
		Authorities: []string{string(auth.DeviceRead), string(auth.DeviceWrite)}, ScopeAnchors: []string{"customer:acme"}})
	viewer := f.withClaims(&auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: scopeTenant,
		Authorities: []string{string(auth.DeviceRead)}})

	calls := map[string]struct {
		query string
		vars  map[string]any
	}{
		"create": {wireCreateProfileSecret, createProfileVars("pp-9", nil)},
		"rotate": {wireRotateProfileSecret, map[string]any{"token": "pp-1"}},
		"revoke": {wireRevoke, map[string]any{"t": "pp-1"}},
		"reset":  {wireReset, map[string]any{"t": "acme-pump"}},
	}
	for name, call := range calls {
		for who, ctx := range map[string]context.Context{"viewer": viewer, "scoped writer": scopedWriter} {
			if got := serve(t, ctx, call.query, call.vars); len(got.errs) == 0 {
				t.Errorf("%s was admitted for a %s", name, who)
			}
		}
		if got := serve(t, admin, call.query, call.vars); len(got.errs) > 0 && name != "reset" {
			t.Errorf("the control: %s was refused for the administrator: %v", name, got.errs[0].Message)
		}
	}
	rows, err := f.api.ProvisioningProfilesByToken(admin, []string{"pp-9"})
	if err != nil || len(rows) != 1 {
		t.Fatalf("only the administrator's create should have landed: %v %v", rows, err)
	}
}

func TestEarmarkAndProvenanceOverTheWire(t *testing.T) {
	f := newProvisioningWireFixture(t)
	admin := f.operator()
	serve(t, admin, wireCreateProfileSecret, createProfileVars("pp-1", nil))

	made := serve(t, admin, `mutation ($r: DeviceCreateRequest) { createDevice(request: $r) {
	  token expectedProvisioningProfile { token provisionKey } } }`, map[string]any{"r": map[string]any{
		"token": "early", "deviceTypeToken": "pump", "externalId": "SN-E", "provisioningProfileToken": "pp-1"}})
	if len(made.errs) > 0 {
		t.Fatalf("createDevice: %v", made.errs[0].Message)
	}
	dev := made.data["createDevice"].(map[string]any)
	if ep, _ := dev["expectedProvisioningProfile"].(map[string]any); ep == nil || ep["token"] != "pp-1" {
		t.Fatalf("expectedProvisioningProfile = %v", dev["expectedProvisioningProfile"])
	}

	// A profile that does not exist, or is of another type, is INVALID_VALUE.
	bad := serve(t, admin, `mutation ($r: DeviceCreateRequest) { createDevice(request: $r) { token } }`,
		map[string]any{"r": map[string]any{"token": "x", "deviceTypeToken": "pump", "provisioningProfileToken": "nope"}})
	if len(bad.errs) == 0 || bad.errs[0].Extensions["code"] != "INVALID_VALUE" {
		t.Fatalf("an unknown earmark: %v", bad.errs)
	}

	// A device the profile registers carries the provenance on its credential.
	p, err := f.api.AuthenticateProvisioningProfile(admin, "key-pp-1", secretOf(t, f, admin, "pp-1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.api.ProvisionDevice(admin, &model.ProvisionRequest{Profile: p, ExternalId: "SN-E", CredentialSecret: strings.Repeat("a", 40)}); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	creds := serve(t, admin, `query { deviceCredentials(criteria: {pageNumber: 1, pageSize: 10, device: "early"}) {
	  results { token enabled provisioningProfile { token } } } }`, nil)
	if len(creds.errs) > 0 {
		t.Fatalf("deviceCredentials: %v", creds.errs[0].Message)
	}
	rows := creds.data["deviceCredentials"].(map[string]any)["results"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["provisioningProfile"].(map[string]any)["token"] != "pp-1" {
		t.Fatalf("credential provenance = %v", rows)
	}

	// The filters.
	for _, filter := range []string{`provisioningProfile: "pp-1"`, `expectedProvisioningProfile: "pp-1"`} {
		got := serve(t, admin, `query { devices(criteria: {pageNumber: 1, pageSize: 10, `+filter+`}) { results { token } } }`, nil)
		if len(got.errs) > 0 {
			t.Fatalf("devices(%s): %v", filter, got.errs[0].Message)
		}
		res := got.data["devices"].(map[string]any)["results"].([]any)
		if len(res) != 1 || res[0].(map[string]any)["token"] != "early" {
			t.Fatalf("devices(%s) = %v", filter, res)
		}
	}

	// Profile bookkeeping reads back, and revoke and reset take effect through the wire.
	read := serve(t, admin, wireReadProfile, map[string]any{"tokens": []any{"pp-1"}})
	pr := read.data["provisioningProfilesByToken"].([]any)[0].(map[string]any)
	if pr["provisionedDeviceCount"] != float64(1) {
		t.Fatalf("provisionedDeviceCount = %v", pr["provisionedDeviceCount"])
	}
	if got := serve(t, admin, wireRevoke, map[string]any{"t": "pp-1"}); len(got.errs) > 0 || got.data["revokeProvisionedDevices"] != float64(1) {
		t.Fatalf("revoke = %v %v", got.data, got.errs)
	}
	del := serve(t, admin, `mutation { deleteProvisioningProfile(token: "pp-1") }`, nil)
	if len(del.errs) == 0 || del.errs[0].Extensions["code"] != "REFERENCE_VIOLATION" ||
		!strings.Contains(del.errs[0].Message, "revoke the profile's devices first") {
		t.Fatalf("deleting a profile that issued credentials: %v", del.errs)
	}
	if got := serve(t, admin, wireReset, map[string]any{"t": "early"}); len(got.errs) > 0 || got.data["resetDeviceProvisioning"] != float64(1) {
		t.Fatalf("reset = %v %v", got.data, got.errs)
	}
	if got := serve(t, admin, `mutation { deleteProvisioningProfile(token: "pp-1") }`, nil); len(got.errs) > 0 || got.data["deleteProvisioningProfile"] != true {
		t.Fatalf("delete after reset = %v %v", got.data, got.errs)
	}
}

// secretOf rotates the profile and returns the new secret, standing in for an operator who
// kept the one create returned.
func secretOf(t *testing.T, f scopeFixture, ctx context.Context, token string) string {
	t.Helper()
	rotated, err := f.api.RotateProvisioningProfileSecret(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	return rotated.IssuedSecret
}

// A scoped caller is refused the earmark and provenance fields rather than handed "none".
func TestAScopedCallerCannotReadAProfileThroughADeviceOrACredential(t *testing.T) {
	f := newProvisioningWireFixture(t)
	admin := f.operator()
	serve(t, admin, wireCreateProfileSecret, createProfileVars("pp-1", nil))
	serve(t, admin, `mutation ($r: DeviceCreateRequest) { createDevice(request: $r) { token } }`, map[string]any{"r": map[string]any{
		"token": "early", "deviceTypeToken": "pump", "provisioningProfileToken": "pp-1"}})
	// The device is acme's, so the member can see it; only the profile behind it is off limits.
	assigned, err := f.api.EnsureAssignmentType(admin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.api.CreateEntityRelationship(admin, &model.EntityRelationshipCreateRequest{
		Token: "early-to-acme", SourceType: "device", Source: "early",
		TargetType: "customer", Target: "acme", RelationshipType: assigned.Token,
	}); err != nil {
		t.Fatal(err)
	}

	const q = `query { devicesByToken(tokens: ["early"]) { token expectedProvisioningProfile { token } } }`
	if got := serve(t, admin, q, nil); len(got.errs) > 0 {
		t.Fatalf("the control: %v", got.errs[0].Message)
	}
	member := f.withClaims(&auth.Claims{TokenType: auth.TokenTypeAccess, Tenant: scopeTenant,
		Authorities: []string{string(auth.DeviceRead)}, ScopeAnchors: []string{"customer:acme"}})
	// The control: the same member reads the device itself.
	if own := serve(t, member, `query { devicesByToken(tokens: ["early"]) { token } }`, nil); len(own.errs) > 0 ||
		len(own.data["devicesByToken"].([]any)) != 1 {
		t.Fatalf("the member cannot see its own device: %v %v", own.errs, own.data)
	}
	got := serve(t, member, q, nil)
	if len(got.errs) == 0 {
		t.Fatalf("a scoped caller read the profile behind a device: %v", got.data)
	}
	for _, e := range got.errs {
		if strings.Contains(e.Message, "pp-1") {
			t.Fatalf("an error leaked the profile: %s", e.Message)
		}
	}
}

func toJSON(t *testing.T, v any) string {
	t.Helper()
	return fmt.Sprint(v)
}
