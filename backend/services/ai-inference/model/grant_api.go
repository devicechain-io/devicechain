// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/devicechain-io/dc-microservice/core"
	"gorm.io/gorm"
)

// ErrProviderInUse refuses the delete of a provider that is still granted. It names
// the grants so the operator can act on it rather than guess (ADR-044's
// ErrEntityInUse shape, as user-management already uses for a tier that tenants
// reference).
var ErrProviderInUse = errors.New("provider is still granted and cannot be deleted")

// ErrUnknownProvider is returned when a grant names a provider token that does not
// exist. The FK would reject the insert anyway; catching it here gives the operator a
// message about a token rather than a constraint violation about an id.
var ErrUnknownProvider = errors.New("no provider with that token")

// Menu is a tenant's resolved set of AI models: everything it may draft with, plus
// which one a task uses absent an explicit choice.
type Menu struct {
	// Providers are the ENABLED providers the tenant may use, ordered by token so the
	// surface is stable for a caller rendering it.
	Providers []AIProvider
	// Default is the provider a task uses when the caller expresses no preference, or
	// nil when the menu cannot name one (see MenuForTenant).
	Default *AIProvider
}

// TierGrant pairs a tier grant with its provider, for the admin surface.
type TierGrant struct {
	TierToken string
	Provider  AIProvider
	IsDefault bool
}

// TenantGrant pairs a per-tenant additive grant with its provider.
type TenantGrant struct {
	TenantToken string
	Provider    AIProvider
	// IsDefault mirrors TierGrant's: the tenant's own marked default, which decides only
	// when its tier offers nothing (see MenuForTenant). It is surfaced for the same
	// reason the mark exists at all — a default that an operator cannot READ back is not
	// meaningfully "a row they can see and change", and setAiTenantDefault would be
	// operating blind without it.
	IsDefault bool
}

// AnyGrants reports whether this instance has any grant at all, of EITHER kind. It
// exists to keep the resolver's local short-circuit: an instance where nobody has been
// granted anything can answer "unavailable" without a user-management round trip,
// which is the property the retired active-pointer read used to provide ("default of
// none" costs nothing).
//
// It must count BOTH tables. Counting only tier grants would be the same short-circuit
// by appearance and a bug in fact: a tenant whose sole entitlement is an additive
// grant (ADR-065 decision 7 — "a bronze-tier tenant could be given access to Fable
// when it's not offered in the bronze contract") would be refused before its grant was
// ever looked at, on an instance where no tier had been granted anything. The
// exception would silently not work in precisely the situation it exists for.
//
// Both counts run in the system context: the question is "is anything configured on
// this instance", not "what may this caller use" — no entitlement decision is made
// here, only whether it is worth asking.
func (api *Api) AnyGrants(ctx context.Context) (bool, error) {
	// Existence, not a count: `Limit(1).Count(...)` reads like an early exit and is not
	// one — gorm strips ORDER BY from a Count but keeps the LIMIT, which then applies
	// to the single aggregate row, so the scan counts every row anyway. Selecting one
	// row and reading RowsAffected is the check this actually wants.
	exists := func(model any) (bool, error) {
		var probe []int64
		res := api.sys(ctx).Model(model).Select("1").Limit(1).Find(&probe)
		if res.Error != nil {
			return false, res.Error
		}
		return res.RowsAffected > 0, nil
	}
	if found, err := exists(&AIProviderTierGrant{}); err != nil || found {
		return found, err
	}
	return exists(&AIProviderTenantGrant{})
}

// MenuForTenant resolves the menu for a tenant at tierToken:
//
//	menu = enabled providers granted to the tier ∪ enabled providers granted to the
//	       tenant additively (ADR-065 decisions 7 + 10)
//
// The union is why the per-tenant grant can only ever ADD: there is no deny row to
// subtract with, so an exception cannot quietly undercut what the tier sold.
//
// THE DEFAULT IS A STORED MARK, NEVER A COUNT OF THE MENU. That is the whole of
// decision 10's additive-only guarantee, and it is worth stating as a rule about rules:
//
//	a default inferred by counting a set that operators can GROW is non-monotonic,
//	because growing the set changes the answer.
//
// This bit us three times before the rule was written down. Every instance was some
// flavour of "…and if exactly one model is granted, treat it as the default":
//
//   - resolving that fallback over the UNION let a per-tenant grant vaporise a default
//     the TIER had already resolved;
//   - scoping it to the tier's grants fixed that one and left its twin on the tenant
//     axis, where a second exception vaporised an exception-only tenant's default;
//   - and the tier's own arm had it too — a second UNMARKED grant to a tier turned the
//     door off for EVERY tenant on that tier.
//
// Each fix was correct about its instance and blind to the shape. So the fallback is
// gone entirely: a default exists iff somebody stored a mark. Offering a model still
// just works because the grant calls AUTO-MARK the first grant to a tier (and to a
// tenant) — the convenience moved from read time, where it silently re-answered as the
// data changed, to write time, where it is a row an operator can see and change.
//
// Precedence, most authoritative first:
//
//  1. IF THE TIER OFFERS ANYTHING AT ALL, THE TIER ANSWERS — with its marked model if
//     that model is usable, and otherwise with NONE. Never a substitute, and never a
//     fall-through to the tenant's mark. The tier is answering here even when its answer
//     is "nothing": a tier whose default was cleared, or revoked, or disabled is a broken
//     tier, and every tenant on it must fail identically rather than have the ones
//     holding exceptions quietly served something else. Quietly routing a tenant's
//     prompts, and its spend, to a different model because the chosen one went out of
//     service is exactly the silent re-pricing this rule exists to prevent — and a
//     tier-level act must land as a tier-level consequence.
//
//     Note this is why "does the tier speak" cannot be read off the mark alone: revoking
//     a marked grant destroys the mark with the row, so "grants nothing" and "grants
//     things, marks none" both present as no-mark. Conflating them let a tier-level
//     revoke fall through to a tenant's mark. Both states are the tier's to answer for.
//
//  2. THE TENANT'S MARK, only when the tier offers nothing at all and so has no opinion
//     to have. This is decision 7's exception-only tenant: a tier that sells no AI has no
//     grant row to carry a mark on, so the tenant's own grant is the only place its
//     default can live.
//
//  3. Otherwise nil. A caller that gets a nil Default with a non-empty menu is not
//     broken — it means nobody has chosen yet and the tenant must choose explicitly.
//
// Note what is NOT here: any branch that reads len(providers). That absence is the
// invariant, and TestGrantingMoreNeverChangesTheDefault pins it as a property over both
// axes rather than at the two points a counting rule happened to break.
//
// TENANT ISOLATION IS NOT ENFORCED HERE. The additive-grant read below deliberately
// carries no `WHERE tenant_id = ?`: AIProviderTenantGrant is rdb.TenantScoped, so the
// scoping callback injects the predicate from the context tenant and fails closed
// (core.ErrNoTenant) if there is none. That is the un-skippable path — a hand-written
// predicate here would be a second, forgettable copy of an isolation rule the
// platform already guarantees. The tierToken argument is caller-supplied because it
// comes from user-management over the wire, not from this service's tables.
func (api *Api) MenuForTenant(ctx context.Context, tierToken string) (*Menu, error) {
	// The context tenant is what actually scopes the additive grants; require it
	// explicitly so a caller that forgot to stamp one gets a clear error rather than
	// the callback's generic fail-closed further down.
	if _, ok := core.TenantFromContext(ctx); !ok {
		return nil, core.ErrNoTenant
	}
	// A system context DISABLES the scoping callback while leaving the tenant in place
	// (core.WithSystemContext), so the guard above would pass and the additive-grant
	// read below would silently return EVERY tenant's exceptions as this tenant's menu.
	// No caller does that today — the only one is the inference resolver, on the
	// request context. But this function's own contract invites callers to trust the
	// callback for isolation, so the one context that removes the callback must be
	// refused here rather than left as a trap for whoever adds the next caller.
	if core.IsSystemContext(ctx) {
		return nil, fmt.Errorf("a tenant's menu cannot be resolved in the system context: it would read across tenants")
	}

	var tierGrants []AIProviderTierGrant
	if strings.TrimSpace(tierToken) != "" {
		if err := api.sys(ctx).Where("tier_token = ?", tierToken).Find(&tierGrants).Error; err != nil {
			return nil, err
		}
	}

	// Tenant-scoped: the callback adds the tenant predicate. See the note above.
	var tenantGrants []AIProviderTenantGrant
	if err := api.RDB.DB(ctx).Find(&tenantGrants).Error; err != nil {
		return nil, err
	}

	ids := make([]uint, 0, len(tierGrants)+len(tenantGrants))
	seen := map[uint]bool{}
	var tierDefaultID, tenantDefaultID uint
	for _, g := range tierGrants {
		if !seen[g.ProviderID] {
			seen[g.ProviderID] = true
			ids = append(ids, g.ProviderID)
		}
		if g.IsDefault {
			tierDefaultID = g.ProviderID
		}
	}
	for _, g := range tenantGrants {
		if !seen[g.ProviderID] {
			seen[g.ProviderID] = true
			ids = append(ids, g.ProviderID)
		}
		if g.IsDefault {
			tenantDefaultID = g.ProviderID
		}
	}
	if len(ids) == 0 {
		return &Menu{}, nil
	}

	// Only ENABLED providers reach a menu: disabling is how an operator takes a model
	// out of service without unpicking its packaging, so a disabled provider must not
	// serve even where it is granted.
	var providers []AIProvider
	if err := api.sys(ctx).Where("id IN ? AND enabled = ?", ids, true).Find(&providers).Error; err != nil {
		return nil, err
	}
	sort.Slice(providers, func(i, j int) bool { return providers[i].Token < providers[j].Token })

	menu := &Menu{
		Providers: providers,
		// len(tierGrants) > 0 asks "does the tier offer anything at all", NOT "how many"
		// — see pickDefault's contract. It is computed from the grant rows rather than
		// from `providers`, so a tier whose only model is DISABLED still speaks (and
		// correctly answers NONE) instead of silently handing the decision to whichever
		// tenants hold exceptions.
		Default: pickDefault(providers, len(tierGrants) > 0, tierDefaultID, tenantDefaultID),
	}
	return menu, nil
}

// pickDefault applies the precedence documented on MenuForTenant. It is a pure function
// over the already-resolved menu so the rule can be read — and tested — in one place,
// rather than inferred from a chain of conditionals inside the query path.
//
// It takes the two MARKS and no SIZES, and that signature is the point: with nothing to
// count, the non-monotonic fallback cannot be reintroduced here without first adding a
// parameter to carry it. Both ids are provider ids, or 0 for "no mark".
//
// tierSpeaks is the one fact about the grant sets it needs, and it is deliberately a
// BOOLEAN rather than a length: it selects WHICH AXIS answers, and is never itself an
// answer. "The tier grants nothing" and "the tier grants things but marks none" are
// different states that a mark id alone cannot distinguish — revoking a marked grant
// destroys the mark with the row — and collapsing them is what let a tier-level revoke
// fall through to a tenant's own mark, so exception-holding tenants silently kept a
// default while everyone else on the tier correctly lost theirs.
//
// The marks are resolved against `providers`, which holds only the ENABLED ones — so a
// mark on a disabled provider correctly finds nothing and yields NONE.
func pickDefault(providers []AIProvider, tierSpeaks bool, tierDefaultID, tenantDefaultID uint) *AIProvider {
	find := func(id uint) *AIProvider {
		for i := range providers {
			if providers[i].ID == id {
				return &providers[i]
			}
		}
		return nil
	}

	// 1. The tier offers models, so the tier answers — with its mark if that model is
	//    usable, and otherwise with NONE. No fall-through to the tenant's mark on either
	//    branch: a tier whose default is missing (cleared, or revoked) or out of service
	//    (disabled) is a broken tier, and every tenant on it must fail the same way
	//    rather than have the ones holding exceptions quietly served something else. A
	//    tier-level act has a tier-level consequence.
	if tierSpeaks {
		return find(tierDefaultID) // nil when tierDefaultID == 0, which is the point.
	}

	// 2. The tier offers nothing at all, so it has no opinion to have: the tenant's own
	//    mark decides. This is decision 7's exception-only tenant, whose default can live
	//    nowhere else.
	if tenantDefaultID != 0 {
		return find(tenantDefaultID)
	}

	// 3. Nobody has chosen. Not an error — the caller must choose explicitly.
	return nil
}

// assertProviderNotGranted refuses the delete of a still-granted provider, naming what
// holds it. Called by DeleteAIProvider before the row is removed.
func (api *Api) assertProviderNotGranted(ctx context.Context, providerID uint) error {
	var tierGrants []AIProviderTierGrant
	if err := api.sys(ctx).Where("provider_id = ?", providerID).Find(&tierGrants).Error; err != nil {
		return err
	}
	var tenantCount int64
	if err := api.sys(ctx).Model(&AIProviderTenantGrant{}).
		Where("provider_id = ?", providerID).Count(&tenantCount).Error; err != nil {
		return err
	}
	if len(tierGrants) == 0 && tenantCount == 0 {
		return nil
	}

	tiers := make([]string, 0, len(tierGrants))
	for _, g := range tierGrants {
		tiers = append(tiers, g.TierToken)
	}
	sort.Strings(tiers)

	switch {
	case len(tiers) > 0 && tenantCount > 0:
		return fmt.Errorf("%w: granted to tier(s) %s and to %d tenant(s); remove those grants first",
			ErrProviderInUse, strings.Join(tiers, ", "), tenantCount)
	case len(tiers) > 0:
		return fmt.Errorf("%w: granted to tier(s) %s; remove those grants first",
			ErrProviderInUse, strings.Join(tiers, ", "))
	default:
		return fmt.Errorf("%w: granted to %d tenant(s); remove those grants first",
			ErrProviderInUse, tenantCount)
	}
}

// providerIDByToken resolves a provider token to its immutable id for the grant
// surface, which addresses providers by token (an operator-facing name) but stores
// ids (so a token rename keeps a grant bound).
func (api *Api) providerIDByToken(ctx context.Context, token string) (uint, error) {
	matches, err := api.AIProvidersByToken(ctx, []string{token})
	if err != nil {
		return 0, err
	}
	if len(matches) == 0 {
		return 0, fmt.Errorf("%w: %q", ErrUnknownProvider, token)
	}
	return matches[0].ID, nil
}

// GrantProviderToTier offers a provider to every tenant at a tier. Idempotent:
// re-granting an existing pair updates nothing and is not an error.
//
// makeDefault promotes this grant to the tier's default in the same transaction,
// demoting any previous one, so the "at most one default per tier" invariant never
// transiently breaks (the uix_ai_tier_grant_default partial unique index is the
// storage backstop). Passing false on an existing default does NOT demote it — a
// plain re-grant is not a statement about the default; use ClearTierDefault.
//
// tierToken is not validated against user-management: this service holds a service
// token and the tier catalog is on user-management's identity-only admin plane, so
// there is no door this credential can reach to ask. A grant naming an unknown tier
// is inert (no tenant reports that tier), and the admin console shows it as unknown
// rather than hiding it.
func (api *Api) GrantProviderToTier(ctx context.Context, tierToken, providerToken string, makeDefault bool) error {
	if err := core.ValidateToken(tierToken); err != nil {
		return fmt.Errorf("invalid tier token: %w", err)
	}
	providerID, err := api.providerIDByToken(ctx, providerToken)
	if err != nil {
		return err
	}

	return api.sys(ctx).Transaction(func(tx *gorm.DB) error {
		var existing AIProviderTierGrant
		err := tx.Where("tier_token = ? AND provider_id = ?", tierToken, providerID).First(&existing).Error
		switch {
		case err == nil:
			// Already granted. A plain re-grant says NOTHING about the default, so only
			// an explicit makeDefault may move it from here.
			if !makeDefault || existing.IsDefault {
				return nil
			}
			return promoteTierDefault(tx, tierToken, existing.ID)
		case errors.Is(err, gorm.ErrRecordNotFound):
		default:
			return err
		}

		// AUTO-MARK THE TIER'S FIRST GRANT — and note the probe is on the GRANT set, not
		// the MARK set, which is the whole of this rule's correctness.
		//
		// Keying it on "no mark exists" reintroduces the bug this design exists to kill,
		// one level up: operators can EMPTY the mark set (ClearTierDefault, or revoking
		// the marked grant), so "unmarked" is a state they can be IN, not just a state
		// they start in — and auto-marking then lets the next unmarked grant silently
		// overturn an explicit "this tier has no default", re-pointing every tenant on
		// the tier at a model nobody chose. A default inferred from the emptiness of a
		// set operators can empty is the same non-monotonic shape as inferring it from
		// the SIZE of a set operators can grow. It was found here in review, in the fix
		// for the third instance of that shape.
		//
		// The GRANT set has the property the mark set lacks: nothing an operator does to
		// the default can empty it. It is empty only when the tier genuinely offers
		// nothing, so "first grant" means what it says, and a cleared default stays
		// cleared no matter how the menu grows.
		first, err := tierHasNoGrants(tx, tierToken)
		if err != nil {
			return err
		}
		existing = AIProviderTierGrant{TierToken: tierToken, ProviderID: providerID}
		if err := tx.Create(&existing).Error; err != nil {
			return err
		}
		if !makeDefault && !first {
			return nil
		}
		return promoteTierDefault(tx, tierToken, existing.ID)
	})
}

// tierHasNoGrants reports whether the tier has no grant rows at all, inside the caller's
// transaction — i.e. whether the next grant is its first. An existence probe, not a
// count: Limit(1).Count would keep the LIMIT and apply it to the aggregate row, the fake
// early-exit this repo has been bitten by before.
func tierHasNoGrants(tx *gorm.DB, tierToken string) (bool, error) {
	var probe []int64
	res := tx.Model(&AIProviderTierGrant{}).Select("1").
		Where("tier_token = ?", tierToken).Limit(1).Find(&probe)
	return res.RowsAffected == 0, res.Error
}

// promoteTierDefault demotes the tier's current default and promotes grantID, inside
// the caller's transaction. Order matters: the demote must land before the promote or
// the partial unique index rejects the second default.
func promoteTierDefault(tx *gorm.DB, tierToken string, grantID uint) error {
	if err := tx.Model(&AIProviderTierGrant{}).
		Where("tier_token = ? AND is_default = ?", tierToken, true).
		Update("is_default", false).Error; err != nil {
		return err
	}
	return tx.Model(&AIProviderTierGrant{}).
		Where("id = ?", grantID).Update("is_default", true).Error
}

// SetTierDefault marks an already-granted provider as the tier's default, demoting any
// previous one atomically. Returns gorm.ErrRecordNotFound when the pair is not granted
// — a default must be something the tier actually offers, so this deliberately does
// not create the grant as a side effect.
func (api *Api) SetTierDefault(ctx context.Context, tierToken, providerToken string) error {
	providerID, err := api.providerIDByToken(ctx, providerToken)
	if err != nil {
		return err
	}
	return api.sys(ctx).Transaction(func(tx *gorm.DB) error {
		var existing AIProviderTierGrant
		if err := tx.Where("tier_token = ? AND provider_id = ?", tierToken, providerID).
			First(&existing).Error; err != nil {
			return err
		}
		if existing.IsDefault {
			return nil
		}
		return promoteTierDefault(tx, tierToken, existing.ID)
	})
}

// ClearTierDefault leaves the tier's grants in place but marks none of them default.
// Idempotent. With two or more models on the menu this makes the tenant choose
// explicitly (see MenuForTenant).
func (api *Api) ClearTierDefault(ctx context.Context, tierToken string) error {
	return api.sys(ctx).Model(&AIProviderTierGrant{}).
		Where("tier_token = ? AND is_default = ?", tierToken, true).
		Update("is_default", false).Error
}

// RevokeProviderFromTier withdraws a tier's offer of a provider, reporting whether a
// grant was removed. Hard delete; idempotent.
func (api *Api) RevokeProviderFromTier(ctx context.Context, tierToken, providerToken string) (bool, error) {
	providerID, err := api.providerIDByToken(ctx, providerToken)
	if err != nil {
		return false, err
	}
	res := api.sys(ctx).Where("tier_token = ? AND provider_id = ?", tierToken, providerID).
		Delete(&AIProviderTierGrant{})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// ListTierGrants returns every tier grant with its provider, for the operator surface.
// Instance-global (an operator sees all tiers' packaging), ordered by tier then
// provider token. Grants naming a tier that no longer exists are INCLUDED: the console
// renders them as unknown so a stale or mistyped grant is visible rather than silently
// filtered out of the one screen that could reveal it.
func (api *Api) ListTierGrants(ctx context.Context) ([]TierGrant, error) {
	var grants []AIProviderTierGrant
	if err := api.sys(ctx).Find(&grants).Error; err != nil {
		return nil, err
	}
	providers, err := providersByID(api, ctx, grants, func(g AIProviderTierGrant) uint { return g.ProviderID })
	if err != nil {
		return nil, err
	}
	out := make([]TierGrant, 0, len(grants))
	for _, g := range grants {
		p, ok := providers[g.ProviderID]
		if !ok {
			// The FK makes this unreachable; skip rather than surface a zero provider.
			continue
		}
		out = append(out, TierGrant{TierToken: g.TierToken, Provider: p, IsDefault: g.IsDefault})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TierToken != out[j].TierToken {
			return out[i].TierToken < out[j].TierToken
		}
		return out[i].Provider.Token < out[j].Provider.Token
	})
	return out, nil
}

// GrantProviderToTenant adds a provider to ONE tenant's menu over and above its tier
// (ADR-065 decision 7's audited exception). Idempotent.
//
// Runs in the system context because the caller is an operator on the identity-token
// admin plane, which holds no tenant — so TenantId is set explicitly here rather than
// stamped by the scoping callback. That is the sanctioned bypass; the read path in
// MenuForTenant keeps the callback's isolation.
func (api *Api) GrantProviderToTenant(ctx context.Context, tenantToken, providerToken string) error {
	if err := core.ValidateToken(tenantToken); err != nil {
		return fmt.Errorf("invalid tenant token: %w", err)
	}
	providerID, err := api.providerIDByToken(ctx, providerToken)
	if err != nil {
		return err
	}
	return api.sys(ctx).Transaction(func(tx *gorm.DB) error {
		var existing AIProviderTenantGrant
		err := tx.Where("tenant_id = ? AND provider_id = ?", tenantToken, providerID).First(&existing).Error
		if err == nil {
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		// Auto-mark the tenant's FIRST grant, and only that one — the tier axis's rule
		// (see GrantProviderToTier), probing the GRANT set for the same reason: a mark
		// set is something an operator can empty, so auto-marking on "no mark" would let
		// the next exception silently overturn an explicit ClearTenantDefault.
		first, err := tenantHasNoGrants(tx, tenantToken)
		if err != nil {
			return err
		}
		grant := AIProviderTenantGrant{ProviderID: providerID, IsDefault: first}
		grant.TenantId = tenantToken
		return tx.Create(&grant).Error
	})
}

// tenantHasNoGrants reports whether the tenant has no additive grant rows at all — i.e.
// whether the next grant is its first. An existence probe — see tierHasNoGrants.
func tenantHasNoGrants(tx *gorm.DB, tenantToken string) (bool, error) {
	var probe []int64
	res := tx.Model(&AIProviderTenantGrant{}).Select("1").
		Where("tenant_id = ?", tenantToken).Limit(1).Find(&probe)
	return res.RowsAffected == 0, res.Error
}

// promoteTenantDefault demotes the tenant's current default and promotes grantID inside
// the caller's transaction. Demote before promote, or the partial unique index rejects
// the second default — see promoteTierDefault.
func promoteTenantDefault(tx *gorm.DB, tenantToken string, grantID uint) error {
	if err := tx.Model(&AIProviderTenantGrant{}).
		Where("tenant_id = ? AND is_default = ?", tenantToken, true).
		Update("is_default", false).Error; err != nil {
		return err
	}
	return tx.Model(&AIProviderTenantGrant{}).
		Where("id = ?", grantID).Update("is_default", true).Error
}

// SetTenantDefault marks one of a tenant's additive grants as its default, demoting any
// previous one atomically. Returns gorm.ErrRecordNotFound when the pair is not granted:
// a default must be something the tenant actually holds, so this does not create the
// grant as a side effect (the tier axis's SetTierDefault rule).
//
// This is the REPAIR for an exception-only tenant. It matters because the mark it writes
// is only consulted when the tenant's tier marks nothing (pickDefault) — so an operator
// calling it for a tenant whose tier does mark a default changes stored state that has
// no effect today, and takes effect if the tier's mark is ever cleared. That is the
// honest behaviour for an additive exception, but it is not obvious from the call.
func (api *Api) SetTenantDefault(ctx context.Context, tenantToken, providerToken string) error {
	providerID, err := api.providerIDByToken(ctx, providerToken)
	if err != nil {
		return err
	}
	return api.sys(ctx).Transaction(func(tx *gorm.DB) error {
		var existing AIProviderTenantGrant
		if err := tx.Where("tenant_id = ? AND provider_id = ?", tenantToken, providerID).
			First(&existing).Error; err != nil {
			return err
		}
		if existing.IsDefault {
			return nil
		}
		return promoteTenantDefault(tx, tenantToken, existing.ID)
	})
}

// ClearTenantDefault leaves the tenant's additive grants in place but marks none of them
// default. Idempotent. For an exception-only tenant this means no default at all, so the
// caller must choose explicitly.
func (api *Api) ClearTenantDefault(ctx context.Context, tenantToken string) error {
	return api.sys(ctx).Model(&AIProviderTenantGrant{}).
		Where("tenant_id = ? AND is_default = ?", tenantToken, true).
		Update("is_default", false).Error
}

// RevokeProviderFromTenant removes a tenant's additive grant, reporting whether one was
// removed. This withdraws an EXCEPTION, not an entitlement: it cannot take away what
// the tenant's tier offers, because the tier's grants live in a different table and
// the menu is a union (see MenuForTenant). Idempotent.
func (api *Api) RevokeProviderFromTenant(ctx context.Context, tenantToken, providerToken string) (bool, error) {
	providerID, err := api.providerIDByToken(ctx, providerToken)
	if err != nil {
		return false, err
	}
	res := api.sys(ctx).Where("tenant_id = ? AND provider_id = ?", tenantToken, providerID).
		Delete(&AIProviderTenantGrant{})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// ListTenantGrants returns the additive grants for one tenant (operator surface, system
// context — the admin plane has no tenant of its own).
func (api *Api) ListTenantGrants(ctx context.Context, tenantToken string) ([]TenantGrant, error) {
	var grants []AIProviderTenantGrant
	if err := api.sys(ctx).Where("tenant_id = ?", tenantToken).Find(&grants).Error; err != nil {
		return nil, err
	}
	providers, err := providersByID(api, ctx, grants, func(g AIProviderTenantGrant) uint { return g.ProviderID })
	if err != nil {
		return nil, err
	}
	out := make([]TenantGrant, 0, len(grants))
	for _, g := range grants {
		p, ok := providers[g.ProviderID]
		if !ok {
			continue
		}
		out = append(out, TenantGrant{TenantToken: g.TenantId, Provider: p, IsDefault: g.IsDefault})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Provider.Token < out[j].Provider.Token })
	return out, nil
}

// providersByID loads the providers referenced by a slice of grants in one query,
// keyed by id. Generic over the grant type so both grant tables share the lookup.
func providersByID[T any](api *Api, ctx context.Context, grants []T, id func(T) uint) (map[uint]AIProvider, error) {
	if len(grants) == 0 {
		return map[uint]AIProvider{}, nil
	}
	ids := make([]uint, 0, len(grants))
	seen := map[uint]bool{}
	for _, g := range grants {
		if v := id(g); !seen[v] {
			seen[v] = true
			ids = append(ids, v)
		}
	}
	var providers []AIProvider
	if err := api.sys(ctx).Where("id IN ?", ids).Find(&providers).Error; err != nil {
		return nil, err
	}
	out := make(map[uint]AIProvider, len(providers))
	for _, p := range providers {
		out[p.ID] = p
	}
	return out, nil
}
