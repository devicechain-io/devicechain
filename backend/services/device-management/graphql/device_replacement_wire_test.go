// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	gql "github.com/graph-gophers/graphql-go"
	"gorm.io/gorm"
)

// replaceDevice's guarantees split across layers, and these are the ones only the
// REAL SCHEMA can show:
//
//   - the input carries no device identity field, so a caller cannot express a
//     token/externalId/device-type move. The model test cannot see that, because
//     the Go struct simply has no such field to set — it is the SDL that has to
//     refuse it on the way in.
//   - the mutation's whole round trip actually executes: resolver → model →
//     resolvers, over a real database.
//   - the write-only rule on credentialValue survives being returned through a NEW
//     result type. The existing assertion covers the credential QUERIES; a fresh
//     door onto the same resolver is exactly where that rule gets reinstated by
//     accident.
//
// The storage-side behaviour (what is retired, what the journal records, the
// transaction) lives in model/api_device_replacement_test.go.

func newReplacementWireCtx(t *testing.T, authorities ...auth.Authority) context.Context {
	t.Helper()
	dsn := "file:" + t.Name() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	// The token grammar is registered unconditionally in production, and
	// replaceDevice MINTS a credential token — a fixture without it could not tell a
	// valid minted token from an invalid one.
	if err := rdb.RegisterTokenGrammar(db); err != nil {
		t.Fatalf("register token grammar: %v", err)
	}
	if err := db.AutoMigrate(&model.DeviceType{}, &model.Device{},
		&model.DeviceCredential{}, &model.DeviceReplacement{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	ctx := core.WithTenant(context.Background(), "acme")
	ctx = withAuthorities(ctx, authorities...)
	ctx = auth.WithClaims(ctx, &auth.Claims{
		Username:    "tech@acme.example",
		Authorities: authorityStrings(authorities),
	})
	return context.WithValue(ctx, gqlcore.ContextApiKey, model.NewApi(&rdb.RdbManager{Database: db}))
}

func authorityStrings(authorities []auth.Authority) []string {
	strs := make([]string, 0, len(authorities))
	for _, a := range authorities {
		strs = append(strs, string(a))
	}
	return strs
}

// seedReplacementWireDevice creates a device plus one live credential carrying a
// stored secret, so the write-only assertion has something that could leak.
func seedReplacementWireDevice(t *testing.T, ctx context.Context) {
	t.Helper()
	api := ctx.Value(gqlcore.ContextApiKey).(*model.Api)

	deviceType := &model.DeviceType{}
	deviceType.Token = "excavator"
	if err := api.RDB.DB(ctx).Create(deviceType).Error; err != nil {
		t.Fatalf("seed device type: %v", err)
	}
	if _, err := api.CreateDevice(ctx, &model.DeviceCreateRequest{
		Token: "dozer-01", DeviceTypeToken: "excavator",
	}); err != nil {
		t.Fatalf("seed device: %v", err)
	}
	value := "stored-secret"
	if _, err := api.CreateDeviceCredential(ctx, &model.DeviceCredentialCreateRequest{
		Token:           "dozer-01-cred",
		DeviceToken:     "dozer-01",
		CredentialType:  string(model.CredentialMqttBasic),
		CredentialId:    "dozer-01",
		CredentialValue: &value,
		Enabled:         true,
	}); err != nil {
		t.Fatalf("seed credential: %v", err)
	}
}

const replaceDeviceMutation = `mutation($request: DeviceReplaceRequest!) {
  replaceDevice(request: $request) {
    device { token externalId }
    replacement { actor reason unitIdentifier retiredCredentialTokens newCredentialToken newCredentialType }
    newCredential { token credentialType credentialId credentialValue }
    retiredCredentialTokens
  }
}`

// The whole mutation executes over a real schema and a real database, and the
// result reports the swap.
func TestReplaceDeviceRoundTrips(t *testing.T) {
	ctx := newReplacementWireCtx(t, auth.DeviceRead, auth.DeviceWrite)
	seedReplacementWireDevice(t, ctx)

	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	res := schema.Exec(ctx, replaceDeviceMutation, "", map[string]any{
		"request": map[string]any{
			"deviceToken":    "dozer-01",
			"reason":         "water ingress",
			"unitIdentifier": "SN-88213",
		},
	})
	if len(res.Errors) != 0 {
		t.Fatalf("replaceDevice failed: %v", res.Errors)
	}

	var doc struct {
		ReplaceDevice struct {
			Device      struct{ Token string }
			Replacement struct {
				Actor                   string
				Reason                  string
				UnitIdentifier          string
				RetiredCredentialTokens []string
				NewCredentialToken      string
				NewCredentialType       string
			}
			NewCredential struct {
				Token           string
				CredentialType  string
				CredentialId    string
				CredentialValue *string
			}
			RetiredCredentialTokens []string
		}
	}
	if err := json.Unmarshal(res.Data, &doc); err != nil {
		t.Fatalf("decode result: %v", err)
	}

	got := doc.ReplaceDevice
	if got.Device.Token != "dozer-01" {
		t.Errorf("device token = %q, want dozer-01: the surviving identity moved", got.Device.Token)
	}
	if got.Replacement.Actor != "tech@acme.example" {
		t.Errorf("actor = %q, want the authenticated subject", got.Replacement.Actor)
	}
	if got.Replacement.Reason != "water ingress" || got.Replacement.UnitIdentifier != "SN-88213" {
		t.Errorf("annotations not recorded: reason=%q unit=%q",
			got.Replacement.Reason, got.Replacement.UnitIdentifier)
	}
	if len(got.RetiredCredentialTokens) != 1 || got.RetiredCredentialTokens[0] != "dozer-01-cred" {
		t.Errorf("retiredCredentialTokens = %v, want [dozer-01-cred]", got.RetiredCredentialTokens)
	}
	if len(got.Replacement.RetiredCredentialTokens) != 1 ||
		got.Replacement.RetiredCredentialTokens[0] != "dozer-01-cred" {
		t.Errorf("the record's retired list = %v, want [dozer-01-cred]",
			got.Replacement.RetiredCredentialTokens)
	}
	if got.Replacement.NewCredentialToken != got.NewCredential.Token {
		t.Errorf("record names credential %q but the result minted %q",
			got.Replacement.NewCredentialToken, got.NewCredential.Token)
	}
	if got.Replacement.NewCredentialType != "ACCESS_TOKEN" {
		t.Errorf("newCredentialType = %q, want the ACCESS_TOKEN default", got.Replacement.NewCredentialType)
	}
	// The one moment the incoming unit's material is readable — the operation is
	// useless without it.
	if got.NewCredential.CredentialId == "" {
		t.Error("replaceDevice returned no credential id: the new unit cannot be programmed")
	}
}

// credentialValue stays write-only through the NEW result type. The existing
// assertion covers the three credential queries; this is a fresh door onto the same
// resolver, and the write-only rule is exactly the kind that gets reinstated by
// accident when one is added.
func TestReplaceDeviceDoesNotReturnACredentialValue(t *testing.T) {
	ctx := newReplacementWireCtx(t, auth.DeviceRead, auth.DeviceWrite)
	seedReplacementWireDevice(t, ctx)

	secret := "the-new-password"
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	res := schema.Exec(ctx, replaceDeviceMutation, "", map[string]any{
		"request": map[string]any{
			"deviceToken":     "dozer-01",
			"credentialType":  "MQTT_BASIC",
			"credentialId":    "dozer-01-replacement",
			"credentialValue": secret,
		},
	})
	if len(res.Errors) != 0 {
		t.Fatalf("replaceDevice failed: %v", res.Errors)
	}
	if strings.Contains(string(res.Data), secret) {
		t.Errorf("the submitted credentialValue was echoed back in the result: %s", res.Data)
	}
}

// The input carries no device identity field, so moving a device's token,
// externalId or type through a replacement is UNREPRESENTABLE rather than refused.
// The rejection comes from the unknown-input-field guard, which the forked
// graphql-go supplies for variables; without it these would be silently dropped and
// the caller told their replacement succeeded.
func TestReplaceDeviceCannotMoveTheIdentity(t *testing.T) {
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})

	for _, field := range []string{"token", "externalId", "deviceTypeToken", "name"} {
		t.Run(field, func(t *testing.T) {
			ctx := newReplacementWireCtx(t, auth.DeviceRead, auth.DeviceWrite)
			seedReplacementWireDevice(t, ctx)

			res := schema.Exec(ctx, replaceDeviceMutation, "", map[string]any{
				"request": map[string]any{"deviceToken": "dozer-01", field: "moved"},
			})
			if len(res.Errors) == 0 {
				t.Fatalf("a %q field on the replace input was accepted; a replacement must not be "+
					"able to move the identity it exists to preserve", field)
			}
		})
	}
}

// A read-only tenant member cannot replace a device. The gate matters more than it
// looks: the result carries the new credential's id, which for an ACCESS_TOKEN is
// the device's bearer, so a weaker gate here would be the same impersonation
// escalation the credential queries were raised to device:write to close.
func TestReplaceDeviceRefusesTheViewerBaseline(t *testing.T) {
	ctx := newReplacementWireCtx(t, viewerBaseline...)
	seedReplacementWireDevice(t, ctx)

	r := &SchemaResolver{}
	_, err := r.ReplaceDevice(ctx, struct {
		Request model.DeviceReplaceRequest
	}{Request: model.DeviceReplaceRequest{DeviceToken: "dozer-01"}})
	if err != auth.ErrForbidden {
		t.Errorf("replaceDevice answered a read-only caller with %v, want ErrForbidden", err)
	}
}

// The journal is readable at device:READ, deliberately unlike the credential
// queries next door. It names credentials only by entity token, so there is no
// bearer in it to protect — and a maintenance history an operator cannot read
// answers nobody's question.
func TestDeviceReplacementsReadableAtDeviceRead(t *testing.T) {
	ctx := newReplacementWireCtx(t, auth.DeviceRead, auth.DeviceWrite)
	seedReplacementWireDevice(t, ctx)

	r := &SchemaResolver{}
	if _, err := r.ReplaceDevice(ctx, struct {
		Request model.DeviceReplaceRequest
	}{Request: model.DeviceReplaceRequest{DeviceToken: "dozer-01"}}); err != nil {
		t.Fatalf("replace device: %v", err)
	}

	readOnly := withAuthorities(ctx, viewerBaseline...)
	found, err := r.DeviceReplacements(readOnly, struct {
		Criteria model.DeviceReplacementSearchCriteria
	}{Criteria: model.DeviceReplacementSearchCriteria{
		Pagination: rdb.Pagination{PageNumber: 1, PageSize: 10},
	}})
	if err != nil {
		t.Fatalf("deviceReplacements refused a read-only caller: %v", err)
	}
	if len(found.Results()) != 1 {
		t.Fatalf("expected 1 replacement, got %d", len(found.Results()))
	}

	// The counterweight: it is still gated. An unauthenticated caller is refused, so
	// "device:read is enough" does not mean "no authority is enough".
	anonymous := context.WithValue(context.Background(), gqlcore.ContextApiKey,
		ctx.Value(gqlcore.ContextApiKey))
	anonymous = core.WithTenant(anonymous, "acme")
	if _, err := r.DeviceReplacements(anonymous, struct {
		Criteria model.DeviceReplacementSearchCriteria
	}{Criteria: model.DeviceReplacementSearchCriteria{
		Pagination: rdb.Pagination{PageNumber: 1, PageSize: 10},
	}}); err != auth.ErrUnauthenticated {
		t.Errorf("deviceReplacements answered an anonymous caller with %v, want ErrUnauthenticated", err)
	}
}
