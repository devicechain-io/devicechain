// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-ai-inference/model"
	"github.com/devicechain-io/dc-microservice/auth"
	gql "github.com/graph-gophers/graphql-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// adminCtx builds the credential the admin plane actually carries: an IDENTITY
// token. The token type is load-bearing — ai:admin is system-tier, so a tenant
// access token can never satisfy it (ADR-065) and claims left untyped here would be
// refused on the tier, making every assertion below pass for the wrong reason.
func adminCtx(authorities ...string) context.Context {
	return auth.WithClaims(context.Background(), &auth.Claims{
		TokenType:   auth.TokenTypeIdentity,
		Authorities: authorities,
	})
}

// TestAdminSchemaParses validates that the admin schema parses against its resolver
// root — every admin field must have a matching resolver method. graph-gophers binds
// these by reflection at startup, so a renamed or missing resolver is a PANIC in
// main, not a compile error; this test is what turns that into a red build.
func TestAdminSchemaParses(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("admin schema failed to parse against resolver: %v", r)
		}
	}()
	gql.MustParseSchema(AdminSchemaContent, &AdminResolver{})
}

// fieldNames returns the sorted field names of a parsed schema's query or mutation
// root, read by introspection rather than by grepping the SDL — so it describes what
// the server will actually SERVE.
func fieldNames(t *testing.T, schema *gql.Schema, root string) []string {
	t.Helper()
	res := schema.Exec(context.Background(),
		`{ __schema { `+root+` { fields { name } } } }`, "", nil)
	require.Empty(t, res.Errors, "introspection failed")

	var out struct {
		Schema map[string]struct {
			Fields []struct {
				Name string `json:"name"`
			} `json:"fields"`
		} `json:"__schema"`
	}
	require.NoError(t, json.Unmarshal(res.Data, &out))

	names := make([]string, 0)
	for _, f := range out.Schema[root].Fields {
		names = append(names, f.Name)
	}
	sort.Strings(names)
	return names
}

// TestDataPlaneExposesNothingButTheTenantCall is the STRUCTURAL half of the ADR-065
// fix, and the one worth having. Moving the provider screen to /admin closes today's
// instance of the bug; this closes the class, by pinning what the tenant-facing plane
// is allowed to serve.
//
// The original defect was not a missing authorization check — every provider resolver
// DID gate on ai:admin. It was that instance-scoped operator fields were mounted on
// the plane that authenticates tenant access tokens, where the seeded `tenant-admin`
// role's "*" satisfied them. A future slice that adds a provider field back here
// would recreate that exactly, and no authz test would notice, because the check
// would look correct. This one fails.
func TestDataPlaneExposesNothingButTheTenantCall(t *testing.T) {
	schema := gql.MustParseSchema(SchemaContent, &SchemaResolver{})

	assert.Equal(t, []string{"aiInferenceInfo"}, fieldNames(t, schema, "queryType"),
		"the tenant data plane must expose only the service identity — provider administration belongs on /admin/graphql (ADR-065)")
	assert.Equal(t, []string{"inferRuleCandidate"}, fieldNames(t, schema, "mutationType"),
		"the tenant data plane must expose only the inference call — provider administration belongs on /admin/graphql (ADR-065)")
}

// TestAdminPlaneCarriesTheWholeProviderSurface is the mirror: everything the data
// plane gave up must be reachable somewhere, or the move is a silent feature
// deletion rather than a re-homing.
func TestAdminPlaneCarriesTheWholeProviderSurface(t *testing.T) {
	schema := gql.MustParseSchema(AdminSchemaContent, &AdminResolver{})

	assert.Equal(t,
		[]string{"aiProvider", "aiProviderKinds", "aiProviderTenantGrants",
			"aiProviderTierGrants", "aiProviders"},
		fieldNames(t, schema, "queryType"))
	assert.Equal(t,
		[]string{"clearPlatformBaselineProvider", "createAiProvider",
			"deleteAiProvider", "grantAiProviderToTenant", "grantAiProviderToTier",
			"revokeAiProviderFromTenant", "revokeAiProviderFromTier",
			"setPlatformBaselineProvider", "testAiProvider", "updateAiProvider"},
		fieldNames(t, schema, "mutationType"))
}

// TestTheDerivedDefaultIsGone pins the cutover at the surface, the way
// TestTheActivePointerIsGone pins its predecessor. The "default model" mechanism
// inferred which model served a call from properties of the grant sets, and so
// re-answered whenever an operator changed a set — the same non-monotonic shape, five
// times over (see model/function.go).
//
// Naming the retired fields explicitly is what stops one coming back via a resolver
// copied from history. A reintroduced setAiTierDefault would not merely be redundant: it
// would be a SECOND answer to "which model serves this call", sitting beside the stored
// assignment with no rule for which wins.
func TestTheDerivedDefaultIsGone(t *testing.T) {
	schema := gql.MustParseSchema(AdminSchemaContent, &AdminResolver{})
	served := append(fieldNames(t, schema, "queryType"), fieldNames(t, schema, "mutationType")...)

	for _, retired := range []string{
		"setAiTierDefault", "clearAiTierDefault", "setAiTenantDefault", "clearAiTenantDefault",
	} {
		assert.NotContains(t, served, retired,
			"the derived default is retired — the answer is a stored (tenant, function) assignment")
	}
	assert.NotContains(t, AdminSchemaContent, "isDefault",
		"a grant carries no default mark: it is an entitlement, not an answer")
	assert.NotContains(t, AdminSchemaContent, "makeDefault",
		"granting is not a statement about which model anything uses")
}

// TestTheActivePointerIsGone pins the ADR-065 retirement at the surface. The
// single-active-provider pointer modeled "one model, globally" and could express no
// packaging at all; it is replaced by the per-tier menu. Naming the retired fields
// explicitly is what stops one being reintroduced by a resolver copied from history —
// which would silently give every tenant the same model regardless of what its tier
// was sold.
func TestTheActivePointerIsGone(t *testing.T) {
	schema := gql.MustParseSchema(AdminSchemaContent, &AdminResolver{})
	served := append(fieldNames(t, schema, "queryType"), fieldNames(t, schema, "mutationType")...)

	for _, retired := range []string{"activeAiProvider", "setActiveAiProvider", "clearActiveAiProvider"} {
		assert.NotContains(t, served, retired,
			"the instance-wide active pointer is retired — entitlement is per-tier now (ADR-065)")
	}
	assert.NotContains(t, AdminSchemaContent, "active: Boolean!",
		"a provider carries no active flag — being offered is a property of the GRANT, not of the provider")
}

// TestAdminResolversFailClosed confirms every admin resolver rejects an
// unauthenticated request BEFORE touching the api in context — which is also why
// this test can pass a bare context without the api provider and not panic. If a
// resolver ever read the api first, it would panic here rather than return
// ErrUnauthenticated, so this doubles as a check that the gate really does run
// first.
func TestAdminResolversFailClosed(t *testing.T) {
	r := &AdminResolver{}
	ctx := context.Background()

	_, err := r.AiProvider(ctx, struct{ Token string }{Token: "x"})
	assert.ErrorIs(t, err, auth.ErrUnauthenticated)

	_, err = r.AiProviders(ctx, struct {
		Criteria model.AIProviderSearchCriteria
	}{})
	assert.ErrorIs(t, err, auth.ErrUnauthenticated)

	_, err = r.AiProviderKinds(ctx)
	assert.ErrorIs(t, err, auth.ErrUnauthenticated)

	_, err = r.AiProviderTierGrants(ctx)
	assert.ErrorIs(t, err, auth.ErrUnauthenticated)

	_, err = r.AiProviderTenantGrants(ctx, struct{ Tenant string }{Tenant: "acme"})
	assert.ErrorIs(t, err, auth.ErrUnauthenticated)

	_, err = r.CreateAiProvider(ctx, struct{ Request model.AIProviderCreateRequest }{})
	assert.ErrorIs(t, err, auth.ErrUnauthenticated)

	_, err = r.UpdateAiProvider(ctx, struct {
		Token             string
		Request           model.AIProviderCreateRequest
		ExpectedUpdatedAt *string
	}{Token: "x"})
	assert.ErrorIs(t, err, auth.ErrUnauthenticated)

	ok, err := r.DeleteAiProvider(ctx, struct{ Token string }{Token: "x"})
	assert.ErrorIs(t, err, auth.ErrUnauthenticated)
	assert.False(t, ok)

	// The grant surface is the packaging surface: every one of these decides which
	// tenants may spend the operator's API key, so each must fail closed on its own.
	ok, err = r.GrantAiProviderToTier(ctx, struct{ Tier, Provider string }{Tier: "gold", Provider: "p"})
	assert.ErrorIs(t, err, auth.ErrUnauthenticated)
	assert.False(t, ok)

	ok, err = r.RevokeAiProviderFromTier(ctx, struct{ Tier, Provider string }{Tier: "gold", Provider: "p"})
	assert.ErrorIs(t, err, auth.ErrUnauthenticated)
	assert.False(t, ok)

	// The baseline designation decides what every tenant that never chose a model gets,
	// so it fails closed on its own like the rest of the packaging surface.
	ok, err = r.SetPlatformBaselineProvider(ctx, struct{ Token string }{Token: "p"})
	assert.ErrorIs(t, err, auth.ErrUnauthenticated)
	assert.False(t, ok)

	ok, err = r.ClearPlatformBaselineProvider(ctx)
	assert.ErrorIs(t, err, auth.ErrUnauthenticated)
	assert.False(t, ok)

	ok, err = r.GrantAiProviderToTenant(ctx, struct{ Tenant, Provider string }{Tenant: "acme", Provider: "p"})
	assert.ErrorIs(t, err, auth.ErrUnauthenticated)
	assert.False(t, ok)

	ok, err = r.RevokeAiProviderFromTenant(ctx, struct{ Tenant, Provider string }{Tenant: "acme", Provider: "p"})
	assert.ErrorIs(t, err, auth.ErrUnauthenticated)
	assert.False(t, ok)

	_, err = r.TestAiProvider(ctx, struct {
		Token   string
		Request InferenceRequestInput
	}{Token: "x"})
	assert.ErrorIs(t, err, auth.ErrUnauthenticated)
}

// TestAdminResolversForbidWithoutAiAdmin confirms an authenticated operator who does
// not hold ai:admin is refused. ai:admin is not implied by being on the admin plane:
// an identity with, say, only tenant:read manages packaging, not model keys.
func TestAdminResolversForbidWithoutAiAdmin(t *testing.T) {
	r := &AdminResolver{}
	ctx := adminCtx(string(auth.TenantRead), string(auth.UserWrite))

	_, err := r.AiProviders(ctx, struct {
		Criteria model.AIProviderSearchCriteria
	}{})
	assert.ErrorIs(t, err, auth.ErrForbidden)

	_, err = r.AiProviderKinds(ctx)
	assert.ErrorIs(t, err, auth.ErrForbidden)

	_, err = r.CreateAiProvider(ctx, struct{ Request model.AIProviderCreateRequest }{})
	assert.ErrorIs(t, err, auth.ErrForbidden)

	// tenant:read is what an operator managing TIERS holds (tier CRUD is gated on it,
	// ADR-065 slice 1). It must not carry over into deciding which models a tier is
	// sold: the two surfaces sit either side of the API-key boundary.
	_, err = r.GrantAiProviderToTier(ctx, struct{ Tier, Provider string }{Tier: "gold", Provider: "p"})
	assert.ErrorIs(t, err, auth.ErrForbidden)

	_, err = r.SetPlatformBaselineProvider(ctx, struct{ Token string }{Token: "p"})
	assert.ErrorIs(t, err, auth.ErrForbidden)

	_, err = r.GrantAiProviderToTenant(ctx, struct{ Tenant, Provider string }{Tenant: "acme", Provider: "p"})
	assert.ErrorIs(t, err, auth.ErrForbidden)

	_, err = r.TestAiProvider(ctx, struct {
		Token   string
		Request InferenceRequestInput
	}{Token: "x"})
	assert.ErrorIs(t, err, auth.ErrForbidden)
}

// TestATenantAdminCannotAdministerProviders is the regression test for the ADR-065
// origin story, stated at the surface it actually happened on.
//
// The seeded `tenant-admin` role is TENANT-scoped and grants "*", so before slice 4 a
// member holding it — not just a superuser — passed every ai:admin check on this
// service and could read the provider list, re-point the model, or replace an API key.
// Two independent things stop that: the resolvers moved to an endpoint that refuses
// access tokens, and ai:admin became system-tier so "*" on an access token no longer
// satisfies it. This test pins the second, which is the one that still holds if
// someone later re-mounts a field on the wrong plane.
//
// The grant surface makes this sharper than it was: a tenant able to grant itself a
// provider would be writing its own entitlements — buying the gold package by API
// call — so the packaging mutations are checked here alongside the key-bearing ones.
func TestATenantAdminCannotAdministerProviders(t *testing.T) {
	r := &AdminResolver{}
	tenantAdmin := auth.WithClaims(context.Background(), &auth.Claims{
		TokenType:   auth.TokenTypeAccess,
		Tenant:      "acme",
		Authorities: []string{string(auth.AuthorityAll)},
	})

	_, err := r.AiProviders(tenantAdmin, struct {
		Criteria model.AIProviderSearchCriteria
	}{})
	assert.ErrorIs(t, err, auth.ErrForbidden, `a tenant-admin's "*" must not reach the provider list`)

	_, err = r.CreateAiProvider(tenantAdmin, struct{ Request model.AIProviderCreateRequest }{})
	assert.ErrorIs(t, err, auth.ErrForbidden, `a tenant-admin's "*" must not create a provider`)

	_, err = r.GrantAiProviderToTier(tenantAdmin, struct{ Tier, Provider string }{Tier: "gold", Provider: "x"})
	assert.ErrorIs(t, err, auth.ErrForbidden, `a tenant-admin's "*" must not decide what a tier is sold`)

	_, err = r.SetPlatformBaselineProvider(tenantAdmin, struct{ Token string }{Token: "x"})
	assert.ErrorIs(t, err, auth.ErrForbidden, `a tenant-admin's "*" must not designate the instance's baseline model`)

	_, err = r.GrantAiProviderToTenant(tenantAdmin, struct{ Tenant, Provider string }{Tenant: "acme", Provider: "x"})
	assert.ErrorIs(t, err, auth.ErrForbidden, `a tenant-admin's "*" must not grant its own tenant a model its tier never offered`)

	_, err = r.AiProviderTierGrants(tenantAdmin)
	assert.ErrorIs(t, err, auth.ErrForbidden, `a tenant-admin's "*" must not read the instance's packaging matrix`)

	_, err = r.TestAiProvider(tenantAdmin, struct {
		Token   string
		Request InferenceRequestInput
	}{Token: "x"})
	assert.ErrorIs(t, err, auth.ErrForbidden, `a tenant-admin's "*" must not spend the operator's API key`)

	// A superuser breaking glass INTO a tenant carries "*" on an access token too, and
	// is refused the same way. They are not locked out — they administer providers from
	// the admin plane, where their identity token carries the same authority.
	sudo := auth.WithClaims(context.Background(), &auth.Claims{
		TokenType:         auth.TokenTypeAccess,
		Tenant:            "acme",
		ActingAsSuperuser: true,
		Authorities:       []string{string(auth.AuthorityAll)},
	})
	_, err = r.AiProviders(sudo, struct {
		Criteria model.AIProviderSearchCriteria
	}{})
	assert.ErrorIs(t, err, auth.ErrForbidden, "a sudo access token must administer providers from /admin, not from inside a tenant")
}

// TestTheTenantCallIsNotGatedOnAiAdmin guards the opposite mistake. inferRuleCandidate
// is reached by event-processing's SERVICE token holding only ai:infer — a
// tenant-tier authority. Gating it on ai:admin (say, by copying a resolver during a
// refactor) would break the live NL-authoring door for every tenant, and the failure
// would look like a permissions config problem rather than a code one.
func TestTheTenantCallIsNotGatedOnAiAdmin(t *testing.T) {
	r := &SchemaResolver{}
	svc := auth.WithClaims(context.Background(), &auth.Claims{
		TokenType:   auth.TokenTypeService,
		Authorities: []string{string(auth.AIInfer)},
	})

	// It gets PAST authorization (the next gate is the missing tenant/inference
	// wiring), which is all this test claims: ai:infer alone opens the door.
	_, err := r.InferRuleCandidate(svc, struct{ Request InferenceRequestInput }{
		Request: InferenceRequestInput{Prompt: "when temp > 50 raise an alarm"},
	})
	require.Error(t, err)
	assert.NotErrorIs(t, err, auth.ErrForbidden, "ai:infer alone must open the tenant inference door")
	assert.NotErrorIs(t, err, auth.ErrUnauthenticated)

	// ...and an ai:admin holder does NOT get the tenant call for free: the authorities
	// are separate capabilities, not a hierarchy (ADR-047 — the inference service holds
	// no ambient authority over tenant data).
	adminOnly := adminCtx(string(auth.AIAdmin))
	_, err = r.InferRuleCandidate(adminOnly, struct{ Request InferenceRequestInput }{
		Request: InferenceRequestInput{Prompt: "x"},
	})
	assert.ErrorIs(t, err, auth.ErrForbidden)
}

// TestAdminSchemaNeverExposesTheApiKey pins the ADR-059 write-only contract across
// the plane move: `secret` goes in on the create/update input and never appears on
// any output type. The read side offers only hasSecret.
func TestAdminSchemaNeverExposesTheApiKey(t *testing.T) {
	schema := gql.MustParseSchema(AdminSchemaContent, &AdminResolver{})
	res := schema.Exec(context.Background(),
		`{ __type(name: "AiProvider") { fields { name } } }`, "", nil)
	require.Empty(t, res.Errors)

	body := string(res.Data)
	assert.NotContains(t, body, `"secret"`, "the API key must never be readable from the provider type (ADR-059)")
	assert.Contains(t, body, `"hasSecret"`, "the provider type must still report whether a key is configured")

	// And the input still accepts one — otherwise a key could never be sealed.
	assert.Contains(t, AdminSchemaContent, "input AiProviderCreateRequest")
	assert.True(t, strings.Contains(AdminSchemaContent, "secret: String"),
		"the create/update input must still accept a write-only secret")
}
