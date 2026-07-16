// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tenantCtx is the credential the INFERENCE path actually runs under: a tenant stamped
// into context from the verified service token. It is what makes the tenant-scope
// callback inject its predicate, so the additive-grant reads below are isolated by the
// platform rather than by a WHERE clause in our own code.
func tenantCtx(tenant string) context.Context {
	return core.WithTenant(context.Background(), tenant)
}

// adminCtx is the credential the OPERATOR path runs under: the admin plane is an
// identity-token plane and carries no tenant, so grant writes take the sanctioned
// system-context bypass. Api.sys applies it internally; this mirrors the bare context
// an admin resolver hands in.
func adminCtx() context.Context { return context.Background() }

func mustProvider(t *testing.T, api *Api, token string) *AIProvider {
	t.Helper()
	p, err := api.CreateAIProvider(adminCtx(), claudeReq(token, strp("sk-"+token)))
	require.NoError(t, err)
	return p
}

func menuTokens(m *Menu) []string {
	out := make([]string, 0, len(m.Providers))
	for _, p := range m.Providers {
		out = append(out, p.Token)
	}
	return out
}

// TestMenuIsTheUnionOfTierAndTenantGrants is the core ADR-065 resolution rule:
// menu(tenant) = its tier's grants ∪ its own additive grants.
func TestMenuIsTheUnionOfTierAndTenantGrants(t *testing.T) {
	api := newTestApi(t)
	mustProvider(t, api, "sonnet")
	mustProvider(t, api, "fable")

	require.NoError(t, api.GrantProviderToTier(adminCtx(), "bronze", "sonnet", true))
	require.NoError(t, api.GrantProviderToTenant(adminCtx(), "acme", "fable"))

	menu, err := api.MenuForTenant(tenantCtx("acme"), "bronze")
	require.NoError(t, err)
	assert.Equal(t, []string{"fable", "sonnet"}, menuTokens(menu),
		"a bronze tenant with an additive Fable grant sees both")
	require.NotNil(t, menu.Default)
	assert.Equal(t, "sonnet", menu.Default.Token, "the tier's marked default wins")
}

// TestAPerTenantGrantCannotRevokeWhatTheTierOffers pins ADR-065 decision 10's
// additive-only rule STRUCTURALLY rather than by convention. The exception table holds
// no deny row, so there is no sequence of per-tenant writes that removes a tier's
// offer — the closest an operator can get is revoking the exception, which leaves the
// tier's grant untouched.
func TestAPerTenantGrantCannotRevokeWhatTheTierOffers(t *testing.T) {
	api := newTestApi(t)
	mustProvider(t, api, "sonnet")
	mustProvider(t, api, "fable")
	require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "sonnet", true))
	require.NoError(t, api.GrantProviderToTenant(adminCtx(), "acme", "fable"))

	// Revoking the tenant's exception removes only the exception.
	removed, err := api.RevokeProviderFromTenant(adminCtx(), "acme", "fable")
	require.NoError(t, err)
	assert.True(t, removed)

	menu, err := api.MenuForTenant(tenantCtx("acme"), "gold")
	require.NoError(t, err)
	assert.Equal(t, []string{"sonnet"}, menuTokens(menu),
		"the tier's offer survives the exception being withdrawn")

	// And there is no per-tenant path to remove the tier's own offer: revoking the
	// TIER's grant for one tenant is not expressible — the call takes a tier, and its
	// effect is on every tenant at that tier, which is what makes the contract legible.
	removed, err = api.RevokeProviderFromTenant(adminCtx(), "acme", "sonnet")
	require.NoError(t, err)
	assert.False(t, removed, "sonnet was never an exception for acme; nothing to withdraw")

	menu, err = api.MenuForTenant(tenantCtx("acme"), "gold")
	require.NoError(t, err)
	assert.Equal(t, []string{"sonnet"}, menuTokens(menu),
		"a per-tenant revoke must not be able to strip a tier's entitlement")
}

// TestAdditiveGrantsAreIsolatedBetweenTenants is the leak test. The additive grants
// are per-tenant data in a table the operator writes from the system context, so the
// read path's isolation is what stands between one tenant's exception and another's.
func TestAdditiveGrantsAreIsolatedBetweenTenants(t *testing.T) {
	api := newTestApi(t)
	mustProvider(t, api, "sonnet")
	mustProvider(t, api, "fable")
	require.NoError(t, api.GrantProviderToTier(adminCtx(), "bronze", "sonnet", true))
	require.NoError(t, api.GrantProviderToTenant(adminCtx(), "acme", "fable"))

	other, err := api.MenuForTenant(tenantCtx("globex"), "bronze")
	require.NoError(t, err)
	assert.Equal(t, []string{"sonnet"}, menuTokens(other),
		"globex must not inherit acme's exception")
}

// TestMenuRequiresATenantInContext pins the fail-closed direction. The additive-grant
// read is tenant-scoped, so a caller that resolves a menu without a tenant must be
// refused — never silently served every tenant's exceptions.
func TestMenuRequiresATenantInContext(t *testing.T) {
	api := newTestApi(t)
	mustProvider(t, api, "sonnet")
	require.NoError(t, api.GrantProviderToTier(adminCtx(), "bronze", "sonnet", true))

	_, err := api.MenuForTenant(context.Background(), "bronze")
	assert.ErrorIs(t, err, core.ErrNoTenant,
		"resolving a menu with no tenant must fail closed, not read across tenants")
}

// TestATierWithNoGrantsHasAnEmptyMenu. Zero grants is a legitimate package ("this tier
// does not sell AI"), not a broken state: ADR-065 decision 11's "≥1 usable model"
// invariant was dropped because enforcing it would make that package unexpressible.
// The tenant is not dead — the form and canvas authoring doors are untouched; only the
// NL door reports unavailable.
func TestATierWithNoGrantsHasAnEmptyMenu(t *testing.T) {
	api := newTestApi(t)
	mustProvider(t, api, "sonnet")
	require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "sonnet", true))

	menu, err := api.MenuForTenant(tenantCtx("acme"), "bronze")
	require.NoError(t, err)
	assert.Empty(t, menu.Providers, "bronze was sold no AI; that is a package, not a fault")
	assert.Nil(t, menu.Default)
}

// TestAnUnknownTierResolvesToNothing. A tier token this service cannot validate (it
// holds a service token; the tier catalog is on user-management's identity-only admin
// plane) must resolve to an empty menu rather than to somebody else's grants.
func TestAnUnknownTierResolvesToNothing(t *testing.T) {
	api := newTestApi(t)
	mustProvider(t, api, "sonnet")
	require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "sonnet", true))

	menu, err := api.MenuForTenant(tenantCtx("acme"), "no-such-tier")
	require.NoError(t, err)
	assert.Empty(t, menu.Providers)

	// Same for an empty tier token — a tenant whose tier could not be read must not
	// fall through to every grant on the instance.
	menu, err = api.MenuForTenant(tenantCtx("acme"), "")
	require.NoError(t, err)
	assert.Empty(t, menu.Providers, "an unreadable tier must not resolve to a menu")
}

// TestDisabledProvidersNeverReachAMenu. Disabling is how an operator takes a model out
// of service without unpicking its packaging, so it must beat the grant.
func TestDisabledProvidersNeverReachAMenu(t *testing.T) {
	api := newTestApi(t)
	mustProvider(t, api, "sonnet")
	require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "sonnet", true))

	req := claudeReq("sonnet", nil)
	req.Enabled = false
	_, err := api.UpdateAIProvider(adminCtx(), "sonnet", req, nil)
	require.NoError(t, err)

	menu, err := api.MenuForTenant(tenantCtx("acme"), "gold")
	require.NoError(t, err)
	assert.Empty(t, menu.Providers, "a disabled model is granted but not usable")
	assert.Nil(t, menu.Default, "and it cannot remain the default it was marked as")
}

// TestTheDefaultFallsBackToASoleModel covers step 2 of the default rule: an operator
// who grants exactly one model and never marks a default plainly means that model.
func TestTheDefaultFallsBackToASoleModel(t *testing.T) {
	api := newTestApi(t)
	mustProvider(t, api, "sonnet")
	require.NoError(t, api.GrantProviderToTier(adminCtx(), "bronze", "sonnet", false))

	menu, err := api.MenuForTenant(tenantCtx("acme"), "bronze")
	require.NoError(t, err)
	require.NotNil(t, menu.Default)
	assert.Equal(t, "sonnet", menu.Default.Token)
}

// TestNoDefaultWhenTheChoiceIsAmbiguous covers step 3: with two models offered and none
// marked, guessing would silently route a tenant's prompts — and its spend — to a model
// nobody chose. The menu still resolves; only the default is absent.
func TestNoDefaultWhenTheChoiceIsAmbiguous(t *testing.T) {
	api := newTestApi(t)
	mustProvider(t, api, "sonnet")
	mustProvider(t, api, "fable")
	require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "sonnet", false))
	require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "fable", false))

	menu, err := api.MenuForTenant(tenantCtx("acme"), "gold")
	require.NoError(t, err)
	assert.Len(t, menu.Providers, 2)
	assert.Nil(t, menu.Default, "two models and no marked default is ambiguous, not a guess")
}

// TestAtMostOneDefaultPerTier pins the invariant that replaced "at most one active
// provider, instance-wide" — re-scoped from global to per-tier, which is the whole
// shape change ADR-065 makes.
func TestAtMostOneDefaultPerTier(t *testing.T) {
	api := newTestApi(t)
	mustProvider(t, api, "sonnet")
	mustProvider(t, api, "fable")

	require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "sonnet", true))
	require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "fable", true))

	grants, err := api.ListTierGrants(adminCtx())
	require.NoError(t, err)
	defaults := 0
	for _, g := range grants {
		if g.IsDefault {
			defaults++
			assert.Equal(t, "fable", g.Provider.Token, "the newest promotion is the default")
		}
	}
	assert.Equal(t, 1, defaults, "promoting a second default must demote the first")
}

// TestDefaultsAreIndependentAcrossTiers is the property the retired global pointer could
// not express, and the reason ADR-065 exists: bronze may default to a cheap fast model
// gold never touches.
func TestDefaultsAreIndependentAcrossTiers(t *testing.T) {
	api := newTestApi(t)
	mustProvider(t, api, "sonnet")
	mustProvider(t, api, "fable")
	require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "fable", true))
	require.NoError(t, api.GrantProviderToTier(adminCtx(), "bronze", "sonnet", true))

	gold, err := api.MenuForTenant(tenantCtx("acme"), "gold")
	require.NoError(t, err)
	require.NotNil(t, gold.Default)
	assert.Equal(t, "fable", gold.Default.Token)

	bronze, err := api.MenuForTenant(tenantCtx("globex"), "bronze")
	require.NoError(t, err)
	require.NotNil(t, bronze.Default)
	assert.Equal(t, "sonnet", bronze.Default.Token)
}

// TestSetTierDefaultRequiresTheGrant. A default must be something the tier actually
// offers; creating the grant as a side effect would let a typo silently sell a model.
func TestSetTierDefaultRequiresTheGrant(t *testing.T) {
	api := newTestApi(t)
	mustProvider(t, api, "sonnet")

	err := api.SetTierDefault(adminCtx(), "gold", "sonnet")
	assert.Error(t, err, "a provider the tier does not offer cannot be its default")

	menu, err := api.MenuForTenant(tenantCtx("acme"), "gold")
	require.NoError(t, err)
	assert.Empty(t, menu.Providers, "the failed default must not have granted anything")
}

// TestClearTierDefaultKeepsTheGrants.
func TestClearTierDefaultKeepsTheGrants(t *testing.T) {
	api := newTestApi(t)
	mustProvider(t, api, "sonnet")
	mustProvider(t, api, "fable")
	require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "sonnet", true))
	require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "fable", false))

	require.NoError(t, api.ClearTierDefault(adminCtx(), "gold"))

	menu, err := api.MenuForTenant(tenantCtx("acme"), "gold")
	require.NoError(t, err)
	assert.Len(t, menu.Providers, 2, "clearing the default withdraws nothing")
	assert.Nil(t, menu.Default)
}

// TestGrantIsIdempotent. Re-granting must not duplicate a row (the unique index would
// reject it) nor silently demote the tier's default.
func TestGrantIsIdempotent(t *testing.T) {
	api := newTestApi(t)
	mustProvider(t, api, "sonnet")
	mustProvider(t, api, "fable")
	require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "sonnet", true))
	require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "fable", false))

	// A plain re-grant of the non-default is not a statement about the default.
	require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "fable", false))

	grants, err := api.ListTierGrants(adminCtx())
	require.NoError(t, err)
	assert.Len(t, grants, 2, "re-granting must not duplicate")

	menu, err := api.MenuForTenant(tenantCtx("acme"), "gold")
	require.NoError(t, err)
	require.NotNil(t, menu.Default)
	assert.Equal(t, "sonnet", menu.Default.Token, "a re-grant must not disturb the default")

	// Tenant grants are idempotent too.
	require.NoError(t, api.GrantProviderToTenant(adminCtx(), "acme", "fable"))
	require.NoError(t, api.GrantProviderToTenant(adminCtx(), "acme", "fable"))
	tg, err := api.ListTenantGrants(adminCtx(), "acme")
	require.NoError(t, err)
	assert.Len(t, tg, 1)
}

// TestDeletingAGrantedProviderIsRefused is the refusal that makes provider deletion
// safe. Cascading instead would let one delete silently empty a tier's menu and strip
// AI from every tenant at that tier within the governance cache TTL.
func TestDeletingAGrantedProviderIsRefused(t *testing.T) {
	api := newTestApi(t)
	mustProvider(t, api, "sonnet")
	require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "sonnet", true))

	_, err := api.DeleteAIProvider(adminCtx(), "sonnet")
	assert.ErrorIs(t, err, ErrProviderInUse)
	assert.Contains(t, err.Error(), "gold", "the refusal must name what holds the provider")

	// Still there.
	found, err := api.AIProvidersByToken(adminCtx(), []string{"sonnet"})
	require.NoError(t, err)
	assert.Len(t, found, 1, "a refused delete must not have removed the row")

	// Ungrant, then the delete goes through — the refusal is a gate, not a wall.
	removed, err := api.RevokeProviderFromTier(adminCtx(), "gold", "sonnet")
	require.NoError(t, err)
	assert.True(t, removed)

	deleted, err := api.DeleteAIProvider(adminCtx(), "sonnet")
	require.NoError(t, err)
	assert.True(t, deleted)
}

// TestDeletingATenantGrantedProviderIsRefused. The per-tenant exception protects the
// provider exactly as a tier grant does — a model somebody is using is a model somebody
// is using.
func TestDeletingATenantGrantedProviderIsRefused(t *testing.T) {
	api := newTestApi(t)
	mustProvider(t, api, "fable")
	require.NoError(t, api.GrantProviderToTenant(adminCtx(), "acme", "fable"))

	_, err := api.DeleteAIProvider(adminCtx(), "fable")
	assert.ErrorIs(t, err, ErrProviderInUse)
	assert.Contains(t, err.Error(), "tenant", "the refusal must say a tenant holds it")

	removed, err := api.RevokeProviderFromTenant(adminCtx(), "acme", "fable")
	require.NoError(t, err)
	assert.True(t, removed)

	deleted, err := api.DeleteAIProvider(adminCtx(), "fable")
	require.NoError(t, err)
	assert.True(t, deleted)
}

// TestDeletingAnUngrantedProviderStillWorks guards the opposite mistake: the refusal
// must not become a blanket "providers cannot be deleted".
func TestDeletingAnUngrantedProviderStillWorks(t *testing.T) {
	api := newTestApi(t)
	mustProvider(t, api, "sonnet")

	deleted, err := api.DeleteAIProvider(adminCtx(), "sonnet")
	require.NoError(t, err)
	assert.True(t, deleted)
}

// TestGrantsRejectAMalformedTierOrTenantToken. The tokens are cross-service references
// this service cannot resolve, so grammar is the only validation available — and it is
// worth having, since these strings are stored and later compared against values that
// arrive over the wire.
func TestGrantsRejectAMalformedTierOrTenantToken(t *testing.T) {
	api := newTestApi(t)
	mustProvider(t, api, "sonnet")

	err := api.GrantProviderToTier(adminCtx(), "gold.tier", "sonnet", false)
	assert.Error(t, err, "a token carrying subject-structural characters must be refused")

	err = api.GrantProviderToTenant(adminCtx(), "acme>evil", "sonnet")
	assert.Error(t, err)
}

// TestListTierGrantsShowsAnUnknownTier. This service cannot validate a tier token, so
// the only defense against a stale or mistyped grant is that the operator can SEE it.
// Filtering unknown tiers out of the listing would hide the one thing that reveals the
// mistake — the grant would sit there, inert, offering a model to nobody, with no
// screen admitting it exists.
func TestListTierGrantsShowsAnUnknownTier(t *testing.T) {
	api := newTestApi(t)
	mustProvider(t, api, "sonnet")
	require.NoError(t, api.GrantProviderToTier(adminCtx(), "gld", "sonnet", true))

	grants, err := api.ListTierGrants(adminCtx())
	require.NoError(t, err)
	require.Len(t, grants, 1)
	assert.Equal(t, "gld", grants[0].TierToken, "a typo'd tier must remain visible to the operator")
}
