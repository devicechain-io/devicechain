// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/blob"
	util "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-user-management/branding"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/devicechain-io/dc-user-management/settings"
	"github.com/rs/zerolog/log"
)

// getSettingsService retrieves the settings Service injected into the data-plane
// context (main.go). The branding cascade reads the `branding.default` system
// setting as the tier below a tenant's override; the settings store is instance-
// global (no tenant scope), so it is safe to read from a tenant-scoped request.
func (r *SchemaResolver) getSettingsService(ctx context.Context) *settings.Service {
	return ctx.Value(ContextSettingsKey).(*settings.Service)
}

// TenantBrandingResolver resolves the TenantBranding type: a tenant's resolved
// white-labeling (ADR-038). A nil field means nothing overrides it and the console
// keeps its built-in look for that aspect.
type TenantBrandingResolver struct {
	b         branding.Branding
	updatedAt *string
}

func (r *TenantBrandingResolver) Title() *string      { return r.b.Title }
func (r *TenantBrandingResolver) Logo() *string       { return proxyLogoValue(r.b.Logo) }
func (r *TenantBrandingResolver) Primary() *string    { return r.b.Primary }
func (r *TenantBrandingResolver) Background() *string { return r.b.Background }
func (r *TenantBrandingResolver) Foreground() *string { return r.b.Foreground }
func (r *TenantBrandingResolver) Accent() *string     { return r.b.Accent }
func (r *TenantBrandingResolver) UpdatedAt() *string  { return r.updatedAt }

func (r *TenantBrandingResolver) LogoMaxHeight() *int32 {
	if r.b.LogoMaxHeight == nil {
		return nil
	}
	v := int32(*r.b.LogoMaxHeight)
	return &v
}

// Branding resolves the tenant's white-labeling: its override columns folded over
// the `branding.default` system setting (whose own default is the code floor), so
// the cascade always has a floor with no DB seed. A future customer tier inserts
// one Merge above with no contract change (ADR-038 §3.1).
func (r *TenantResolver) Branding(ctx context.Context) (*TenantBrandingResolver, error) {
	def, err := r.svc.getSettingsService(ctx).Get(ctx, settings.KeyBrandingDefault)
	if err != nil {
		return nil, err
	}
	// Degrade, never fail: a malformed or rule-invalid branding.default override
	// (the operator tier) must never break this query — it is the console's boot
	// query — nor propagate a bad value (e.g. an inline SVG logo) to every tenant.
	// The setSetting path validates it (settings_catalog.go), so this only trips on
	// a value stored before that gate; fall back to no system default (the tenant
	// override + the console's built-in floor still apply).
	var systemDefault branding.Branding
	if err := json.Unmarshal(def.Value, &systemDefault); err != nil || branding.Validate(systemDefault) != nil {
		log.Warn().Str("setting", settings.KeyBrandingDefault).Msg("ignoring malformed branding.default; using built-in floor")
		systemDefault = branding.Branding{}
	}
	resolved := branding.Merge(brandingFromTenant(r.t), systemDefault)
	// Version on the later of the tenant row and the operator default, so a client
	// keying its cache on updatedAt notices either tier changing.
	updatedAt := r.t.UpdatedAt
	if def.UpdatedAt != nil && def.UpdatedAt.After(updatedAt) {
		updatedAt = *def.UpdatedAt
	}
	return &TenantBrandingResolver{b: resolved, updatedAt: util.FormatTime(updatedAt)}, nil
}

// BrandingOverride resolves the tenant's RAW override columns with no cascade — a
// null field means "this tenant inherits." The editor reads it to show set-vs-
// inherited per field; unlike Branding it needs no settings lookup.
func (r *TenantResolver) BrandingOverride() *TenantBrandingResolver {
	return &TenantBrandingResolver{b: brandingFromTenant(r.t), updatedAt: util.FormatTime(r.t.UpdatedAt)}
}

// SetTenantBranding writes the caller's OWN tenant white-labeling (ADR-038 §3.2).
// Self-scoped to the tenant in the access token; gated on branding:write. Each
// null field clears that override (re-inherits). Validated server-side fail-closed
// before it is stored, and returns the tenant with freshly-resolved branding so
// the editing client writes it straight into cache (§1.2).
func (r *SchemaResolver) SetTenantBranding(ctx context.Context, args struct {
	Input tenantBrandingInput
}) (*TenantResolver, error) {
	if err := auth.Authorize(ctx, auth.BrandingWrite); err != nil {
		return nil, err
	}
	claims, ok := auth.ClaimsFromContext(ctx)
	if !ok {
		return nil, auth.ErrUnauthenticated
	}
	b := args.Input.toBranding()
	if err := branding.Validate(b); err != nil {
		return nil, err
	}
	t, err := r.getIdentityManager(ctx).SetTenantBranding(ctx, claims.Tenant, b)
	if err != nil {
		return nil, err
	}
	return &TenantResolver{t: t, svc: r}, nil
}

// SetTenantLogo sets the caller's OWN tenant logo to a Tier-0 value (an https URL
// or a bounded raster data: URI) or clears it (null) — self-scoped, gated on
// branding:write (ADR-038). A binary upload instead goes to the object store via
// POST /branding/logo (ADR-058). A blob storage reference is never accepted here:
// clients never hold one, and branding.Validate rejects anything that is not an
// https/data: value. When the previous logo was an object-store blob, it is
// garbage-collected (best-effort — a stale blob is harmless, never a correctness
// risk).
func (r *SchemaResolver) SetTenantLogo(ctx context.Context, args struct {
	Logo *string
}) (*TenantResolver, error) {
	if err := auth.Authorize(ctx, auth.BrandingWrite); err != nil {
		return nil, err
	}
	claims, ok := auth.ClaimsFromContext(ctx)
	if !ok {
		return nil, auth.ErrUnauthenticated
	}
	value := ""
	if args.Logo != nil {
		if v := strings.TrimSpace(*args.Logo); v != "" {
			if err := branding.Validate(branding.Branding{Logo: &v}); err != nil {
				return nil, err
			}
			value = v
		}
	}
	mgr := r.getIdentityManager(ctx)
	prev, err := mgr.SetTenantLogo(ctx, claims.Tenant, value)
	if err != nil {
		return nil, err
	}
	deleteReplacedLogoBlob(ctx, r.getBlobStore(ctx), prev, value)
	t, err := mgr.TenantByToken(ctx, claims.Tenant)
	if err != nil {
		return nil, err
	}
	return &TenantResolver{t: t, svc: r}, nil
}

// getBlobStore returns the object store injected into the data-plane context, or
// nil when the store is not configured (branding-logo upload/read then 503, and
// orphan-blob GC is skipped — the store is required only for object-store logos).
func (r *SchemaResolver) getBlobStore(ctx context.Context) blob.Store {
	v := ctx.Value(ContextBlobKey)
	if v == nil {
		return nil
	}
	s, _ := v.(blob.Store)
	return s
}

// deleteReplacedLogoBlob GCs a previous object-store logo when the logo reference
// changes. It is a no-op when there is no previous value, the value is unchanged,
// the previous value was a Tier-0 (non-blob) logo, or the store is unconfigured.
func deleteReplacedLogoBlob(ctx context.Context, store blob.Store, prev *string, newValue string) {
	if store == nil || prev == nil || *prev == newValue {
		return
	}
	ref, err := blob.ParseRef(*prev)
	if err != nil {
		return // a Tier-0 https/data: value — nothing in the object store to remove
	}
	if err := store.Delete(ctx, ref); err != nil {
		log.Warn().Err(err).Msg("failed to delete replaced branding logo blob")
	}
}

// proxyLogoValue maps a stored logo column value to the value a client uses in an
// <img>: an object-store (blob://) reference becomes the authorizing read-proxy
// path with a cache-busting version taken from the object id (which changes on
// every upload), while a Tier-0 https/data: value passes through unchanged. The raw
// storage reference is never exposed to the client.
func proxyLogoValue(stored *string) *string {
	if stored == nil {
		return nil
	}
	ref, err := blob.ParseRef(*stored)
	if err != nil {
		return stored // Tier-0 (https/data:) — usable directly
	}
	version := ref.Key
	if i := strings.LastIndexByte(version, '/'); i >= 0 {
		version = version[i+1:]
	}
	// The object id charset is [A-Za-z0-9._-] (blob.validateSegment), so it needs no
	// URL escaping.
	p := brandingLogoPath + "?v=" + version
	return &p
}

// tenantBrandingInput mirrors the TenantBrandingInput GraphQL input — the THEME
// fields only (logo is managed separately, see SetTenantLogo). Optional scalars
// arrive as pointers; a nil pointer clears that override.
type tenantBrandingInput struct {
	Title         *string
	LogoMaxHeight *int32
	Primary       *string
	Background    *string
	Foreground    *string
	Accent        *string
}

func (in tenantBrandingInput) toBranding() branding.Branding {
	return branding.Branding{
		Title:         in.Title,
		LogoMaxHeight: intPtr(in.LogoMaxHeight),
		Primary:       in.Primary,
		Background:    in.Background,
		Foreground:    in.Foreground,
		Accent:        in.Accent,
	}
}

// brandingFromTenant reads a tenant's override columns into a branding.Branding.
func brandingFromTenant(t *iam.Tenant) branding.Branding {
	return branding.Branding{
		Title:         t.BrandingTitle,
		Logo:          t.BrandingLogo,
		LogoMaxHeight: t.BrandingLogoMaxHeight,
		Primary:       t.BrandingPrimary,
		Background:    t.BrandingBackground,
		Foreground:    t.BrandingForeground,
		Accent:        t.BrandingAccent,
	}
}
