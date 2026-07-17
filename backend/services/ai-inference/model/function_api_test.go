// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resolveFor is the production question, asked the way production asks it: as the tenant
// in context, for a declared function.
func resolveFor(t *testing.T, api *Api, tenant, tier string) *AIProvider {
	t.Helper()
	chosen, err := api.ResolveModelForFunction(tenantCtx(tenant), tier, FunctionRuleDrafting)
	require.NoError(t, err)
	return chosen
}

// TestAnAssignmentOnTheMenuIsHonoured is the happy path: a tenant chose, it is entitled,
// it gets what it chose.
func TestAnAssignmentOnTheMenuIsHonoured(t *testing.T) {
	api := newTestApi(t)
	mustProvider(t, api, "sonnet")
	mustProvider(t, api, "fable")
	require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "sonnet"))
	require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "fable"))
	require.NoError(t, api.SetFunctionModel(adminCtx(), "acme", FunctionRuleDrafting, "fable"))

	chosen := resolveFor(t, api, "acme", "gold")
	require.NotNil(t, chosen)
	assert.Equal(t, "fable", chosen.Token, "the tenant's stored choice answers")
}

// TestAnAssignmentOffTheMenuResolvesToNoneNeverASubstitute is property 1 of
// ResolveModelForFunction, and it is the one that keeps an operator's act from silently
// re-pricing a tenant.
//
// A tenant chose a model and then lost access to it — by revoke, or by the operator
// disabling it during an incident. The honest answer is that the door is shut. Serving
// SOMETHING ELSE would route the tenant's prompts, and its spend, to a model nobody
// picked, at the exact moment an operator was busy with an outage. Both routes to
// "off the menu" are asserted, because they are different code paths (a missing grant vs
// a disabled provider) that must land identically.
func TestAnAssignmentOffTheMenuResolvesToNoneNeverASubstitute(t *testing.T) {
	cases := []struct {
		name string
		// takeItAway removes the assigned model from the tenant's menu, leaving a
		// perfectly usable OTHER model on it as bait for a substitution rule.
		takeItAway func(t *testing.T, api *Api)
	}{{
		name: "the assigned model is revoked from the tier",
		takeItAway: func(t *testing.T, api *Api) {
			removed, err := api.RevokeProviderFromTier(adminCtx(), "gold", "fable")
			require.NoError(t, err)
			require.True(t, removed)
		},
	}, {
		name: "the assigned model is disabled",
		takeItAway: func(t *testing.T, api *Api) {
			req := claudeReq("fable", nil)
			req.Enabled = false
			_, err := api.UpdateAIProvider(adminCtx(), "fable", req, nil)
			require.NoError(t, err)
		},
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := newTestApi(t)
			mustProvider(t, api, "sonnet")
			mustProvider(t, api, "fable")
			require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "sonnet"))
			require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "fable"))
			// sonnet is the baseline AND stays on the menu: if anything is going to be
			// wrongly substituted, this is what it would be.
			require.NoError(t, api.SetPlatformBaseline(adminCtx(), "sonnet"))
			require.NoError(t, api.SetFunctionModel(adminCtx(), "acme", FunctionRuleDrafting, "fable"))
			require.NotNil(t, resolveFor(t, api, "acme", "gold"), "precondition: the choice is honoured to begin with")

			tc.takeItAway(t, api)

			assert.Nil(t, resolveFor(t, api, "acme", "gold"),
				"a tenant that CHOSE has an opinion: losing access to its choice means NONE, "+
					"never a silent fallback to the baseline")
		})
	}
}

// TestTheAssignmentIsNotDestroyedByALapse is the other half of the rule above: the choice
// is not honoured while the entitlement is gone, and it is not FORGOTTEN either. This is
// why SetFunctionModel does not check the menu — an operator re-packaging a tier must not
// silently make every affected tenant re-enter its choice.
func TestTheAssignmentIsNotDestroyedByALapse(t *testing.T) {
	api := newTestApi(t)
	mustProvider(t, api, "fable")
	require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "fable"))
	require.NoError(t, api.SetFunctionModel(adminCtx(), "acme", FunctionRuleDrafting, "fable"))

	removed, err := api.RevokeProviderFromTier(adminCtx(), "gold", "fable")
	require.NoError(t, err)
	require.True(t, removed)
	require.Nil(t, resolveFor(t, api, "acme", "gold"), "the lapse shuts the door")

	// The entitlement returns; so does the choice, with no re-entry.
	require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "fable"))
	chosen := resolveFor(t, api, "acme", "gold")
	require.NotNil(t, chosen)
	assert.Equal(t, "fable", chosen.Token, "the stored choice survived the revoke and answers again")
}

// TestAnAssignmentCanBeMadeBeforeTheEntitlement is the same property from the other
// direction, and it is what makes the write path's silence about the menu deliberate
// rather than an oversight: an operator may set up a tenant's choice and its packaging in
// either order.
func TestAnAssignmentCanBeMadeBeforeTheEntitlement(t *testing.T) {
	api := newTestApi(t)
	mustProvider(t, api, "fable")

	// Not granted to anyone yet — the assignment is accepted anyway.
	require.NoError(t, api.SetFunctionModel(adminCtx(), "acme", FunctionRuleDrafting, "fable"))
	assert.Nil(t, resolveFor(t, api, "acme", "gold"), "stored, but not yet entitled: NONE")

	require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "fable"))
	chosen := resolveFor(t, api, "acme", "gold")
	require.NotNil(t, chosen)
	assert.Equal(t, "fable", chosen.Token)
}

// TestAnUnassignedTenantGetsTheBaseline: the baseline is what a tenant that never chose
// gets — the LCD, and the reason the platform still "just works" without every tenant
// being configured.
func TestAnUnassignedTenantGetsTheBaseline(t *testing.T) {
	api := newTestApi(t)
	mustProvider(t, api, "sonnet")
	require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "sonnet"))
	require.NoError(t, api.SetPlatformBaseline(adminCtx(), "sonnet"))

	chosen := resolveFor(t, api, "acme", "gold")
	require.NotNil(t, chosen)
	assert.Equal(t, "sonnet", chosen.Token)
}

// TestTheBaselineIsFilteredThroughTheTenantsMenu. The baseline is a fallback, not a
// bypass: it is checked against the menu like any other candidate. A tenant entitled to
// `fable` only does not get `sonnet` merely because the operator designated it.
func TestTheBaselineIsFilteredThroughTheTenantsMenu(t *testing.T) {
	api := newTestApi(t)
	mustProvider(t, api, "sonnet")
	mustProvider(t, api, "fable")
	require.NoError(t, api.SetPlatformBaseline(adminCtx(), "sonnet"))
	// gold sells fable, not the baseline.
	require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "fable"))

	assert.Nil(t, resolveFor(t, api, "acme", "gold"),
		"the baseline is not on this tenant's menu, so it cannot serve it — and `fable` is "+
			"not substituted in either, because nobody chose it")
}

// TestATierGrantingNothingResolvesToNoneEvenWithABaseline is THE PRODUCT INVARIANT: AI is
// a TIERED ENTITLEMENT.
//
// The baseline is the lowest-common-denominator for a tenant that never chose a model. It
// is NOT a free tier. If it could serve a tenant whose tier grants nothing, then every
// unpackaged tenant on the instance would have a working AI door the moment an operator
// designated a baseline — the entitlement model inverted, silently, by a single act aimed
// at something else entirely. Asserted hard, and from both directions (a tenant with no
// grants at all, and one whose tier sells nothing while other tiers do).
func TestATierGrantingNothingResolvesToNoneEvenWithABaseline(t *testing.T) {
	api := newTestApi(t)
	mustProvider(t, api, "sonnet")
	require.NoError(t, api.SetPlatformBaseline(adminCtx(), "sonnet"))

	// Nothing granted anywhere on the instance.
	assert.Nil(t, resolveFor(t, api, "acme", "bronze"),
		"a designated baseline must not hand AI to a tenant nobody sold it to")

	// And with `gold` packaged, `bronze` still gets nothing: the baseline follows the
	// entitlement, not the other way round.
	require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "sonnet"))
	assert.Nil(t, resolveFor(t, api, "acme", "bronze"),
		"bronze sells no AI; a baseline designated for the instance does not change that")
	require.NotNil(t, resolveFor(t, api, "globex", "gold"),
		"precondition: the baseline DOES serve a tenant whose tier was sold it")

	// Even an explicit assignment cannot manufacture an entitlement: the tenant may
	// choose, but choosing is not buying.
	require.NoError(t, api.SetFunctionModel(adminCtx(), "acme", FunctionRuleDrafting, "sonnet"))
	assert.Nil(t, resolveFor(t, api, "acme", "bronze"),
		"a tenant cannot assign its way onto a model its tier never sold it")
}

// TestAnExceptionOnlyTenantCanAssignAndResolve is ADR-065 decision 7's tenant — "a
// bronze-tier tenant could be given access to Fable when it's not offered in the bronze
// contract" — and it is the case the old mechanism served worst: its default could live
// nowhere, because the mark lived on tier grant rows and its tier had none.
//
// The (tenant, function) key makes the case unremarkable. There is no exception-only
// special path, no precedence rule, and no repair affordance — the tenant assigns a model
// exactly like any other tenant does.
func TestAnExceptionOnlyTenantCanAssignAndResolve(t *testing.T) {
	api := newTestApi(t)
	mustProvider(t, api, "sonnet")
	mustProvider(t, api, "fable")
	// bronze sells no AI at all; acme holds an audited exception for fable.
	require.NoError(t, api.GrantProviderToTenant(adminCtx(), "acme", "fable"))
	require.NoError(t, api.SetFunctionModel(adminCtx(), "acme", FunctionRuleDrafting, "fable"))

	chosen := resolveFor(t, api, "acme", "bronze")
	require.NotNil(t, chosen)
	assert.Equal(t, "fable", chosen.Token,
		"an exception-only tenant's choice resolves through the same path as everyone else's")

	// And its neighbour on the same tier, holding no exception, still gets nothing.
	assert.Nil(t, resolveFor(t, api, "globex", "bronze"),
		"the exception is acme's; bronze itself still sells nothing")
}

// TestGrantingMoreNeverChangesWhatResolves is THE test. Five bugs died here.
//
// THE PROPERTY: granting a tenant, or its tier, an EXTRA model never changes what any
// function resolves to. Granting is how an operator gives a tenant MORE; it must never be
// how a tenant loses, or silently changes, what it had.
//
// Every retired bug was a violation of exactly this, and each one hid from a test that
// asserted the property at a POINT instead of over a RANGE — my original regression test
// started from a tier-granted sole model and so only ever exercised the one arm already
// fixed. Hence a table over both axes, and over both the has-a-model and the has-nothing
// starting states, since a rule that infers from a set breaks differently in each.
//
// It is worth being precise about why this now holds STRUCTURALLY rather than by
// vigilance: resolution reads a stored row and a membership test, and consults the size
// of no set. There is no expression in ResolveModelForFunction for a grant to perturb.
// The table is the proof, not the mechanism.
func TestGrantingMoreNeverChangesWhatResolves(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, api *Api)
		// grantMore adds an entitlement and nothing else.
		grantMore func(t *testing.T, api *Api)
		// want is the token expected both BEFORE and AFTER grantMore; "" means NONE.
		want string
	}{{
		name: "tenant axis: an exception cannot move a model the tenant assigned",
		setup: func(t *testing.T, api *Api) {
			require.NoError(t, api.GrantProviderToTier(adminCtx(), "bronze", "sonnet"))
			require.NoError(t, api.SetFunctionModel(adminCtx(), "acme", FunctionRuleDrafting, "sonnet"))
		},
		grantMore: func(t *testing.T, api *Api) {
			require.NoError(t, api.GrantProviderToTenant(adminCtx(), "acme", "fable"))
		},
		want: "sonnet",
	}, {
		name: "tier axis: a second tier grant cannot move an assigned model",
		setup: func(t *testing.T, api *Api) {
			require.NoError(t, api.GrantProviderToTier(adminCtx(), "bronze", "sonnet"))
			require.NoError(t, api.SetFunctionModel(adminCtx(), "acme", FunctionRuleDrafting, "sonnet"))
		},
		grantMore: func(t *testing.T, api *Api) {
			require.NoError(t, api.GrantProviderToTier(adminCtx(), "bronze", "fable"))
		},
		want: "sonnet",
	}, {
		name: "tier axis: a second tier grant cannot turn the door off for the whole tier",
		setup: func(t *testing.T, api *Api) {
			// Nobody assigned anything: the baseline carries this tier.
			require.NoError(t, api.GrantProviderToTier(adminCtx(), "bronze", "sonnet"))
			require.NoError(t, api.SetPlatformBaseline(adminCtx(), "sonnet"))
		},
		grantMore: func(t *testing.T, api *Api) {
			require.NoError(t, api.GrantProviderToTier(adminCtx(), "bronze", "fable"))
		},
		want: "sonnet",
	}, {
		name: "tenant axis: an exception cannot vaporise a baseline the tenant was resolving",
		setup: func(t *testing.T, api *Api) {
			require.NoError(t, api.GrantProviderToTier(adminCtx(), "bronze", "sonnet"))
			require.NoError(t, api.SetPlatformBaseline(adminCtx(), "sonnet"))
		},
		grantMore: func(t *testing.T, api *Api) {
			require.NoError(t, api.GrantProviderToTenant(adminCtx(), "acme", "fable"))
		},
		want: "sonnet",
	}, {
		name: "tenant axis: a second exception cannot move an exception-only tenant's choice",
		setup: func(t *testing.T, api *Api) {
			// The tier sells no AI — the case the old mechanism could not express.
			require.NoError(t, api.GrantProviderToTenant(adminCtx(), "acme", "sonnet"))
			require.NoError(t, api.SetFunctionModel(adminCtx(), "acme", FunctionRuleDrafting, "sonnet"))
		},
		grantMore: func(t *testing.T, api *Api) {
			require.NoError(t, api.GrantProviderToTenant(adminCtx(), "acme", "fable"))
		},
		want: "sonnet",
	}, {
		// The half the old regression test's precondition excluded — and where the fourth
		// and fifth bugs lived. NONE is an ANSWER: nobody chose and no baseline is
		// designated. Growing the menu must not silently invent a choice.
		name: "NONE survives the tier's menu growing",
		setup: func(t *testing.T, api *Api) {
			require.NoError(t, api.GrantProviderToTier(adminCtx(), "bronze", "sonnet"))
		},
		grantMore: func(t *testing.T, api *Api) {
			require.NoError(t, api.GrantProviderToTier(adminCtx(), "bronze", "fable"))
		},
		want: "",
	}, {
		name: "NONE survives the tenant getting an exception",
		setup: func(t *testing.T, api *Api) {
			require.NoError(t, api.GrantProviderToTier(adminCtx(), "bronze", "sonnet"))
		},
		grantMore: func(t *testing.T, api *Api) {
			require.NoError(t, api.GrantProviderToTenant(adminCtx(), "acme", "fable"))
		},
		want: "",
	}, {
		name: "NONE survives an exception-only tenant getting a second exception",
		setup: func(t *testing.T, api *Api) {
			require.NoError(t, api.GrantProviderToTenant(adminCtx(), "acme", "sonnet"))
		},
		grantMore: func(t *testing.T, api *Api) {
			require.NoError(t, api.GrantProviderToTenant(adminCtx(), "acme", "fable"))
		},
		want: "",
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := newTestApi(t)
			mustProvider(t, api, "sonnet")
			mustProvider(t, api, "fable")
			tc.setup(t, api)

			before := resolveFor(t, api, "acme", "bronze")
			if tc.want == "" {
				require.Nil(t, before, "precondition: the answer starts as NONE")
			} else {
				require.NotNil(t, before, "precondition: the tenant starts with a usable model")
				require.Equal(t, tc.want, before.Token)
			}

			tc.grantMore(t, api)

			// The grant really did widen the menu — otherwise this asserts nothing.
			menu, err := api.MenuForTenant(tenantCtx("acme"), "bronze")
			require.NoError(t, err)
			require.Len(t, menu.Providers, 2, "precondition: the grant actually widened the menu")

			after := resolveFor(t, api, "acme", "bronze")
			if tc.want == "" {
				assert.Nil(t, after,
					"MONOTONICITY: granting an EXTRA model must not invent an answer where the operator's answer was NONE")
				return
			}
			require.NotNil(t, after,
				"MONOTONICITY: granting an EXTRA model must never leave the tenant with no model")
			assert.Equal(t, tc.want, after.Token,
				"MONOTONICITY: granting an EXTRA model must not re-point what resolves either")
		})
	}
}

// TestAtMostOnePlatformBaseline pins the invariant uix_ai_providers_baseline backs:
// designating B demotes A. Without the demote-before-promote ordering the partial unique
// index would reject the second designation outright.
func TestAtMostOnePlatformBaseline(t *testing.T) {
	api := newTestApi(t)
	mustProvider(t, api, "sonnet")
	mustProvider(t, api, "fable")

	require.NoError(t, api.SetPlatformBaseline(adminCtx(), "sonnet"))
	require.NoError(t, api.SetPlatformBaseline(adminCtx(), "fable"))

	baseline, err := api.PlatformBaseline(adminCtx())
	require.NoError(t, err)
	require.NotNil(t, baseline)
	assert.Equal(t, "fable", baseline.Token, "the newest designation is the baseline")

	// Read it back off the rows themselves, not only through the accessor: the accessor
	// takes the FIRST match, so it would report a single baseline even if two rows carried
	// the flag — the invariant is about the DATA, so the data is what gets counted.
	found, err := api.AIProviders(adminCtx(), AIProviderSearchCriteria{})
	require.NoError(t, err)
	baselines := 0
	for _, p := range found.Results {
		if p.IsPlatformBaseline {
			baselines++
		}
	}
	assert.Equal(t, 1, baselines, "designating a second baseline must demote the first")
}

// TestClearPlatformBaseline: with no baseline designated, a tenant that assigned nothing
// resolves to NONE — the whole instance falls back to explicit choice.
func TestClearPlatformBaseline(t *testing.T) {
	api := newTestApi(t)
	mustProvider(t, api, "sonnet")
	require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "sonnet"))
	require.NoError(t, api.SetPlatformBaseline(adminCtx(), "sonnet"))
	require.NotNil(t, resolveFor(t, api, "acme", "gold"))

	require.NoError(t, api.ClearPlatformBaseline(adminCtx()))

	baseline, err := api.PlatformBaseline(adminCtx())
	require.NoError(t, err)
	assert.Nil(t, baseline)
	assert.Nil(t, resolveFor(t, api, "acme", "gold"),
		"no baseline means a tenant that never chose has no model — the grant is untouched")

	menu, err := api.MenuForTenant(tenantCtx("acme"), "gold")
	require.NoError(t, err)
	assert.Len(t, menu.Providers, 1, "clearing the baseline withdraws no entitlement")
}

// TestResolveModelForFunctionRefusesTheSystemContext closes the hole the tenant guard
// leaves open. core.WithSystemContext PRESERVES the tenant while DISABLING the scoping
// callback, so a "is there a tenant?" check passes while the isolation this read depends
// on — twice over, for the menu and for the assignment — is switched off.
func TestResolveModelForFunctionRefusesTheSystemContext(t *testing.T) {
	api := newTestApi(t)
	mustProvider(t, api, "sonnet")
	require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "sonnet"))

	_, err := api.ResolveModelForFunction(core.WithSystemContext(tenantCtx("acme")), "gold", FunctionRuleDrafting)
	require.Error(t, err, "a system context disables tenant scoping: resolving under it must be refused")
	assert.Contains(t, err.Error(), "across tenants")
}

// TestResolveModelForFunctionRequiresATenant pins the fail-closed direction.
func TestResolveModelForFunctionRequiresATenant(t *testing.T) {
	api := newTestApi(t)
	mustProvider(t, api, "sonnet")

	_, err := api.ResolveModelForFunction(adminCtx(), "gold", FunctionRuleDrafting)
	assert.ErrorIs(t, err, core.ErrNoTenant)
}

// TestAssignmentsAreIsolatedBetweenTenants is the leak test. Assignments are per-tenant
// rows written by the operator from the SYSTEM context, so the read path's isolation is
// the only thing standing between one tenant's choice and another's — exactly the shape
// that makes the additive grants need the same test.
func TestAssignmentsAreIsolatedBetweenTenants(t *testing.T) {
	api := newTestApi(t)
	mustProvider(t, api, "sonnet")
	mustProvider(t, api, "fable")
	require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "sonnet"))
	require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "fable"))
	require.NoError(t, api.SetPlatformBaseline(adminCtx(), "sonnet"))

	// Only acme chooses fable. globex chose nothing, so it must get the BASELINE — not
	// acme's choice leaking across the tenant boundary.
	require.NoError(t, api.SetFunctionModel(adminCtx(), "acme", FunctionRuleDrafting, "fable"))

	acme := resolveFor(t, api, "acme", "gold")
	require.NotNil(t, acme)
	assert.Equal(t, "fable", acme.Token)

	globex := resolveFor(t, api, "globex", "gold")
	require.NotNil(t, globex)
	assert.Equal(t, "sonnet", globex.Token, "globex must not inherit acme's assignment")

	// And the admin listing is scoped too.
	listed, err := api.ListFunctionAssignments(adminCtx(), "globex")
	require.NoError(t, err)
	assert.Empty(t, listed, "globex has assigned nothing; acme's row is not its own")
}

// TestSetFunctionModelIsAnUpsert: one row per (tenant, function). Re-assigning replaces
// rather than duplicating — uix_ai_function_assignment would reject a second row anyway,
// so a non-upserting write would be an error rather than a silent duplicate.
func TestSetFunctionModelIsAnUpsert(t *testing.T) {
	api := newTestApi(t)
	mustProvider(t, api, "sonnet")
	mustProvider(t, api, "fable")
	require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "sonnet"))
	require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "fable"))

	require.NoError(t, api.SetFunctionModel(adminCtx(), "acme", FunctionRuleDrafting, "sonnet"))
	require.NoError(t, api.SetFunctionModel(adminCtx(), "acme", FunctionRuleDrafting, "fable"))
	// Idempotent: re-stating the current choice is not an error either.
	require.NoError(t, api.SetFunctionModel(adminCtx(), "acme", FunctionRuleDrafting, "fable"))

	listed, err := api.ListFunctionAssignments(adminCtx(), "acme")
	require.NoError(t, err)
	require.Len(t, listed, 1, "re-assigning must replace, never duplicate")
	assert.Equal(t, "fable", listed[0].Provider.Token)
	assert.Equal(t, FunctionRuleDrafting, listed[0].Function)

	chosen := resolveFor(t, api, "acme", "gold")
	require.NotNil(t, chosen)
	assert.Equal(t, "fable", chosen.Token)
}

// TestClearFunctionModel falls the tenant back to the baseline — the state it was in
// before it ever chose, not a new one. Idempotent.
func TestClearFunctionModel(t *testing.T) {
	api := newTestApi(t)
	mustProvider(t, api, "sonnet")
	mustProvider(t, api, "fable")
	require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "sonnet"))
	require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "fable"))
	require.NoError(t, api.SetPlatformBaseline(adminCtx(), "sonnet"))
	require.NoError(t, api.SetFunctionModel(adminCtx(), "acme", FunctionRuleDrafting, "fable"))

	removed, err := api.ClearFunctionModel(adminCtx(), "acme", FunctionRuleDrafting)
	require.NoError(t, err)
	assert.True(t, removed)

	chosen := resolveFor(t, api, "acme", "gold")
	require.NotNil(t, chosen)
	assert.Equal(t, "sonnet", chosen.Token, "a tenant that un-chose gets the baseline again")

	// Idempotent: clearing what is not there is not an error.
	removed, err = api.ClearFunctionModel(adminCtx(), "acme", FunctionRuleDrafting)
	require.NoError(t, err)
	assert.False(t, removed)
}

// TestAnUnknownFunctionIsRefused across every door that takes one. The vocabulary is
// platform-declared, so a row naming a function nothing will ever ask for is a row that
// looks like configuration and is dead.
func TestAnUnknownFunctionIsRefused(t *testing.T) {
	api := newTestApi(t)
	mustProvider(t, api, "sonnet")
	require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "sonnet"))

	err := api.SetFunctionModel(adminCtx(), "acme", "no-such-function", "sonnet")
	assert.ErrorIs(t, err, ErrUnknownFunction)

	_, err = api.ClearFunctionModel(adminCtx(), "acme", "no-such-function")
	assert.ErrorIs(t, err, ErrUnknownFunction)

	_, err = api.ResolveModelForFunction(tenantCtx("acme"), "gold", "no-such-function")
	assert.ErrorIs(t, err, ErrUnknownFunction)

	// The refused write stored nothing.
	listed, err := api.ListFunctionAssignments(adminCtx(), "acme")
	require.NoError(t, err)
	assert.Empty(t, listed)
}

// TestTheFunctionVocabulary pins the registry itself. AllFunctions is built by register
// at the declaration site, so this asserts the CAPABILITY (a token resolves, and the
// declared set is exactly what is reachable) rather than restating the list a second
// time — a restated list is one that silently stops matching.
func TestTheFunctionVocabulary(t *testing.T) {
	all := AllFunctions()
	require.Len(t, all, 1, "exactly one function at GA — deliberate, not a placeholder")
	assert.Equal(t, FunctionRuleDrafting, all[0].Token)
	assert.NotEmpty(t, all[0].Name, "a function needs a label for the console picker")
	assert.NotEmpty(t, all[0].Description)

	f, ok := FunctionByToken(FunctionRuleDrafting)
	require.True(t, ok)
	assert.Equal(t, all[0], f)

	_, ok = FunctionByToken("nope")
	assert.False(t, ok)
	assert.True(t, ValidFunction(FunctionRuleDrafting))
	assert.False(t, ValidFunction("nope"))

	// The returned slice is a copy: a caller mutating it must not reach the registry.
	all[0].Token = "clobbered"
	again := AllFunctions()
	assert.Equal(t, FunctionRuleDrafting, again[0].Token, "AllFunctions must hand out a copy")
}

// TestDeletingAnAssignedProviderIsRefused. An assignment protects a provider exactly as a
// grant does — a model somebody is using is a model somebody is using. Deleting it would
// silently drop that tenant to the baseline, or to nothing.
func TestDeletingAnAssignedProviderIsRefused(t *testing.T) {
	api := newTestApi(t)
	mustProvider(t, api, "fable")
	require.NoError(t, api.SetFunctionModel(adminCtx(), "acme", FunctionRuleDrafting, "fable"))

	_, err := api.DeleteAIProvider(adminCtx(), "fable")
	assert.ErrorIs(t, err, ErrProviderInUse)
	assert.Contains(t, err.Error(), "acme", "the refusal must name the tenant that assigned it")

	found, err := api.AIProvidersByToken(adminCtx(), []string{"fable"})
	require.NoError(t, err)
	assert.Len(t, found, 1, "a refused delete must not have removed the row")

	// Clear the assignment and the delete goes through — a gate, not a wall.
	removed, err := api.ClearFunctionModel(adminCtx(), "acme", FunctionRuleDrafting)
	require.NoError(t, err)
	require.True(t, removed)

	deleted, err := api.DeleteAIProvider(adminCtx(), "fable")
	require.NoError(t, err)
	assert.True(t, deleted)
}

// TestDeletingTheBaselineProviderIsRefused. The baseline has the WIDEST blast radius of
// the three references and the least visible one: every tenant that never chose is using
// it, and no FK protects a flag on the provider's own row — so the application check is
// the only thing standing there.
func TestDeletingTheBaselineProviderIsRefused(t *testing.T) {
	api := newTestApi(t)
	mustProvider(t, api, "sonnet")
	require.NoError(t, api.SetPlatformBaseline(adminCtx(), "sonnet"))

	_, err := api.DeleteAIProvider(adminCtx(), "sonnet")
	assert.ErrorIs(t, err, ErrProviderInUse)
	assert.Contains(t, err.Error(), "baseline", "the refusal must say the provider is the baseline")

	require.NoError(t, api.ClearPlatformBaseline(adminCtx()))
	deleted, err := api.DeleteAIProvider(adminCtx(), "sonnet")
	require.NoError(t, err)
	assert.True(t, deleted)
}

// TestTheDeleteRefusalNamesEveryReasonAtOnce. An operator clearing obstacles one at a
// time, re-running the delete to discover the next, is doing the tool's job for it.
func TestTheDeleteRefusalNamesEveryReasonAtOnce(t *testing.T) {
	api := newTestApi(t)
	mustProvider(t, api, "sonnet")
	require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "sonnet"))
	require.NoError(t, api.GrantProviderToTenant(adminCtx(), "acme", "sonnet"))
	require.NoError(t, api.SetFunctionModel(adminCtx(), "acme", FunctionRuleDrafting, "sonnet"))
	require.NoError(t, api.SetPlatformBaseline(adminCtx(), "sonnet"))

	_, err := api.DeleteAIProvider(adminCtx(), "sonnet")
	require.ErrorIs(t, err, ErrProviderInUse)
	msg := err.Error()
	assert.Contains(t, msg, "gold", "names the tier grant")
	assert.Contains(t, msg, "tenant(s)", "names the tenant grant")
	assert.Contains(t, msg, "acme", "names the assigning tenant")
	assert.Contains(t, msg, "baseline", "names the baseline designation")
}
