// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-user-management/locale"
	"github.com/devicechain-io/dc-user-management/settingsdefs"
	"github.com/rs/zerolog/log"
)

// Locale resolves the tenant's EFFECTIVE default locale: its own override folded over
// the `locale.default` system setting (ADR-066 sub-workstream d). Self-scoped like
// Branding and Basemap, and requiring no authority for the same reason — gating the
// READ would leave the console in the wrong language for exactly the people the
// default exists to serve.
//
// 🔴 What this returns is rung 2 of the console's FOUR-rung precedence, not the
// answer. The console applies it through applyTenantDefaultLocale, which drops it if
// the user has made an explicit choice (rung 1) and if it is not a catalog this build
// ships. Rungs 3 (the browser's advertised languages) and 4 (`en`) are the browser's
// own business and are never computed here — the server has no view of either.
func (r *TenantResolver) Locale(ctx context.Context) (*string, error) {
	def, err := r.svc.getSettingsService(ctx).Get(ctx, settingsdefs.KeyLocaleDefault)
	if err != nil {
		return nil, err
	}
	return resolveLocale(r.t.Locale, def.Value), nil
}

// resolveLocale folds a tenant's override over the operator's STORED default value.
//
// A package-level function taking bytes rather than a method reaching for a service,
// for the reason resolveBasemap states: the two things most worth pinning are testable
// without a database. That the tenant is the HIGH tier (a swapped argument order
// silently makes the operator win over every tenant, and every other test in this
// package still passes), and that a malformed stored default degrades rather than
// propagating.
//
// Degrade, never fail — the same stance branding and basemap take, and for the same
// reason: this rides the console's boot query, and a 500 here would cost the user the
// whole shell rather than one preference. setSetting validates the value at the mint
// point (settings_catalog.go), so this only trips on one stored before that gate
// existed; fall back to no operator default, leaving the tenant's own override (or
// nothing, which the console reads as "use the browser") in force.
func resolveLocale(tenantOverride *string, storedDefault []byte) *string {
	var systemDefault *string
	if err := json.Unmarshal(storedDefault, &systemDefault); err != nil || locale.Validate(locale.Normalize(systemDefault)) != nil {
		// (Merge normalizes both tiers itself, so a blank stored here cannot mask
		// anything; this branch is only about a value that is malformed or rule-invalid.)
		log.Warn().Str("setting", settingsdefs.KeyLocaleDefault).Msg("ignoring malformed locale.default; resolving without an operator default")
		systemDefault = nil
	}
	return locale.Merge(tenantOverride, systemDefault)
}

// LocaleOverride resolves the tenant's RAW override column with no cascade — null
// means "this tenant inherits". The editor reads it to show set-vs-inherited; unlike
// Locale it needs no settings lookup.
func (r *TenantResolver) LocaleOverride() *string {
	return locale.Normalize(r.t.Locale)
}

// SetTenantLocale writes the caller's OWN tenant default locale (ADR-066
// sub-workstream d). Self-scoped to the tenant in the access token; gated on
// locale:write — deliberately NOT branding:write, for the reason on auth.LocaleWrite:
// this one value re-languages the console for every member who has not chosen
// otherwise, which plausibly belongs to different people than the brand does.
//
// A null locale clears the override, re-inheriting the operator's `locale.default`.
// Validated server-side, fail-closed, before anything is stored.
func (r *SchemaResolver) SetTenantLocale(ctx context.Context, args struct {
	Locale *string
}) (*TenantResolver, error) {
	if err := auth.Authorize(ctx, auth.LocaleWrite); err != nil {
		return nil, err
	}
	claims, ok := auth.ClaimsFromContext(ctx)
	if !ok {
		return nil, auth.ErrUnauthenticated
	}
	l, err := prepareLocaleWrite(args.Locale)
	if err != nil {
		return nil, err
	}
	t, err := r.getIdentityManager(ctx).SetTenantLocale(ctx, claims.Tenant, l)
	if err != nil {
		return nil, err
	}
	return &TenantResolver{t: t, svc: r}, nil
}

// prepareLocaleWrite turns a submitted locale into the value to store, or an error.
//
// 🔴 Normalizing and validating are ONE step on purpose, and the reason is testability
// rather than tidiness — the same argument prepareBasemapWrite makes. Reaching
// SetTenantLocale's storage call needs a DB-backed identity.Manager, so a test cannot
// observe what was written, which means a resolver that quietly stopped normalizing
// would go unnoticed. Bundled here, "the resolver stopped normalizing" and "the
// resolver stopped validating" are the same mutation, and the latter IS observable
// (the authority test drives an invalid locale through the gate and requires the
// validation error).
//
// Normalizing first is what makes "clear this" expressible: a client that means it may
// send "" rather than null — the console maps an emptied field to null, dcctl, the
// SDKs and any direct GraphQL caller need not — and a stored blank is not the same as
// an absent one. It WINS the cascade and masks the operator's instance default. It
// also keeps the stored bytes identical to the bytes that were validated, rather than
// storing padding for every reader to trim again.
func prepareLocaleWrite(in *string) (*string, error) {
	l := locale.Normalize(in)
	if err := locale.Validate(l); err != nil {
		return nil, err
	}
	return l, nil
}
