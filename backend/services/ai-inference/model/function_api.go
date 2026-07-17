// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/devicechain-io/dc-microservice/core"
	"gorm.io/gorm"
)

// ErrUnknownFunction is returned when a caller names a function that is not in the
// platform's vocabulary (see AllFunctions). A stored assignment for a function nothing
// asks for is a row that looks like a configured capability and is dead, so the write
// path refuses it rather than accepting it and never using it.
var ErrUnknownFunction = errors.New("no such AI function")

// FunctionAssignment pairs a tenant's assigned function with the provider it names, for
// the admin surface.
type FunctionAssignment struct {
	Function string
	Provider AIProvider
}

// ResolveModelForFunction answers the only question the inference path asks: which model
// serves this tenant's call to this function? It returns (nil, nil) for "no model" — an
// answer, not a fault, which the caller turns into its own error with its own wording.
//
// THE RULE, in full, and note what is absent from it:
//
//	menu = MenuForTenant(tenant).Providers        // enabled ∩ (tier grants ∪ exceptions)
//	if an assignment for (tenant, function) exists:
//	    return that provider if it is ON the menu, else NONE
//	return the platform-baseline provider if it is ON the menu, else NONE
//
// What is absent is any read of a SET'S SIZE OR EMPTINESS. Not len(menu), not "the sole
// granted model", not "does the tier grant anything", not "does any mark survive". That
// absence is the entire point of this function and the reason the derived default was
// deleted rather than repaired: a rule that consults the size of a set an operator can
// change re-answers when they change it, and that shape shipped five times here under
// five different disguises (see function.go). Adding a parameter to carry a count is the
// only way to reintroduce it, which is deliberate — the signature is the guard.
//
// TWO PROPERTIES ARE LOAD-BEARING AND EACH KILLED A BUG:
//
//  1. AN ASSIGNMENT OFF THE MENU RESOLVES TO NONE — NEVER TO A SUBSTITUTE. The tenant
//     chose a model; if that model is revoked or disabled, the honest answer is that the
//     door is shut, not that some other model quietly took the call. Substituting would
//     silently re-route a tenant's prompts and its SPEND to a model nobody chose, which
//     is precisely what an operator disabling a model during an incident must not
//     trigger. The assignment is not destroyed by this — it is not honoured today and
//     honoured again the moment the entitlement returns.
//
//  2. AI IS A TIERED ENTITLEMENT: NO MENU ⇒ NO MODEL, BASELINE OR NOT. The baseline is
//     the lowest-common-denominator model for a tenant that never chose one; it is NOT a
//     free tier. A tenant whose tier grants nothing and who holds no exception resolves
//     to NONE even with a baseline designated, because the baseline is checked AGAINST
//     THE MENU like any other candidate. Skipping that check would hand every unpackaged
//     tenant on the instance a working AI door, which is the product invariant inverted.
//
// The baseline is a STORED designation (AIProvider.IsPlatformBaseline), not the sole
// enabled provider or the first one registered — same rule, same reason.
func (api *Api) ResolveModelForFunction(ctx context.Context, tierToken, function string) (*AIProvider, error) {
	if !ValidFunction(function) {
		return nil, fmt.Errorf("%w: %q", ErrUnknownFunction, function)
	}
	// Guard the tenant exactly as MenuForTenant does, and for the same reasons — stated
	// here rather than left to the call below because this function reads the tenant's
	// own assignment row as well, so it depends on the scoping callback twice.
	if _, ok := core.TenantFromContext(ctx); !ok {
		return nil, core.ErrNoTenant
	}
	// core.WithSystemContext PRESERVES the tenant while DISABLING the scoping callback,
	// so the guard above passes while the isolation both reads below depend on is
	// switched off — every tenant's assignments would be candidates for this one.
	if core.IsSystemContext(ctx) {
		return nil, fmt.Errorf("a tenant's model cannot be resolved in the system context: it would read across tenants")
	}

	menu, err := api.MenuForTenant(ctx, tierToken)
	if err != nil {
		return nil, err
	}
	// onMenu is the entitlement check every candidate passes, whichever branch produced
	// it. It reads a MEMBERSHIP, not a size — the distinction this whole design rests on.
	onMenu := func(id uint) *AIProvider {
		for i := range menu.Providers {
			if menu.Providers[i].ID == id {
				return &menu.Providers[i]
			}
		}
		return nil
	}

	// Tenant-scoped: the callback injects the tenant predicate, so this read carries no
	// hand-written `WHERE tenant_id = ?` — the platform's un-skippable path rather than a
	// second, forgettable copy of an isolation rule.
	var assignment AIFunctionAssignment
	err = api.RDB.DB(ctx).Where("function = ?", function).First(&assignment).Error
	switch {
	case err == nil:
		// The tenant chose. Honour it iff it is still entitled, and otherwise answer NONE
		// — never fall through to the baseline, which would be the substitution property 1
		// exists to forbid. A tenant that chose is not a tenant that has no opinion.
		return onMenu(assignment.ProviderID), nil
	case errors.Is(err, gorm.ErrRecordNotFound):
	default:
		return nil, err
	}

	// Nobody chose, so the platform's lowest-common-denominator answers — still only if
	// the tenant is entitled to it (property 2).
	baseline, err := api.PlatformBaseline(ctx)
	if err != nil {
		return nil, err
	}
	if baseline == nil {
		return nil, nil
	}
	return onMenu(baseline.ID), nil
}

// SetFunctionModel stores a tenant's choice of model for one function, replacing any
// previous choice. One row per (tenant, function) — uix_ai_function_assignment is the
// storage backstop.
//
// IT DELIBERATELY DOES NOT CHECK THE MENU. Entitlement is checked at RESOLVE time, on
// every call, so an assignment SURVIVES a temporary revoke: an operator who revokes a
// model and grants it back has not silently destroyed the tenant's choice, and a tenant
// whose tier is being re-packaged does not need its assignment re-entered afterwards.
// Checking here instead would make the choice a casualty of an unrelated operator act,
// and would buy nothing — a menu check at write time is stale the instant a grant moves,
// so it could never be the thing enforcement rests on.
//
// Runs in the system context because the caller is an operator on the identity-token
// admin plane, which holds no tenant — so TenantId is set explicitly here rather than
// stamped by the callback. That is the sanctioned bypass; the read path keeps it.
func (api *Api) SetFunctionModel(ctx context.Context, tenantToken, function, providerToken string) error {
	if err := core.ValidateToken(tenantToken); err != nil {
		return fmt.Errorf("invalid tenant token: %w", err)
	}
	if !ValidFunction(function) {
		return fmt.Errorf("%w: %q", ErrUnknownFunction, function)
	}
	providerID, err := api.providerIDByToken(ctx, providerToken)
	if err != nil {
		return err
	}
	return api.sys(ctx).Transaction(func(tx *gorm.DB) error {
		var existing AIFunctionAssignment
		err := tx.Where("tenant_id = ? AND function = ?", tenantToken, function).First(&existing).Error
		switch {
		case err == nil:
			if existing.ProviderID == providerID {
				return nil
			}
			return tx.Model(&AIFunctionAssignment{}).
				Where("id = ?", existing.ID).Update("provider_id", providerID).Error
		case errors.Is(err, gorm.ErrRecordNotFound):
		default:
			return err
		}
		created := AIFunctionAssignment{Function: function, ProviderID: providerID}
		created.TenantId = tenantToken
		return tx.Create(&created).Error
	})
}

// ClearFunctionModel removes a tenant's choice for one function, reporting whether a row
// was removed. Hard delete; idempotent. The tenant falls back to the platform baseline
// (if it is entitled to it) — which is the state it was in before it ever chose, not a
// new one.
func (api *Api) ClearFunctionModel(ctx context.Context, tenantToken, function string) (bool, error) {
	if !ValidFunction(function) {
		return false, fmt.Errorf("%w: %q", ErrUnknownFunction, function)
	}
	res := api.sys(ctx).Where("tenant_id = ? AND function = ?", tenantToken, function).
		Delete(&AIFunctionAssignment{})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// ListFunctionAssignments returns one tenant's stored choices with their providers, for
// the operator surface (system context — the admin plane has no tenant of its own).
// Ordered by function token so the surface is stable for a caller rendering it.
//
// It lists what the tenant CHOSE, not what it would GET: an assignment naming a model
// the tenant is no longer entitled to still appears here, because an operator who cannot
// see the stale choice cannot fix it. Resolution is where entitlement applies.
func (api *Api) ListFunctionAssignments(ctx context.Context, tenantToken string) ([]FunctionAssignment, error) {
	var assignments []AIFunctionAssignment
	if err := api.sys(ctx).Where("tenant_id = ?", tenantToken).Find(&assignments).Error; err != nil {
		return nil, err
	}
	providers, err := providersByID(api, ctx, assignments, func(a AIFunctionAssignment) uint { return a.ProviderID })
	if err != nil {
		return nil, err
	}
	out := make([]FunctionAssignment, 0, len(assignments))
	for _, a := range assignments {
		p, ok := providers[a.ProviderID]
		if !ok {
			// The FK makes this unreachable; skip rather than surface a zero provider.
			continue
		}
		out = append(out, FunctionAssignment{Function: a.Function, Provider: p})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Function < out[j].Function })
	return out, nil
}

// SetPlatformBaseline designates a provider as the instance's baseline model: the one a
// tenant gets for a function it never assigned, provided it is entitled to that model
// (ResolveModelForFunction). At most one instance-wide — uix_ai_providers_baseline is
// the storage backstop.
//
// The demote lands BEFORE the promote, in ONE transaction, or the partial unique index
// rejects the second baseline. (The same ordering the retired promoteTierDefault used;
// the shape was never the problem there — inferring WHO to promote was.)
//
// The baseline is a designation an operator makes and can read back, never an inference
// from the provider list. "The only enabled provider" and "the first one registered" are
// the same non-monotonic shape as the sole-model default: they re-answer when the list
// changes.
func (api *Api) SetPlatformBaseline(ctx context.Context, providerToken string) error {
	providerID, err := api.providerIDByToken(ctx, providerToken)
	if err != nil {
		return err
	}
	return api.sys(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&AIProvider{}).
			Where("is_platform_baseline = ?", true).
			Update("is_platform_baseline", false).Error; err != nil {
			return err
		}
		return tx.Model(&AIProvider{}).
			Where("id = ?", providerID).Update("is_platform_baseline", true).Error
	})
}

// ClearPlatformBaseline leaves every provider registered but designates none as the
// baseline. Idempotent. Afterwards a tenant that has assigned no model for a function
// resolves to NONE and must choose explicitly.
func (api *Api) ClearPlatformBaseline(ctx context.Context) error {
	return api.sys(ctx).Model(&AIProvider{}).
		Where("is_platform_baseline = ?", true).
		Update("is_platform_baseline", false).Error
}

// PlatformBaseline returns the designated baseline provider, or nil when none is
// designated. Nil is an answer: an instance whose operator designated no baseline serves
// only the tenants that chose explicitly.
func (api *Api) PlatformBaseline(ctx context.Context) (*AIProvider, error) {
	var found AIProvider
	err := api.sys(ctx).Where("is_platform_baseline = ?", true).First(&found).Error
	switch {
	case err == nil:
		return &found, nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		return nil, nil
	default:
		return nil, err
	}
}

// assertProviderNotAssigned refuses the delete of a provider that a tenant has assigned
// to a function, or that is the platform baseline. Called by assertProviderNotGranted.
func (api *Api) assertProviderNotAssigned(ctx context.Context, providerID uint) ([]string, bool, error) {
	var assignments []AIFunctionAssignment
	if err := api.sys(ctx).Where("provider_id = ?", providerID).Find(&assignments).Error; err != nil {
		return nil, false, err
	}
	tenants := make([]string, 0, len(assignments))
	seen := map[string]bool{}
	for _, a := range assignments {
		if !seen[a.TenantId] {
			seen[a.TenantId] = true
			tenants = append(tenants, a.TenantId)
		}
	}
	sort.Strings(tenants)

	var provider AIProvider
	if err := api.sys(ctx).Where("id = ?", providerID).First(&provider).Error; err != nil {
		return nil, false, err
	}
	return tenants, provider.IsPlatformBaseline, nil
}
