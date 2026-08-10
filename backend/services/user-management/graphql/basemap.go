// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"encoding/json"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-user-management/basemap"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/devicechain-io/dc-user-management/settings"
	"github.com/rs/zerolog/log"
)

// TenantBasemapResolver resolves the TenantBasemap type (ADR-079): the tile source a
// tenant's maps draw on, and the view they open at when there is nothing to fit.
type TenantBasemapResolver struct {
	b basemap.Basemap
}

func (r *TenantBasemapResolver) TileUrl() *string     { return r.b.TileURL }
func (r *TenantBasemapResolver) Attribution() *string { return r.b.Attribution }
func (r *TenantBasemapResolver) CenterLat() *float64  { return r.b.CenterLat }
func (r *TenantBasemapResolver) CenterLon() *float64  { return r.b.CenterLon }
func (r *TenantBasemapResolver) Zoom() *float64       { return r.b.Zoom }

// Basemap resolves the tenant's effective basemap: its override columns folded over
// the `basemap.default` system setting (ADR-079). Self-scoped like Branding, and
// requiring no authority for the same reason — gating the READ would leave the map
// blank for exactly the people who need it.
//
// 🔴 The fold is basemap.Merge, not a field-by-field overlay: the tile URL and its
// attribution resolve together, so a tenant that names its own tile source never
// inherits the operator's credit line for it.
func (r *TenantResolver) Basemap(ctx context.Context) (*TenantBasemapResolver, error) {
	def, err := r.svc.getSettingsService(ctx).Get(ctx, settings.KeyBasemapDefault)
	if err != nil {
		return nil, err
	}
	return &TenantBasemapResolver{b: resolveBasemap(basemapFromTenant(r.t), def.Value)}, nil
}

// resolveBasemap folds a tenant's override over the operator's STORED default value.
//
// It is a package-level function taking bytes rather than a method reaching for a
// service, so the two things most worth pinning are testable without a database: that
// the tenant is the HIGH tier (a swapped argument order silently makes the operator
// win over every tenant, and every other test in this package still passes), and that
// a malformed stored default degrades rather than propagating.
//
// Degrade, never fail — the same stance the branding cascade takes, and for the same
// reason: this rides the console's boot query. setSetting validates the value at the
// mint point (settings_catalog.go), so this only trips on one stored before that gate
// existed; fall back to no operator default, leaving the tenant's own override (or no
// basemap at all) in force.
func resolveBasemap(tenantOverride basemap.Basemap, storedDefault []byte) basemap.Basemap {
	var systemDefault basemap.Basemap
	if err := json.Unmarshal(storedDefault, &systemDefault); err != nil || basemap.Validate(systemDefault) != nil {
		// (Merge normalizes both tiers itself, so a blank stored here cannot mask
		// anything; this branch is only about a value that is malformed or rule-invalid.)
		log.Warn().Str("setting", settings.KeyBasemapDefault).Msg("ignoring malformed basemap.default; resolving without an operator default")
		systemDefault = basemap.Basemap{}
	}
	return basemap.Merge(tenantOverride, systemDefault)
}

// BasemapOverride resolves the tenant's RAW override columns with no cascade — a null
// field means "this tenant inherits". The editor reads it to show set-vs-inherited;
// unlike Basemap it needs no settings lookup.
func (r *TenantResolver) BasemapOverride() *TenantBasemapResolver {
	return &TenantBasemapResolver{b: basemapFromTenant(r.t)}
}

// SetTenantBasemap writes the caller's OWN tenant basemap (ADR-079). Self-scoped to
// the tenant in the access token; gated on basemap:write — deliberately NOT
// branding:write, because the tile URL carries the tenant's provider credential and
// the two grants should not imply each other (see auth.BasemapWrite).
//
// It is a full replace: a null field clears that override. Validated server-side,
// fail-closed, before anything is stored — so in particular a tile URL arriving
// without its attribution is refused here rather than rendering uncredited later.
func (r *SchemaResolver) SetTenantBasemap(ctx context.Context, args struct {
	Input tenantBasemapInput
}) (*TenantResolver, error) {
	if err := auth.Authorize(ctx, auth.BasemapWrite); err != nil {
		return nil, err
	}
	claims, ok := auth.ClaimsFromContext(ctx)
	if !ok {
		return nil, auth.ErrUnauthenticated
	}
	// Normalize BEFORE validating and storing: a client that means "clear this" may
	// send "" rather than null (the console sends null; dcctl, the SDKs and any direct
	// caller need not), and a stored blank is not the same as an absent one — it wins
	// the cascade and masks the operator's instance default.
	b, err := prepareBasemapWrite(args.Input)
	if err != nil {
		return nil, err
	}
	t, err := r.getIdentityManager(ctx).SetTenantBasemap(ctx, claims.Tenant, b)
	if err != nil {
		return nil, err
	}
	return &TenantResolver{t: t, svc: r}, nil
}

// prepareBasemapWrite turns a submitted input into the value to store, or an error.
//
// 🔴 Normalizing and validating are ONE step on purpose, and the reason is testability
// rather than tidiness. Reaching SetTenantBasemap's storage call needs a DB-backed
// identity.Manager, so a test cannot observe what was written — which means a resolver
// that quietly stopped normalizing would go unnoticed. Bundled here, "the resolver
// stopped normalizing" and "the resolver stopped validating" are the same mutation,
// and the latter IS observable (the authority test drives an invalid input through the
// gate and requires the validation error).
//
// Normalizing first is what makes "clear this" expressible: a client that means it may
// send "" rather than null — the console maps an emptied field to null, dcctl, the SDKs
// and any direct GraphQL caller need not — and a stored blank is not the same as an
// absent one. It WINS the cascade and masks the operator's instance default, handing a
// tenant a blank map on an instance that has one configured. It also keeps the stored
// bytes identical to the bytes that were validated, rather than storing padding for
// every reader to trim again.
func prepareBasemapWrite(in tenantBasemapInput) (basemap.Basemap, error) {
	b := basemap.Normalize(in.toBasemap())
	if err := basemap.Validate(b); err != nil {
		return basemap.Basemap{}, err
	}
	return b, nil
}

// tenantBasemapInput mirrors the TenantBasemapInput GraphQL input. Optional scalars
// arrive as pointers; a nil pointer clears that override.
type tenantBasemapInput struct {
	TileUrl     *string
	Attribution *string
	CenterLat   *float64
	CenterLon   *float64
	Zoom        *float64
}

func (in tenantBasemapInput) toBasemap() basemap.Basemap {
	return basemap.Basemap{
		TileURL:     in.TileUrl,
		Attribution: in.Attribution,
		CenterLat:   in.CenterLat,
		CenterLon:   in.CenterLon,
		Zoom:        in.Zoom,
	}
}

// basemapFromTenant reads a tenant's override columns into a basemap.Basemap.
func basemapFromTenant(t *iam.Tenant) basemap.Basemap {
	return basemap.Basemap{
		TileURL:     t.BasemapTileURL,
		Attribution: t.BasemapAttribution,
		CenterLat:   t.BasemapCenterLat,
		CenterLon:   t.BasemapCenterLon,
		Zoom:        t.BasemapZoom,
	}
}
