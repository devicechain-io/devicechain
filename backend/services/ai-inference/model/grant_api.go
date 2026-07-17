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

// ErrProviderInUse refuses the delete of a provider that anything still points at: a
// tier or tenant GRANT, a tenant's function ASSIGNMENT, or the platform-BASELINE
// designation. It names every reason so the operator can act rather than guess
// (ADR-044's ErrEntityInUse shape, as user-management already uses for a tier that
// tenants reference).
var ErrProviderInUse = errors.New("provider is still granted and cannot be deleted")

// ErrUnknownProvider is returned when a grant names a provider token that does not
// exist. The FK would reject the insert anyway; catching it here gives the operator a
// message about a token rather than a constraint violation about an id.
var ErrUnknownProvider = errors.New("no provider with that token")

// Menu is a tenant's resolved set of AI models: everything it may draft with.
//
// It carries ONLY the set. Which model a given call uses is a separate question with a
// separate answer — a stored (tenant, function) assignment, falling back to the platform
// baseline (Api.ResolveModelForFunction). Menu once carried a Default field alongside
// the set, and co-locating them is what made it natural to compute one from the other;
// the answer is not a property of the menu, so it does not live on the menu.
type Menu struct {
	// Providers are the ENABLED providers the tenant may use, ordered by token so the
	// surface is stable for a caller rendering it.
	Providers []AIProvider
}

// TierGrant pairs a tier grant with its provider, for the admin surface.
type TierGrant struct {
	TierToken string
	Provider  AIProvider
}

// TenantGrant pairs a per-tenant additive grant with its provider.
type TenantGrant struct {
	TenantToken string
	Provider    AIProvider
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
// IT RESOLVES A SET AND NOTHING ELSE. Which model a call actually uses is
// ResolveModelForFunction's question, answered from a stored assignment — this function
// has no opinion on it and holds no mark to express one with. That separation is the fix
// for a defect that shipped five times: every instance was some flavour of deriving the
// answer from a property of these sets ("the sole granted model", "the first grant",
// "does the tier grant anything"), and each derivation re-answered when an operator
// changed the set. See function.go for the full history.
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
	for _, g := range tierGrants {
		if !seen[g.ProviderID] {
			seen[g.ProviderID] = true
			ids = append(ids, g.ProviderID)
		}
	}
	for _, g := range tenantGrants {
		if !seen[g.ProviderID] {
			seen[g.ProviderID] = true
			ids = append(ids, g.ProviderID)
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

	return &Menu{Providers: providers}, nil
}

// assertProviderNotGranted refuses the delete of a provider that anything still points
// at, naming what holds it. Called by DeleteAIProvider before the row is removed.
//
// It covers all three kinds of reference, for one reason stated once: a delete must not
// silently take a capability away from a live tenant. A GRANT means somebody is
// entitled to the model; an ASSIGNMENT means a tenant has chosen it for a function and
// would silently fall back to the baseline (or to nothing) if it vanished; the BASELINE
// designation means every tenant that never chose is using it, which is the widest blast
// radius of the three and the least visible. Each gets a legible refusal naming what to
// undo, rather than a constraint violation about an id — or, for the baseline, no error
// at all, since no FK protects a column on the provider's own row.
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
	assignedTo, isBaseline, err := api.assertProviderNotAssigned(ctx, providerID)
	if err != nil {
		return err
	}
	if len(tierGrants) == 0 && tenantCount == 0 && len(assignedTo) == 0 && !isBaseline {
		return nil
	}

	tiers := make([]string, 0, len(tierGrants))
	for _, g := range tierGrants {
		tiers = append(tiers, g.TierToken)
	}
	sort.Strings(tiers)

	// One refusal naming every reason, rather than one reason at a time: an operator
	// clearing them one by one and re-running the delete learns the next obstacle only
	// after acting on the last, which turns a single legible refusal into a guessing game.
	reasons := make([]string, 0, 3)
	if len(tiers) > 0 {
		reasons = append(reasons, fmt.Sprintf("granted to tier(s) %s", strings.Join(tiers, ", ")))
	}
	if tenantCount > 0 {
		reasons = append(reasons, fmt.Sprintf("granted to %d tenant(s)", tenantCount))
	}
	if len(assignedTo) > 0 {
		reasons = append(reasons, fmt.Sprintf("assigned to an AI function by tenant(s) %s",
			strings.Join(assignedTo, ", ")))
	}
	if isBaseline {
		reasons = append(reasons, "designated as the platform baseline model")
	}
	return fmt.Errorf("%w: %s; remove those first", ErrProviderInUse, strings.Join(reasons, "; "))
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

// GrantProviderToTier offers a provider to every tenant at a tier — an ENTITLEMENT and
// nothing more. Idempotent: re-granting an existing pair updates nothing and is not an
// error.
//
// It takes no makeDefault, and there is no auto-mark: granting says a model is on the
// menu, never that anything uses it. The two are separate acts because fusing them is
// what made the answer a property of the grant set, and therefore non-monotonic — the
// auto-mark's own probe went through three revisions before the mechanism was deleted.
// Which model serves a call is SetFunctionModel's business (per tenant) or
// SetPlatformBaseline's (per instance), and neither is disturbed by a grant.
//
// tierToken is not validated against user-management: this service holds a service
// token and the tier catalog is on user-management's identity-only admin plane, so
// there is no door this credential can reach to ask. A grant naming an unknown tier
// is inert (no tenant reports that tier), and the admin console shows it as unknown
// rather than hiding it.
func (api *Api) GrantProviderToTier(ctx context.Context, tierToken, providerToken string) error {
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
			return nil
		case errors.Is(err, gorm.ErrRecordNotFound):
		default:
			return err
		}
		return tx.Create(&AIProviderTierGrant{TierToken: tierToken, ProviderID: providerID}).Error
	})
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
		out = append(out, TierGrant{TierToken: g.TierToken, Provider: p})
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
		grant := AIProviderTenantGrant{ProviderID: providerID}
		grant.TenantId = tenantToken
		return tx.Create(&grant).Error
	})
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
		out = append(out, TenantGrant{TenantToken: g.TenantId, Provider: p})
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
