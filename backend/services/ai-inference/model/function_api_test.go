// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
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
			// sonnet is the tier's default AND stays on the menu: if anything is going to
			// be wrongly substituted, this is what it would be.
			require.NoError(t, api.SetTierDefault(adminCtx(), "gold", "sonnet"))
			require.NoError(t, api.SetFunctionModel(adminCtx(), "acme", FunctionRuleDrafting, "fable"))
			require.NotNil(t, resolveFor(t, api, "acme", "gold"), "precondition: the choice is honoured to begin with")

			tc.takeItAway(t, api)

			assert.Nil(t, resolveFor(t, api, "acme", "gold"),
				"a tenant that CHOSE has an opinion: losing access to its choice means NONE, "+
					"never a silent fallback to the tier's default")
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

// TestAnUnassignedTenantGetsTheTierDefault: the tier's marked default is what a tenant
// that never chose gets — the reason the platform still "just works" for a packaged
// tenant without every tenant being configured.
func TestAnUnassignedTenantGetsTheTierDefault(t *testing.T) {
	api := newTestApi(t)
	mustProvider(t, api, "sonnet")
	require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "sonnet"))
	require.NoError(t, api.SetTierDefault(adminCtx(), "gold", "sonnet"))

	chosen := resolveFor(t, api, "acme", "gold")
	require.NotNil(t, chosen)
	assert.Equal(t, "sonnet", chosen.Token)
}

// TestATierWithGrantsButNoDefaultResolvesToNone IS THE SHAPE GUARD. Read this one before
// touching ResolveModelForFunction.
//
// A tier grants models and its operator marked NO default. The answer is NONE — including,
// and especially, when the tier grants EXACTLY ONE model. "There's only one, obviously use
// it" is the rule that shipped as a bug five times, in five disguises; it is not a helpful
// convenience the server withholds out of pedantry. It is a rule that reads the SIZE of a
// set an operator can change, and so re-answers when they change it: the tenant's door
// works until somebody grants the tier a second model, at which point it silently stops.
//
// The single-model arm is the one that matters. A rule counting the menu passes the
// multi-model case (2 ≠ 1 ⇒ ambiguous ⇒ none) and fails only here, which is exactly how
// this survived review before: the assertion next to the risky thing was green.
func TestATierWithGrantsButNoDefaultResolvesToNone(t *testing.T) {
	for _, tc := range []struct {
		name   string
		grants []string
	}{
		{name: "the tier grants exactly one model and marks no default", grants: []string{"sonnet"}},
		{name: "the tier grants several models and marks no default", grants: []string{"sonnet", "fable"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := newTestApi(t)
			mustProvider(t, api, "sonnet")
			mustProvider(t, api, "fable")
			for _, g := range tc.grants {
				require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", g))
			}

			// The entitlement is real — this is not an empty-menu test wearing a disguise.
			menu, err := api.MenuForTenant(tenantCtx("acme"), "gold")
			require.NoError(t, err)
			require.Len(t, menu.Providers, len(tc.grants),
				"precondition: the tier really does offer these models")

			assert.Nil(t, resolveFor(t, api, "acme", "gold"),
				"NO SOLE-MODEL FALLBACK: a granted model is an entitlement, not a choice. "+
					"Nothing may infer a default from the menu's size")
		})
	}
}

// TestGrantingATierItsFirstModelDoesNotMarkADefault. There is no auto-mark, on any grant,
// ever — not even the one where it would seem harmless.
//
// This is bug #4's grave. The auto-mark probed the MARK set ("no default exists → mark
// this one"), and the flaw was not the probe's logic but its premise: an operator can
// EMPTY that set deliberately (ClearTierDefault, or revoking the marked grant). An empty
// mark set is therefore an ANSWER, not a gap, and a later grant that fills it silently
// overturns an explicit decision.
func TestGrantingATierItsFirstModelDoesNotMarkADefault(t *testing.T) {
	api := newTestApi(t)
	mustProvider(t, api, "sonnet")

	require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "sonnet"))

	def, err := api.TierDefault(adminCtx(), "gold")
	require.NoError(t, err)
	assert.Nil(t, def, "granting is not defaulting: the first grant to a tier must not mark itself")

	grants, err := api.ListTierGrants(adminCtx())
	require.NoError(t, err)
	require.Len(t, grants, 1)
	assert.False(t, grants[0].IsDefault, "the stored row must carry no mark either")

	// And the same holds after the operator explicitly clears a default: the mark set is
	// empty because they said so, and the NEXT grant must not quietly refill it.
	require.NoError(t, api.SetTierDefault(adminCtx(), "gold", "sonnet"))
	require.NoError(t, api.ClearTierDefault(adminCtx(), "gold"))
	mustProvider(t, api, "fable")
	require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "fable"))

	def, err = api.TierDefault(adminCtx(), "gold")
	require.NoError(t, err)
	assert.Nil(t, def,
		"an operator EMPTIED the mark set; a later grant that refills it overturns their decision")
	assert.Nil(t, resolveFor(t, api, "acme", "gold"))
}

// TestTheTierDefaultIsFilteredThroughTheTenantsMenu. The default is a fallback, not a
// bypass: it leaves the resolver through the same membership test as any other candidate.
// A DISABLED default resolves to NONE rather than handing the call to some other granted
// model — taking a model out of service is an operator act with an operator consequence,
// not a trigger for silently re-routing a tenant's prompts and its spend.
func TestTheTierDefaultIsFilteredThroughTheTenantsMenu(t *testing.T) {
	api := newTestApi(t)
	mustProvider(t, api, "sonnet")
	mustProvider(t, api, "fable")
	require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "sonnet"))
	require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "fable"))
	require.NoError(t, api.SetTierDefault(adminCtx(), "gold", "sonnet"))
	require.NotNil(t, resolveFor(t, api, "acme", "gold"), "precondition: the default answers")

	// The default goes out of service. `fable` stays granted and enabled — the bait.
	req := claudeReq("sonnet", nil)
	req.Enabled = false
	_, err := api.UpdateAIProvider(adminCtx(), "sonnet", req, nil)
	require.NoError(t, err)

	assert.Nil(t, resolveFor(t, api, "acme", "gold"),
		"a DISABLED tier default resolves to NONE — never to another granted model nobody chose")

	// The mark itself survives: disabling a provider is not un-marking it, so re-enabling
	// restores the tier without the operator re-entering their choice.
	def, err := api.TierDefault(adminCtx(), "gold")
	require.NoError(t, err)
	require.NotNil(t, def)
	assert.Equal(t, "sonnet", def.Token, "the mark is packaging; disabling is service state")
}

// TestRevokingTheDefaultGrantLeavesTheTierWithNoDefault, uniformly for every tenant on
// the tier — INCLUDING one holding a per-tenant exception.
//
// The exception-holder is bug #5's grave and is asserted explicitly. A dormant mark that
// springs alive when a tier is unpackaged is the same defect as one that dies when a tier
// is packaged: the tenant's answer moved because somebody else's row changed. Here the
// exception-holder has a WIDER menu than its neighbour, so any rule that quietly promoted
// "the model this tenant can still use" would light it up alone.
func TestRevokingTheDefaultGrantLeavesTheTierWithNoDefault(t *testing.T) {
	api := newTestApi(t)
	mustProvider(t, api, "sonnet")
	mustProvider(t, api, "fable")
	require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "sonnet"))
	require.NoError(t, api.SetTierDefault(adminCtx(), "gold", "sonnet"))
	// acme additionally holds an exception for fable; globex does not.
	require.NoError(t, api.GrantProviderToTenant(adminCtx(), "acme", "fable"))
	require.NotNil(t, resolveFor(t, api, "acme", "gold"), "precondition: acme resolves the default")
	require.NotNil(t, resolveFor(t, api, "globex", "gold"), "precondition: so does globex")

	removed, err := api.RevokeProviderFromTier(adminCtx(), "gold", "sonnet")
	require.NoError(t, err)
	require.True(t, removed)

	// The mark went with the row it lived on; nothing was promoted in its place.
	def, err := api.TierDefault(adminCtx(), "gold")
	require.NoError(t, err)
	assert.Nil(t, def, "revoking the marked grant leaves the tier with NO default")

	assert.Nil(t, resolveFor(t, api, "globex", "gold"), "the tier lost its default; globex has none")
	assert.Nil(t, resolveFor(t, api, "acme", "gold"),
		"the exception-holder lands identically: its own fable grant must not be silently "+
			"promoted into the default its TIER no longer has")

	// acme's exception is untouched — it is an entitlement, and it still needs a choice.
	menu, err := api.MenuForTenant(tenantCtx("acme"), "gold")
	require.NoError(t, err)
	assert.Equal(t, []string{"fable"}, menuTokens(menu), "the exception survives the tier's revoke")
	require.NoError(t, api.SetFunctionModel(adminCtx(), "acme", FunctionRuleDrafting, "fable"))
	chosen := resolveFor(t, api, "acme", "gold")
	require.NotNil(t, chosen)
	assert.Equal(t, "fable", chosen.Token, "acme resolves fable because it CHOSE it, not because it is alone")
}

// TestATierGrantingNothingResolvesToNone is THE PRODUCT INVARIANT: AI is a TIERED
// ENTITLEMENT. A tenant whose tier sells no AI gets none.
//
// This USED to be a runtime check — the instance-wide baseline had to be filtered through
// the menu or every unpackaged tenant would have been handed a working door by one
// operator act aimed at something else. IT IS NOW STRUCTURAL: the default is a column on a
// tier GRANT row, so a tier that grants nothing has nowhere to put a default and there is
// no candidate to filter. The test remains because the invariant is a product promise, not
// an implementation detail — a future refactor that re-homes the mark must fail here.
func TestATierGrantingNothingResolvesToNone(t *testing.T) {
	api := newTestApi(t)
	mustProvider(t, api, "sonnet")

	// Nothing granted anywhere on the instance.
	assert.Nil(t, resolveFor(t, api, "acme", "bronze"))

	// `gold` is packaged and defaulted; `bronze` still gets nothing. The default follows
	// the entitlement, not the other way round.
	require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "sonnet"))
	require.NoError(t, api.SetTierDefault(adminCtx(), "gold", "sonnet"))
	assert.Nil(t, resolveFor(t, api, "acme", "bronze"),
		"bronze sells no AI; another tier's default does not change that")
	require.NotNil(t, resolveFor(t, api, "globex", "gold"),
		"precondition: the default DOES serve a tenant whose tier was sold it")

	// Even an explicit assignment cannot manufacture an entitlement: the tenant may
	// choose, but choosing is not buying.
	require.NoError(t, api.SetFunctionModel(adminCtx(), "acme", FunctionRuleDrafting, "sonnet"))
	assert.Nil(t, resolveFor(t, api, "acme", "bronze"),
		"a tenant cannot assign its way onto a model its tier never sold it")
}

// TestDefaultsAreIndependentAcrossTiers. The mark is per-tier, so gold's default is not
// bronze's — the whole point of retiring the instance-wide baseline, which by
// construction gave every tier the same fallback.
func TestDefaultsAreIndependentAcrossTiers(t *testing.T) {
	api := newTestApi(t)
	mustProvider(t, api, "sonnet")
	mustProvider(t, api, "fable")
	for _, tier := range []string{"gold", "bronze"} {
		require.NoError(t, api.GrantProviderToTier(adminCtx(), tier, "sonnet"))
		require.NoError(t, api.GrantProviderToTier(adminCtx(), tier, "fable"))
	}
	require.NoError(t, api.SetTierDefault(adminCtx(), "gold", "fable"))
	require.NoError(t, api.SetTierDefault(adminCtx(), "bronze", "sonnet"))

	gold := resolveFor(t, api, "acme", "gold")
	require.NotNil(t, gold)
	assert.Equal(t, "fable", gold.Token)

	bronze := resolveFor(t, api, "globex", "bronze")
	require.NotNil(t, bronze)
	assert.Equal(t, "sonnet", bronze.Token, "each tier's default is its own")

	// Clearing one tier's default leaves the other's standing.
	require.NoError(t, api.ClearTierDefault(adminCtx(), "gold"))
	assert.Nil(t, resolveFor(t, api, "acme", "gold"))
	require.NotNil(t, resolveFor(t, api, "globex", "bronze"), "bronze's default is not gold's to clear")
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

	// Until it CHOOSES, it resolves to nothing: its tier marked no default (it has no
	// grants to mark), and its own exception is an entitlement rather than an answer.
	// Being the sole model on its menu is emphatically not what makes it resolve.
	assert.Nil(t, resolveFor(t, api, "acme", "bronze"),
		"an exception is an entitlement; nothing may promote it to a choice")

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
			// Nobody assigned anything: the TIER'S DEFAULT carries this tier. This is bug
			// #3's arm — a second UNMARKED grant killed the default for every tenant on the
			// tier, because the rule asked whether the tier granted exactly one model.
			require.NoError(t, api.GrantProviderToTier(adminCtx(), "bronze", "sonnet"))
			require.NoError(t, api.SetTierDefault(adminCtx(), "bronze", "sonnet"))
		},
		grantMore: func(t *testing.T, api *Api) {
			require.NoError(t, api.GrantProviderToTier(adminCtx(), "bronze", "fable"))
		},
		want: "sonnet",
	}, {
		name: "tenant axis: an exception cannot vaporise the tier default the tenant was resolving",
		setup: func(t *testing.T, api *Api) {
			// Bug #1's arm: the sole-model fallback resolved over the tier∪tenant UNION, so
			// giving this tenant MORE made its menu ambiguous and took its door away.
			require.NoError(t, api.GrantProviderToTier(adminCtx(), "bronze", "sonnet"))
			require.NoError(t, api.SetTierDefault(adminCtx(), "bronze", "sonnet"))
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
		// and fifth bugs lived. NONE IS AN ANSWER: nobody chose and the tier marked no
		// default. Growing the menu must not silently invent a choice.
		name: "NONE survives the tier's menu growing",
		setup: func(t *testing.T, api *Api) {
			require.NoError(t, api.GrantProviderToTier(adminCtx(), "bronze", "sonnet"))
		},
		grantMore: func(t *testing.T, api *Api) {
			require.NoError(t, api.GrantProviderToTier(adminCtx(), "bronze", "fable"))
		},
		want: "",
	}, {
		// Bug #4, stated as the property that killed it. The operator did not merely
		// neglect to mark a default — they marked one and then CLEARED it. The mark set is
		// empty BY DECISION, so an auto-mark that "notices there is no default" and
		// installs one on the next grant is not filling a gap, it is overturning a choice.
		name: "NONE survives a grant after the operator explicitly CLEARED the default",
		setup: func(t *testing.T, api *Api) {
			require.NoError(t, api.GrantProviderToTier(adminCtx(), "bronze", "sonnet"))
			require.NoError(t, api.SetTierDefault(adminCtx(), "bronze", "sonnet"))
			require.NoError(t, api.ClearTierDefault(adminCtx(), "bronze"))
		},
		grantMore: func(t *testing.T, api *Api) {
			require.NoError(t, api.GrantProviderToTier(adminCtx(), "bronze", "fable"))
		},
		want: "",
	}, {
		// Bug #4's history names TWO ways an operator empties the mark set: "ClearTierDefault,
		// OR revoking the marked grant". The arm above covers the first. This covers the
		// second, and it is the one a reintroduced auto-mark would light up in first — the
		// tier is left holding grants and no mark, which is exactly the state an
		// "install a default, there isn't one" rule mistakes for a fresh tier.
		name: "NONE survives a grant after the marked grant was REVOKED",
		setup: func(t *testing.T, api *Api) {
			// A third model, so revoking the marked one leaves bronze still granting
			// something — the state this arm is about is "grants, but no mark", which a
			// tier with zero grants would not exercise.
			mustProvider(t, api, "cheap")
			require.NoError(t, api.GrantProviderToTier(adminCtx(), "bronze", "sonnet"))
			require.NoError(t, api.GrantProviderToTier(adminCtx(), "bronze", "cheap"))
			require.NoError(t, api.SetTierDefault(adminCtx(), "bronze", "sonnet"))
			removed, err := api.RevokeProviderFromTier(adminCtx(), "bronze", "sonnet")
			require.NoError(t, err)
			require.True(t, removed)
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

// TestAtMostOneDefaultPerTier pins the invariant uix_ai_tier_grant_default backs: marking
// B demotes A. Without the demote-before-promote ordering the partial unique index would
// reject the second mark outright.
func TestAtMostOneDefaultPerTier(t *testing.T) {
	api := newTestApi(t)
	mustProvider(t, api, "sonnet")
	mustProvider(t, api, "fable")
	require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "sonnet"))
	require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "fable"))

	require.NoError(t, api.SetTierDefault(adminCtx(), "gold", "sonnet"))
	require.NoError(t, api.SetTierDefault(adminCtx(), "gold", "fable"))

	def, err := api.TierDefault(adminCtx(), "gold")
	require.NoError(t, err)
	require.NotNil(t, def)
	assert.Equal(t, "fable", def.Token, "the newest mark is the default")

	// Count the ROWS, not only the accessor's answer: TierDefault takes the FIRST match,
	// so it would report a single default even if two rows carried the mark. The invariant
	// is about the DATA, so the data is what gets counted.
	grants, err := api.ListTierGrants(adminCtx())
	require.NoError(t, err)
	marked := 0
	for _, g := range grants {
		if g.IsDefault {
			marked++
		}
	}
	assert.Equal(t, 1, marked, "marking a second default must demote the first")

	// Re-marking the current default is idempotent, not a demote-then-fail.
	require.NoError(t, api.SetTierDefault(adminCtx(), "gold", "fable"))
	def, err = api.TierDefault(adminCtx(), "gold")
	require.NoError(t, err)
	require.NotNil(t, def)
	assert.Equal(t, "fable", def.Token)
}

// TestSetTierDefaultOnAnUngrantedPairFailsAndCreatesNothing. A default must be something
// the tier actually OFFERS, so this is refused rather than quietly granting the model as a
// side effect — a mutation named "set the default" that also sells a tenant a model is a
// mutation whose blast radius nobody can read off the call site.
func TestSetTierDefaultOnAnUngrantedPairFailsAndCreatesNothing(t *testing.T) {
	api := newTestApi(t)
	mustProvider(t, api, "sonnet")
	mustProvider(t, api, "fable")
	require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "sonnet"))

	err := api.SetTierDefault(adminCtx(), "gold", "fable")
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound, "the tier does not offer fable; it cannot default to it")

	// Nothing was created, and — the sharper half — the tier's EXISTING default is intact:
	// the demote must not have committed while the promote matched nothing.
	grants, err := api.ListTierGrants(adminCtx())
	require.NoError(t, err)
	assert.Len(t, grants, 1, "a refused default must not grant the model as a side effect")

	require.NoError(t, api.SetTierDefault(adminCtx(), "gold", "sonnet"))
	require.Error(t, api.SetTierDefault(adminCtx(), "gold", "fable"))
	def, err := api.TierDefault(adminCtx(), "gold")
	require.NoError(t, err)
	require.NotNil(t, def, "a failed re-mark must leave the previous default standing, never half-apply")
	assert.Equal(t, "sonnet", def.Token)

	// An unknown provider is refused on its own terms.
	assert.ErrorIs(t, api.SetTierDefault(adminCtx(), "gold", "nope"), ErrUnknownProvider)
}

// TestClearTierDefault: with no default marked, a tenant that assigned nothing resolves to
// NONE — the tier falls back to explicit choice, and keeps every entitlement.
func TestClearTierDefault(t *testing.T) {
	api := newTestApi(t)
	mustProvider(t, api, "sonnet")
	require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "sonnet"))
	require.NoError(t, api.SetTierDefault(adminCtx(), "gold", "sonnet"))
	require.NotNil(t, resolveFor(t, api, "acme", "gold"))

	require.NoError(t, api.ClearTierDefault(adminCtx(), "gold"))

	def, err := api.TierDefault(adminCtx(), "gold")
	require.NoError(t, err)
	assert.Nil(t, def)
	assert.Nil(t, resolveFor(t, api, "acme", "gold"),
		"no default means a tenant that never chose has no model — the grant is untouched")

	menu, err := api.MenuForTenant(tenantCtx("acme"), "gold")
	require.NoError(t, err)
	assert.Len(t, menu.Providers, 1, "clearing the default withdraws no entitlement")

	// Idempotent, including on a tier that never had one.
	require.NoError(t, api.ClearTierDefault(adminCtx(), "gold"))
	require.NoError(t, api.ClearTierDefault(adminCtx(), "no-such-tier"))
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
	require.NoError(t, api.SetTierDefault(adminCtx(), "gold", "sonnet"))

	// Only acme chooses fable. globex chose nothing, so it must get its TIER'S DEFAULT —
	// not acme's choice leaking across the tenant boundary.
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

// TestClearFunctionModel falls the tenant back to its tier's default — the state it was in
// before it ever chose, not a new one. Idempotent.
func TestClearFunctionModel(t *testing.T) {
	api := newTestApi(t)
	mustProvider(t, api, "sonnet")
	mustProvider(t, api, "fable")
	require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "sonnet"))
	require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "fable"))
	require.NoError(t, api.SetTierDefault(adminCtx(), "gold", "sonnet"))
	require.NoError(t, api.SetFunctionModel(adminCtx(), "acme", FunctionRuleDrafting, "fable"))

	removed, err := api.ClearFunctionModel(adminCtx(), "acme", FunctionRuleDrafting)
	require.NoError(t, err)
	assert.True(t, removed)

	chosen := resolveFor(t, api, "acme", "gold")
	require.NotNil(t, chosen)
	assert.Equal(t, "sonnet", chosen.Token, "a tenant that un-chose gets its tier's default again")

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
// silently drop that tenant to its tier's default, or to nothing.
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

// TestDeletingATierDefaultProviderIsRefusedByItsGrant. The tier's default is protected
// without an arm of its own, and this pins the reasoning rather than the wording: the mark
// rides a GRANT row, so a provider that is any tier's default is necessarily granted to
// that tier and the grant arm already refuses.
//
// When the fallback was a flag on the PROVIDER's own row, nothing referenced it — no FK,
// no grant — so a hand-written check plus a hand-written predicate on the DELETE were the
// only things standing there. Both are gone, and this is the test that says their absence
// is safety rather than a hole.
func TestDeletingATierDefaultProviderIsRefusedByItsGrant(t *testing.T) {
	api := newTestApi(t)
	mustProvider(t, api, "sonnet")
	require.NoError(t, api.GrantProviderToTier(adminCtx(), "gold", "sonnet"))
	require.NoError(t, api.SetTierDefault(adminCtx(), "gold", "sonnet"))

	_, err := api.DeleteAIProvider(adminCtx(), "sonnet")
	assert.ErrorIs(t, err, ErrProviderInUse)
	assert.Contains(t, err.Error(), "gold", "the grant that carries the mark is what refuses")

	found, err := api.AIProvidersByToken(adminCtx(), []string{"sonnet"})
	require.NoError(t, err)
	assert.Len(t, found, 1, "a refused delete must not have removed the row")

	// Revoking the grant takes the mark with it, and then the delete goes through: a gate,
	// not a wall. Clearing the DEFAULT alone is not enough — the grant still holds it.
	require.NoError(t, api.ClearTierDefault(adminCtx(), "gold"))
	_, err = api.DeleteAIProvider(adminCtx(), "sonnet")
	assert.ErrorIs(t, err, ErrProviderInUse, "un-marking is not un-granting")

	removed, err := api.RevokeProviderFromTier(adminCtx(), "gold", "sonnet")
	require.NoError(t, err)
	require.True(t, removed)
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
	require.NoError(t, api.SetTierDefault(adminCtx(), "gold", "sonnet"))

	_, err := api.DeleteAIProvider(adminCtx(), "sonnet")
	require.ErrorIs(t, err, ErrProviderInUse)
	msg := err.Error()
	assert.Contains(t, msg, "gold", "names the tier grant")
	assert.Contains(t, msg, "tenant(s)", "names the tenant grant")
	assert.Contains(t, msg, "acme", "names the assigning tenant")
}
