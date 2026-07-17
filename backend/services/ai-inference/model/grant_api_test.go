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
	// Assert on what the tenant can USE, not only on what is listed. Checking
	// membership alone is what let the non-monotonic-default bug through review here:
	// every membership assertion passed while the tenant's door was off.
	require.NotNil(t, menu.Default, "the tier's model is still usable")
	assert.Equal(t, "sonnet", menu.Default.Token)

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

// TestGrantingMoreNeverChangesTheDefault pins MONOTONICITY as a PROPERTY, over every
// axis a grant can arrive on, because pinning it at a point is what let the same bug
// ship three times.
//
// The property: if a tenant has a usable default, then granting it (or its tier) an
// ADDITIONAL model leaves that default exactly where it was. Granting is how an
// operator gives a tenant more; it must never be how a tenant loses what it had.
//
// The history this replaces is worth keeping, because each fix was right about its
// instance and blind to the shape — a default inferred by COUNTING a set operators can
// grow. Three instances, all of "…and if exactly one model is granted, use it":
//
//   - over the UNION: a per-tenant grant vaporised the TIER's resolved default;
//   - scoped to the tier, its twin survived on the tenant axis: a second exception
//     vaporised an exception-only tenant's default (case 3 below);
//   - and the tier's own arm had it: a second UNMARKED tier grant turned the door off
//     for EVERY tenant on that tier (case 2 below — the widest blast radius of the
//     three, and the one no reviewer found; it turned up only by re-running the shape).
//
// My original regression test passed throughout, because it started from a tier-granted
// sole model and so only ever exercised the one arm already fixed. It asserted the thing
// NEXT TO the risky thing. Hence a table: the axes are enumerated rather than trusted to
// whichever one I happened to have in mind.
func TestGrantingMoreNeverChangesTheDefault(t *testing.T) {
	cases := []struct {
		name string
		// setup establishes a tenant with a usable default.
		setup func(t *testing.T, api *Api)
		// grantMore adds an entitlement and nothing else.
		grantMore func(t *testing.T, api *Api)
		want      string
	}{{
		name: "an additive exception cannot move a default the tier marked",
		setup: func(t *testing.T, api *Api) {
			require.NoError(t, api.GrantProviderToTier(adminCtx(), "bronze", "sonnet", true))
		},
		grantMore: func(t *testing.T, api *Api) {
			require.NoError(t, api.GrantProviderToTenant(adminCtx(), "acme", "fable"))
		},
		want: "sonnet",
	}, {
		name: "a second UNMARKED tier grant cannot turn the door off for the whole tier",
		setup: func(t *testing.T, api *Api) {
			// Never marked: the auto-mark on the first grant is what carries this now.
			require.NoError(t, api.GrantProviderToTier(adminCtx(), "bronze", "sonnet", false))
		},
		grantMore: func(t *testing.T, api *Api) {
			require.NoError(t, api.GrantProviderToTier(adminCtx(), "bronze", "fable", false))
		},
		want: "sonnet",
	}, {
		name: "a second exception cannot turn the door off for an exception-only tenant",
		setup: func(t *testing.T, api *Api) {
			// The tier sells no AI at all — decision 7's case. The tenant's default can
			// live nowhere but its own grant.
			require.NoError(t, api.GrantProviderToTenant(adminCtx(), "acme", "sonnet"))
		},
		grantMore: func(t *testing.T, api *Api) {
			require.NoError(t, api.GrantProviderToTenant(adminCtx(), "acme", "fable"))
		},
		want: "sonnet",
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := newTestApi(t)
			mustProvider(t, api, "sonnet")
			mustProvider(t, api, "fable")
			tc.setup(t, api)

			before, err := api.MenuForTenant(tenantCtx("acme"), "bronze")
			require.NoError(t, err)
			require.NotNil(t, before.Default, "precondition: the tenant starts with a usable default")
			require.Equal(t, tc.want, before.Default.Token)

			tc.grantMore(t, api)

			after, err := api.MenuForTenant(tenantCtx("acme"), "bronze")
			require.NoError(t, err)
			assert.Len(t, after.Providers, 2, "precondition: the grant actually widened the menu")
			require.NotNil(t, after.Default,
				"MONOTONICITY: granting an EXTRA model must never leave the tenant with no default")
			assert.Equal(t, tc.want, after.Default.Token,
				"MONOTONICITY: granting an EXTRA model must not re-point the default either")
		})
	}
}

// TestNoDefaultIsEverInferredFromTheMenuSize guards the SHAPE rather than its instances.
// pickDefault takes the two marks and no sizes, so there is nothing to count; this
// asserts the consequence directly, so that reintroducing any "if exactly one model is
// granted…" rule fails here even on an axis the table above does not enumerate.
func TestNoDefaultIsEverInferredFromTheMenuSize(t *testing.T) {
	api := newTestApi(t)
	mustProvider(t, api, "sonnet")

	// One model, granted, enabled, usable — and marked by nobody, because the mark was
	// explicitly cleared. A counting rule says "it's the only one, use it". The stored
	// mark is the only source of a default, so the answer is NONE.
	require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "sonnet", false))
	require.NoError(t, api.ClearTierDefault(adminCtx(), "gold"))

	menu, err := api.MenuForTenant(tenantCtx("acme"), "gold")
	require.NoError(t, err)
	require.Equal(t, []string{"sonnet"}, menuTokens(menu), "the model is on the menu and usable")
	assert.Nil(t, menu.Default,
		"a sole usable model is NOT a default: an operator cleared the mark and that is a choice")
}

// TestAnOperatorsNoDefaultSurvivesTheMenuGrowing is the OTHER half of monotonicity, and
// the half I missed: TestGrantingMoreNeverChangesTheDefault is conditioned on the tenant
// STARTING with a usable default, so every state where the operator's answer is
// deliberately NONE fell outside its precondition — and that is exactly where the next
// bug was living.
//
// NONE is an answer, not an absence. An operator who clears a tier's default, or revokes
// the marked grant, has said "tenants here choose explicitly". Growing the menu must not
// silently overturn that: the auto-mark's probe is on the GRANT set precisely so that a
// state an operator can put the tier INTO cannot be mistaken for the state it STARTED in
// (see GrantProviderToTier).
func TestAnOperatorsNoDefaultSurvivesTheMenuGrowing(t *testing.T) {
	cases := []struct {
		name      string
		clearIt   func(t *testing.T, api *Api)
		grantMore func(t *testing.T, api *Api)
	}{{
		name: "tier axis: cleared, then granted an unmarked extra",
		clearIt: func(t *testing.T, api *Api) {
			require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "sonnet", true))
			require.NoError(t, api.ClearTierDefault(adminCtx(), "gold"))
		},
		grantMore: func(t *testing.T, api *Api) {
			require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "fable", false))
		},
	}, {
		name: "tier axis: marked default revoked, then granted an unmarked extra",
		clearIt: func(t *testing.T, api *Api) {
			require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "sonnet", true))
			require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "cheap", false))
			removed, err := api.RevokeProviderFromTier(adminCtx(), "gold", "sonnet")
			require.NoError(t, err)
			require.True(t, removed)
		},
		grantMore: func(t *testing.T, api *Api) {
			require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "fable", false))
		},
	}, {
		name: "tier axis: a re-grant of an ALREADY-granted pair says nothing about the default",
		clearIt: func(t *testing.T, api *Api) {
			require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "sonnet", true))
			require.NoError(t, api.ClearTierDefault(adminCtx(), "gold"))
		},
		grantMore: func(t *testing.T, api *Api) {
			// Documented as idempotent: "re-granting an existing pair updates nothing".
			require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "sonnet", false))
		},
	}, {
		name: "tenant axis: cleared, then granted a second exception",
		clearIt: func(t *testing.T, api *Api) {
			require.NoError(t, api.GrantProviderToTenant(adminCtx(), "acme", "sonnet"))
			require.NoError(t, api.ClearTenantDefault(adminCtx(), "acme"))
		},
		grantMore: func(t *testing.T, api *Api) {
			require.NoError(t, api.GrantProviderToTenant(adminCtx(), "acme", "fable"))
		},
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := newTestApi(t)
			mustProvider(t, api, "sonnet")
			mustProvider(t, api, "fable")
			mustProvider(t, api, "cheap")
			tc.clearIt(t, api)

			before, err := api.MenuForTenant(tenantCtx("acme"), "gold")
			require.NoError(t, err)
			require.Nil(t, before.Default, "precondition: the operator's answer is NONE")

			tc.grantMore(t, api)

			after, err := api.MenuForTenant(tenantCtx("acme"), "gold")
			require.NoError(t, err)
			assert.Nil(t, after.Default,
				"MONOTONICITY: an operator's explicit NO-DEFAULT must survive the menu growing")
		})
	}
}

// TestATierLevelActLandsTheSameForEveryTenantOnTheTier. Revoking a tier's marked default
// destroys the mark with the row, so it presents identically to "this tier grants
// nothing" — and reading the mark alone, the tenant's own mark then answered instead.
// The result was tenants at ONE tier silently resolving to DIFFERENT models based on
// which of them happened to hold an exception, on an act aimed at the tier.
func TestATierLevelActLandsTheSameForEveryTenantOnTheTier(t *testing.T) {
	api := newTestApi(t)
	mustProvider(t, api, "premium")
	mustProvider(t, api, "cheap")
	mustProvider(t, api, "fable")
	require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "premium", true))
	require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "cheap", false))
	// acme holds an exception; globex does not. Both are gold.
	require.NoError(t, api.GrantProviderToTenant(adminCtx(), "acme", "fable"))

	removed, err := api.RevokeProviderFromTier(adminCtx(), "gold", "premium")
	require.NoError(t, err)
	require.True(t, removed)

	plain, err := api.MenuForTenant(tenantCtx("globex"), "gold")
	require.NoError(t, err)
	exception, err := api.MenuForTenant(tenantCtx("acme"), "gold")
	require.NoError(t, err)

	assert.Nil(t, plain.Default, "the tier lost its default, so a plain gold tenant has none")
	assert.Nil(t, exception.Default,
		"and an exception-holding gold tenant must land the SAME way — its exception widens "+
			"what it may choose, it does not quietly answer for the tier")
}

// TestRevokingAMarkedDefaultDoesNotPromoteASurvivor. Revoking hard-deletes the grant the
// mark lives on, so the mark dies with it. Under the old counting rule that silently
// promoted whatever remained — re-pricing every tenant at the tier on an operator act
// that said nothing about the survivor. Disabling the marked default already resolved to
// NONE; revoking it must land the same way, since an operator has no reason to expect
// two adjacent acts on the same row to mean opposite things.
func TestRevokingAMarkedDefaultDoesNotPromoteASurvivor(t *testing.T) {
	api := newTestApi(t)
	mustProvider(t, api, "premium")
	mustProvider(t, api, "cheap")
	require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "premium", true))
	require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "cheap", false))

	removed, err := api.RevokeProviderFromTier(adminCtx(), "gold", "premium")
	require.NoError(t, err)
	require.True(t, removed)

	menu, err := api.MenuForTenant(tenantCtx("acme"), "gold")
	require.NoError(t, err)
	require.Equal(t, []string{"cheap"}, menuTokens(menu), "the revoked model leaves the menu")
	assert.Nil(t, menu.Default,
		"revoking the marked default must not silently re-point every gold tenant at the survivor")
}

// TestATenantMarkDoesNotOverrideItsTier fixes the precedence in a test rather than only
// in a comment. An exception widens what a tenant may CHOOSE; it does not re-point what
// the tenant GETS by default, or the tier would stop being a legible floor — and two
// tenants at the same tier would silently draft against different models.
func TestATenantMarkDoesNotOverrideItsTier(t *testing.T) {
	api := newTestApi(t)
	mustProvider(t, api, "sonnet")
	mustProvider(t, api, "fable")
	require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "sonnet", true))
	require.NoError(t, api.GrantProviderToTenant(adminCtx(), "acme", "fable"))
	require.NoError(t, api.SetTenantDefault(adminCtx(), "acme", "fable"))

	menu, err := api.MenuForTenant(tenantCtx("acme"), "gold")
	require.NoError(t, err)
	require.NotNil(t, menu.Default)
	assert.Equal(t, "sonnet", menu.Default.Token, "the tier's mark outranks the tenant's")

	// Clearing the tier's default does NOT hand the decision to the tenant's mark: a tier
	// that still GRANTS something is still answering, and its answer is now NONE. Only a
	// tier with no grants at all has no opinion to have — otherwise a tier-level act
	// would land differently for the tenants holding exceptions.
	require.NoError(t, api.ClearTierDefault(adminCtx(), "gold"))
	menu, err = api.MenuForTenant(tenantCtx("acme"), "gold")
	require.NoError(t, err)
	assert.Nil(t, menu.Default,
		"the tier still offers sonnet, so the tier answers — and it now answers NONE")

	// The tenant's mark is not discarded, though: it decides for a tenant whose tier
	// offers nothing at all, which is the only tenant it was ever meant to speak for.
	menu, err = api.MenuForTenant(tenantCtx("acme"), "bronze")
	require.NoError(t, err)
	require.NotNil(t, menu.Default)
	assert.Equal(t, "fable", menu.Default.Token,
		"bronze grants nothing, so acme's own mark is the only opinion available")
}

// TestAnExceptionOnlyTenantCanBeRepaired. The bug that started this had no operator
// remedy — the mark lived only on tier grant rows, so a two-exception tenant on a
// no-AI tier was stuck until a future slice shipped caller model choice. Whatever the
// resolution rule, an operator must be able to say which model a tenant defaults to.
func TestAnExceptionOnlyTenantCanBeRepaired(t *testing.T) {
	api := newTestApi(t)
	mustProvider(t, api, "sonnet")
	mustProvider(t, api, "fable")
	require.NoError(t, api.GrantProviderToTenant(adminCtx(), "acme", "sonnet"))
	require.NoError(t, api.GrantProviderToTenant(adminCtx(), "acme", "fable"))

	// The tier sells no AI, so nothing on the tier axis can speak for this tenant.
	require.NoError(t, api.SetTenantDefault(adminCtx(), "acme", "fable"))

	menu, err := api.MenuForTenant(tenantCtx("acme"), "bronze")
	require.NoError(t, err)
	require.NotNil(t, menu.Default)
	assert.Equal(t, "fable", menu.Default.Token)
}

// TestADisabledMarkedDefaultIsNotSubstituted. When the operator has marked a default
// and it goes out of service, the answer is "no default", not "some other model". The
// tier still offers another enabled model here — the point is that it must NOT be
// silently promoted: that would re-price every tenant at the tier the moment an
// operator disabled a model during an incident, which is exactly the silent routing
// the ambiguity rule exists to prevent.
func TestADisabledMarkedDefaultIsNotSubstituted(t *testing.T) {
	api := newTestApi(t)
	mustProvider(t, api, "cheap")
	mustProvider(t, api, "premium")
	require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "cheap", true))
	require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "premium", false))

	req := claudeReq("cheap", nil)
	req.Enabled = false
	_, err := api.UpdateAIProvider(adminCtx(), "cheap", req, nil)
	require.NoError(t, err)

	menu, err := api.MenuForTenant(tenantCtx("acme"), "gold")
	require.NoError(t, err)
	assert.Equal(t, []string{"premium"}, menuTokens(menu), "the disabled model leaves the menu")
	assert.Nil(t, menu.Default,
		"disabling the marked default must not silently promote a model the operator never chose")
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

// TestMenuRefusesTheSystemContext closes the hole the ctx guard leaves open.
// core.WithSystemContext DISABLES the tenant-scope callback but PRESERVES the tenant,
// so a "is there a tenant?" check passes while the isolation the read depends on is
// switched off — every tenant's exceptions would land in one tenant's menu. No caller
// does this today; the guard exists because this function's own contract tells callers
// to rely on the callback, which makes the one context that removes the callback a trap
// for whoever adds the next caller.
func TestMenuRefusesTheSystemContext(t *testing.T) {
	api := newTestApi(t)
	mustProvider(t, api, "sonnet")
	mustProvider(t, api, "fable")
	require.NoError(t, api.GrantProviderToTier(adminCtx(), "bronze", "sonnet", true))
	require.NoError(t, api.GrantProviderToTenant(adminCtx(), "globex", "fable"))

	sysCtx := core.WithSystemContext(tenantCtx("acme"))
	_, err := api.MenuForTenant(sysCtx, "bronze")
	require.Error(t, err, "a system context disables tenant scoping: resolving a menu under it must be refused")
	assert.Contains(t, err.Error(), "across tenants")
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

// TestTheFirstGrantToATierIsAutoMarked. An operator who offers a tier its only model
// plainly means that model, so granting still just works without a second call — but the
// mechanism matters and its name used to lie about it. This test was called
// "TestTheDefaultFallsBackToASoleModel", describing a READ-time sole-model fallback that
// is exactly the bug three fixes went into killing; it kept passing here via the
// auto-mark, telling any future reader the counting rule was alive and sanctioned.
//
// The convenience is real. It lives at WRITE time, as a mark on a row an operator can
// see and change — not as a rule that re-answers as the menu grows.
func TestTheFirstGrantToATierIsAutoMarked(t *testing.T) {
	api := newTestApi(t)
	mustProvider(t, api, "sonnet")
	require.NoError(t, api.GrantProviderToTier(adminCtx(), "bronze", "sonnet", false))

	menu, err := api.MenuForTenant(tenantCtx("acme"), "bronze")
	require.NoError(t, err)
	require.NotNil(t, menu.Default)
	assert.Equal(t, "sonnet", menu.Default.Token)

	// It is a stored mark, not an inference: the grant row itself carries it.
	grants, err := api.ListTierGrants(adminCtx())
	require.NoError(t, err)
	require.Len(t, grants, 1)
	assert.True(t, grants[0].IsDefault, "the default is a row an operator can read back")
}

// TestNoDefaultWhenTheChoiceIsAmbiguous: with several models offered and the mark
// explicitly cleared, guessing would silently route a tenant's prompts — and its spend —
// to a model nobody chose. The menu still resolves; only the default is absent.
//
// Note the ClearTierDefault: it is load-bearing, and its necessity IS the fix. Merely
// granting two models un-marked no longer reaches this state, because the first grant
// auto-marks — so "granted but no default" is now only ever something an operator asked
// for, never something that happened to a tier because its menu grew.
func TestNoDefaultWhenTheChoiceIsAmbiguous(t *testing.T) {
	api := newTestApi(t)
	mustProvider(t, api, "sonnet")
	mustProvider(t, api, "fable")
	require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "sonnet", false))
	require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "fable", false))
	require.NoError(t, api.ClearTierDefault(adminCtx(), "gold"))

	menu, err := api.MenuForTenant(tenantCtx("acme"), "gold")
	require.NoError(t, err)
	assert.Len(t, menu.Providers, 2)
	assert.Nil(t, menu.Default, "several models and no marked default is ambiguous, not a guess")
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

// TestTheDatabaseItselfRefusesADeleteThatBypassesTheCheck proves the FK backstop is
// real rather than merely declared.
//
// assertProviderNotGranted is the refusal an operator meets, and it is the one that
// produces a legible message — but it is application code, and application code can be
// walked around (raw SQL, a future code path, a bug). The claim in the migration's doc
// is that the database itself would still refuse. This test is what makes that claim
// true rather than aspirational: it deletes the provider row DIRECTLY, skipping the
// check entirely, and requires the constraint to stop it.
//
// It also proves the test harness enables SQLite's foreign_keys pragma. Without that
// pragma SQLite silently ignores every FOREIGN KEY, this test would pass by deleting
// happily and asserting nothing, and the backstop would rest on the Postgres golden
// alone.
func TestTheDatabaseItselfRefusesADeleteThatBypassesTheCheck(t *testing.T) {
	api := newTestApi(t)
	p := mustProvider(t, api, "sonnet")
	require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "sonnet", true))

	// Straight past assertProviderNotGranted, as raw SQL or a future caller would.
	err := api.sys(adminCtx()).Unscoped().Where("id = ?", p.ID).Delete(&AIProvider{}).Error
	require.Error(t, err, "the FOREIGN KEY must refuse a granted provider even with the application check bypassed")

	found, err := api.AIProvidersByToken(adminCtx(), []string{"sonnet"})
	require.NoError(t, err)
	assert.Len(t, found, 1, "the refused delete must leave the provider intact")

	// And the grant is not orphaned.
	grants, err := api.ListTierGrants(adminCtx())
	require.NoError(t, err)
	assert.Len(t, grants, 1)
}
