// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
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

// The authorities every enabled tenant member receives whether or not they hold
// any role — user-management's viewer baseline (identity.viewerAuthorities). It
// is spelled out here rather than imported because it lives in another module;
// a test over there asserts the baseline carries no write authority, which is
// what keeps this list an accurate stand-in for "a read-only user".
var viewerBaseline = []auth.Authority{
	auth.DeviceRead, auth.EventRead, auth.StateRead, auth.CommandRead, auth.AlarmRead,
	auth.DashboardRead,
}

// credentialTestCtx builds a context carrying a real sqlite-backed device-management
// Api and a tenant — what the credential resolvers read out of context once past
// the gate. It carries NO claims, which is how an unauthenticated request arrives;
// authorities are layered on with withAuthorities. The Api is real (not a stub) so
// an authorized call runs the whole query path and returns rows rather than proving
// only that it got past the gate.
func credentialTestCtx(t *testing.T) context.Context {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := rdb.RegisterTenantScoping(db); err != nil {
		t.Fatalf("register tenant scoping: %v", err)
	}
	if err := db.AutoMigrate(&model.Device{}, &model.DeviceCredential{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	ctx := core.WithTenant(context.Background(), "acme")
	return context.WithValue(ctx, gqlcore.ContextApiKey, model.NewApi(&rdb.RdbManager{Database: db}))
}

// withAuthorities layers a claims set onto ctx, as the GraphQL auth middleware
// would populate it from a validated bearer token.
func withAuthorities(ctx context.Context, authorities ...auth.Authority) context.Context {
	strs := make([]string, 0, len(authorities))
	for _, a := range authorities {
		strs = append(strs, string(a))
	}
	return auth.WithClaims(ctx, &auth.Claims{Authorities: strs})
}

// seedAccessTokenCredential writes one ACCESS_TOKEN credential whose credentialId
// is the bearer the device authenticates with — the material this authority gate
// exists to protect. It also stores a credentialValue so the write-only assertion
// has something that could leak.
func seedAccessTokenCredential(t *testing.T, ctx context.Context) *model.DeviceCredential {
	t.Helper()
	api := ctx.Value(gqlcore.ContextApiKey).(*model.Api)

	device := &model.Device{}
	device.Token = "dozer-01"
	if err := api.RDB.DB(ctx).Create(device).Error; err != nil {
		t.Fatalf("seed device: %v", err)
	}

	cred := &model.DeviceCredential{
		DeviceId:        device.ID,
		CredentialType:  string(model.CredentialAccessToken),
		CredentialId:    "the-bearer-5f989616",
		CredentialValue: sql.NullString{String: "stored-secret", Valid: true},
		Enabled:         true,
	}
	cred.Token = "dozer-01-cred"
	if err := api.RDB.DB(ctx).Create(cred).Error; err != nil {
		t.Fatalf("seed credential: %v", err)
	}
	return cred
}

// callAllThree invokes each of the three credential queries against ctx and hands
// each (name, error) pair to check. Keeping them in one table matters: they are
// three doors onto the same material, and a fix applied to two of the three is the
// exact shape of the defect this test exists to prevent.
func callAllThree(t *testing.T, ctx context.Context, id string, check func(name string, err error)) {
	t.Helper()
	r := &SchemaResolver{}

	_, err := r.DeviceCredentialsById(ctx, struct{ Ids []string }{Ids: []string{id}})
	check("deviceCredentialsById", err)

	_, err = r.DeviceCredentialsByToken(ctx, struct{ Tokens []string }{Tokens: []string{"dozer-01-cred"}})
	check("deviceCredentialsByToken", err)

	_, err = r.DeviceCredentials(ctx, struct {
		Criteria model.DeviceCredentialSearchCriteria
	}{Criteria: model.DeviceCredentialSearchCriteria{Pagination: rdb.Pagination{PageNumber: 1, PageSize: 100}}})
	check("deviceCredentials", err)
}

// A read-only tenant member is refused by all three credential queries.
//
// This is the property the whole change exists for. device:read is not a
// privileged authority — every enabled tenant member holds it — and for an
// ACCESS_TOKEN the readable credentialId IS the bearer, so admitting a viewer
// here handed a read-only role the ability to impersonate any device in the
// tenant. The caller is given the FULL viewer baseline, not a bare device:read,
// so the test cannot pass merely because some other read authority was missing.
func TestCredentialQueriesRefuseTheViewerBaseline(t *testing.T) {
	ctx := withAuthorities(credentialTestCtx(t), viewerBaseline...)
	cred := seedAccessTokenCredential(t, ctx)

	callAllThree(t, ctx, fmt.Sprint(cred.ID), func(name string, err error) {
		if err == nil {
			t.Errorf("%s admitted a read-only caller: a viewer can read device bearer credentials", name)
			return
		}
		if err != auth.ErrForbidden {
			t.Errorf("%s refused a viewer with %v, want ErrForbidden", name, err)
		}
	})
}

// An unauthenticated caller is refused too, and is distinguishable from a caller
// who authenticated but lacks the authority.
func TestCredentialQueriesRefuseAnAnonymousCaller(t *testing.T) {
	// No claims layered on at all, as an unauthenticated request arrives.
	ctx := credentialTestCtx(t)
	cred := seedAccessTokenCredential(t, ctx)

	callAllThree(t, ctx, fmt.Sprint(cred.ID), func(name string, err error) {
		if err != auth.ErrUnauthenticated {
			t.Errorf("%s answered an anonymous caller with %v, want ErrUnauthenticated", name, err)
		}
	})
}

// device:write reads credentials, and gets the bearer back.
//
// The counterweight to the refusal tests: gating on device:write is only correct
// while the callers that legitimately manage credentials — the console's
// credentials panel, dcctl, a scenario bootstrap — still work. A gate that
// refused everyone would pass every negative test above.
//
// device:write is the right level precisely because it confers nothing new:
// createDeviceCredential is already device:write, so that holder could mint a
// bearer for any device in the tenant regardless of what this query returns.
func TestDeviceWriteReadsTheCredentialBearer(t *testing.T) {
	ctx := withAuthorities(credentialTestCtx(t), append(viewerBaseline, auth.DeviceWrite)...)
	cred := seedAccessTokenCredential(t, ctx)

	callAllThree(t, ctx, fmt.Sprint(cred.ID), func(name string, err error) {
		if err != nil {
			t.Errorf("%s refused a device:write caller: %v", name, err)
		}
	})

	r := &SchemaResolver{}
	found, err := r.DeviceCredentialsByToken(ctx, struct{ Tokens []string }{Tokens: []string{"dozer-01-cred"}})
	if err != nil {
		t.Fatalf("deviceCredentialsByToken: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("expected 1 credential, got %d", len(found))
	}
	if got := found[0].CredentialId(); got != "the-bearer-5f989616" {
		t.Errorf("credentialId = %q, want the seeded bearer", got)
	}
}

// The stored secret is never returned on read, whatever the caller holds.
//
// Narrow by construction: only MQTT_BASIC keeps a comparable secret here, so this
// protects the MQTT_BASIC password and nothing else. It is asserted under
// device:write — the most authority any caller can bring to these queries — so a
// pass means the field is unreachable rather than merely gated.
func TestCredentialValueIsNeverReturnedOnRead(t *testing.T) {
	ctx := withAuthorities(credentialTestCtx(t), append(viewerBaseline, auth.DeviceWrite)...)
	seedAccessTokenCredential(t, ctx) // seeded WITH a credentialValue

	r := &SchemaResolver{}
	found, err := r.DeviceCredentialsByToken(ctx, struct{ Tokens []string }{Tokens: []string{"dozer-01-cred"}})
	if err != nil {
		t.Fatalf("deviceCredentialsByToken: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("expected 1 credential, got %d", len(found))
	}
	if v := found[0].CredentialValue(); v != nil {
		t.Errorf("credentialValue leaked on read: %q", *v)
	}
}

// -----------------------------------------------------------------------------
// Structural guard: the gated queries are the ONLY way to reach a DeviceCredential
// -----------------------------------------------------------------------------

// introspectionQuery walks every field of every type and unwraps its return type
// far enough to see through [X!]! (NON_NULL → LIST → NON_NULL → named), with one
// level to spare.
const introspectionQuery = `{__schema{types{name fields{name type{kind name ofType{kind name ofType{kind name ofType{kind name ofType{kind name}}}}}}}}}`

type introspectedType struct {
	Kind   string            `json:"kind"`
	Name   string            `json:"name"`
	OfType *introspectedType `json:"ofType"`
}

// named unwraps LIST/NON_NULL wrappers down to the named type.
func (t *introspectedType) named() string {
	for t != nil {
		if t.Name != "" {
			return t.Name
		}
		t = t.OfType
	}
	return ""
}

// A DeviceCredential is reachable only through fields whose authorization has
// been considered.
//
// The authority gate on the three queries is the whole boundary only while those
// three are the only door. Adding a `credentials` field to the Device type — the
// obvious convenience — would reopen the hole silently, because Device is read
// under device:read and a field resolver inherits no gate of its own. Nothing
// else in the codebase notices that; this does.
//
// Adding a field here is not forbidden. It has to be a decision: extend the
// allowlist and gate the new field, in the same change.
func TestDeviceCredentialIsReachableOnlyThroughTheGatedQueries(t *testing.T) {
	// Every field in the schema whose return type unwraps to credential material,
	// as "ParentType.field". The Query entries are the three device:write-gated
	// queries; the Mutation entries are already device:write; the
	// DeviceCredentialSearchResults entry is the search wrapper's own payload,
	// reachable only through the gated query that returns it.
	allowed := map[string]string{
		"Query.deviceCredentialsById":           "gated on device:write",
		"Query.deviceCredentialsByToken":        "gated on device:write",
		"Query.deviceCredentials":               "gated on device:write",
		"Mutation.createDeviceCredential":       "gated on device:write",
		"Mutation.updateDeviceCredential":       "gated on device:write",
		"DeviceCredentialSearchResults.results": "payload of the gated search query",
		// replaceDevice (ADR-074) mints the credential the incoming physical unit is
		// programmed with, and returning it is the whole point — this is the one
		// moment that material is readable. It is admitted for the same reason the
		// three queries are: the mutation is gated on device:write, the authority
		// createDeviceCredential already requires, so the holder could mint the same
		// bearer through that door anyway.
		"DeviceReplaceResult.newCredential": "payload of replaceDevice, gated on device:write",
	}

	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})
	resp := schema.Exec(context.Background(), introspectionQuery, "", nil)
	if len(resp.Errors) != 0 {
		t.Fatalf("introspection failed: %v", resp.Errors)
	}

	var doc struct {
		Schema struct {
			Types []struct {
				Name   string `json:"name"`
				Fields []struct {
					Name string            `json:"name"`
					Type *introspectedType `json:"type"`
				} `json:"fields"`
			} `json:"types"`
		} `json:"__schema"`
	}
	if err := json.Unmarshal(resp.Data, &doc); err != nil {
		t.Fatalf("decode introspection: %v", err)
	}

	// The guard is worthless if it inspected nothing, so prove it saw the schema.
	if len(doc.Schema.Types) < 10 {
		t.Fatalf("introspection returned %d types; the guard is not looking at the schema", len(doc.Schema.Types))
	}

	found := make(map[string]bool)
	for _, typ := range doc.Schema.Types {
		for _, field := range typ.Fields {
			switch field.Type.named() {
			case "DeviceCredential", "DeviceCredentialSearchResults":
				found[typ.Name+"."+field.Name] = true
			}
		}
	}

	for path := range found {
		if _, ok := allowed[path]; !ok {
			t.Errorf("%s returns credential material and is not in the allowlist — "+
				"for ACCESS_TOKEN and X509_CERTIFICATE the credentialId is the device's bearer, "+
				"so this field needs its own device:write gate and an entry here", path)
		}
	}
	for path := range allowed {
		if !found[path] {
			t.Errorf("allowlist names %s but the schema has no such field returning credential material; "+
				"the allowlist has drifted and is no longer constraining anything", path)
		}
	}
}

// -----------------------------------------------------------------------------
// The mutation gates, which the read gate's justification rests on
// -----------------------------------------------------------------------------

// Registering, updating and removing a credential all require device:write.
//
// This is not incidental coverage of a neighbouring resolver. It is the premise
// the READ gate above is chosen on: gating credential reads at device:write is
// argued to grant that holder nothing new BECAUSE they can already mint a bearer
// for any device in the tenant through createDeviceCredential. If that mutation
// ever weakens to device:read, two things break at once and neither is loud —
// the impersonation escalation reopens, and the reasoning behind the read gate
// becomes false while the read gate still looks correct.
//
// Nothing else notices. Before this test the mutation gate could be reverted to
// device:read and the entire module suite stayed green, including every test in
// this file: the structural guard names these mutations "gated on device:write"
// in its allowlist and never checks that they are.
func TestCredentialMutationsRequireDeviceWrite(t *testing.T) {
	ctx := withAuthorities(credentialTestCtx(t), viewerBaseline...)
	seedAccessTokenCredential(t, ctx)

	r := &SchemaResolver{}
	request := &model.DeviceCredentialCreateRequest{
		Token:          "dozer-02-cred",
		DeviceToken:    "dozer-01",
		CredentialType: string(model.CredentialAccessToken),
		CredentialId:   "a-minted-bearer",
	}

	_, err := r.CreateDeviceCredential(ctx, struct {
		Request *model.DeviceCredentialCreateRequest
	}{Request: request})
	if err != auth.ErrForbidden {
		t.Errorf("createDeviceCredential answered a read-only caller with %v, want ErrForbidden — "+
			"a viewer who can mint a credential can impersonate any device in the tenant", err)
	}

	_, err = r.UpdateDeviceCredential(ctx, struct {
		Token   string
		Request model.DeviceCredentialUpdateRequest
	}{Token: "dozer-01-cred", Request: model.DeviceCredentialUpdateRequest{
		CredentialId: gqlcore.OptionalStringOf("a-minted-bearer"),
	}})
	if err != auth.ErrForbidden {
		t.Errorf("updateDeviceCredential answered a read-only caller with %v, want ErrForbidden", err)
	}

	if _, err = r.DeleteDeviceCredential(ctx, struct{ Token string }{Token: "dozer-01-cred"}); err != auth.ErrForbidden {
		t.Errorf("deleteDeviceCredential answered a read-only caller with %v, want ErrForbidden", err)
	}
}

// The counterweight: device:write really can mint a bearer for a device it does
// not otherwise own. This is the capability the read gate's justification asserts
// that holder already has, asserted rather than assumed — if credential creation
// were scoped more narrowly than "any device in the tenant", gating reads at
// device:write would be granting something new after all.
func TestDeviceWriteCanMintABearerForAnyDeviceInTheTenant(t *testing.T) {
	ctx := withAuthorities(credentialTestCtx(t), append(viewerBaseline, auth.DeviceWrite)...)
	seedAccessTokenCredential(t, ctx) // creates device dozer-01, owned by nobody in particular

	r := &SchemaResolver{}
	created, err := r.CreateDeviceCredential(ctx, struct {
		Request *model.DeviceCredentialCreateRequest
	}{Request: &model.DeviceCredentialCreateRequest{
		Token:          "dozer-01-cred-2",
		DeviceToken:    "dozer-01",
		CredentialType: string(model.CredentialAccessToken),
		CredentialId:   "a-minted-bearer",
	}})
	if err != nil {
		t.Fatalf("createDeviceCredential refused a device:write caller: %v", err)
	}
	if got := created.CredentialId(); got != "a-minted-bearer" {
		t.Errorf("credentialId = %q, want the minted bearer", got)
	}
}
