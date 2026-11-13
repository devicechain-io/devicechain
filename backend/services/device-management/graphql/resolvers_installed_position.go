// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-microservice/auth"
	util "github.com/devicechain-io/dc-microservice/graphql"

	"github.com/devicechain-io/dc-device-management/model"
)

// InstalledPositionResolver resolves an installed position.
type InstalledPositionResolver struct {
	M model.InstalledPosition
}

// Latitude and Longitude are only ever resolved for a position that has both (installedPositionOf
// and the set mutation guarantee it), so the pointers are not nil here.
func (r *InstalledPositionResolver) Latitude() float64   { return *r.M.InstalledLat }
func (r *InstalledPositionResolver) Longitude() float64  { return *r.M.InstalledLon }
func (r *InstalledPositionResolver) Elevation() *float64 { return r.M.InstalledElev }
func (r *InstalledPositionResolver) Accuracy() *float64  { return r.M.InstalledAccuracy }
func (r *InstalledPositionResolver) Source() *string     { return r.M.InstalledSource }
func (r *InstalledPositionResolver) UpdatedAt() *string {
	if r.M.InstalledUpdatedAt == nil {
		return nil
	}
	return util.FormatTime(*r.M.InstalledUpdatedAt)
}

// installedPositionOf is the ONE function both Device.installedPosition and Asset.installedPosition
// answer through.
//
// 🔴 THE AUTHORITY IS CHECKED HERE, FOR THE INSTALLED SOURCE ONLY, AND NOTHING FALLS BACK. A caller
// without location:installed:read (or location:read, which implies it) gets null, and so does a
// caller with it on an entity that has no position; the withheld marker is a separate field,
// answered from the authority alone, so the payload does not say which of the two null is.
// This function never receives a reported fix, so it cannot return one. (A position that may be a
// live fix, and so needs location:read, belongs to a later slice that wires a reader for reported
// fixes; it is deliberately not served here.)
func installedPositionOf(ctx context.Context, p model.InstalledPosition) *InstalledPositionResolver {
	if !auth.CanReadInstalledPosition(ctx) {
		return nil
	}
	if _, ok := p.Position(); !ok {
		return nil
	}
	return &InstalledPositionResolver{M: p}
}

// positionWithheld is true for EVERY entity a caller without the installed-position authority reads,
// whether or not the entity has a position, and false for every entity a holder reads. It is a
// function of the caller alone, so it cannot reveal whether a position exists.
func positionWithheld(ctx context.Context) bool { return !auth.CanReadInstalledPosition(ctx) }

func (r *DeviceResolver) InstalledPosition(ctx context.Context) *InstalledPositionResolver {
	return installedPositionOf(ctx, r.M.InstalledPosition)
}

func (r *DeviceResolver) PositionWithheld(ctx context.Context) bool { return positionWithheld(ctx) }

func (r *AssetResolver) InstalledPosition(ctx context.Context) *InstalledPositionResolver {
	return installedPositionOf(ctx, r.M.InstalledPosition)
}

func (r *AssetResolver) PositionWithheld(ctx context.Context) bool { return positionWithheld(ctx) }

// BoundaryFence is the geofence that bounds the area, or null when none does.
//
// A link whose fence has since been deleted is an ERROR, not null: null means "this area has no
// boundary", and the stored link says it does. Like metadata, a fence's geometry is operator
// configuration, so a scoped caller gets null.
func (r *AreaResolver) BoundaryFence(ctx context.Context) (*GeoFenceResolver, error) {
	if !r.M.BoundaryFenceToken.Valid || isScopedCaller(ctx) {
		return nil, nil
	}
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}
	found, err := r.S.GetApi(ctx).GeoFencesByToken(ctx, []string{r.M.BoundaryFenceToken.String})
	if err != nil {
		return nil, err
	}
	if len(found) == 0 {
		return nil, fmt.Errorf("the boundary fence %q of this area no longer exists", r.M.BoundaryFenceToken.String)
	}
	return &GeoFenceResolver{M: *found[0], S: r.S, C: r.C}, nil
}

// SetInstalledPosition records where a device or an asset was installed.
//
// Writing needs device:write, the authority every other edit of these entities takes. The entity
// is looked up through the caller's scope, so one outside it is NOT_FOUND, as it is for a read; a
// scoped caller is then refused on an entity it can see, because the stores do not take a scoped
// update of these tables. The position is validated before the database is touched.
func (r *SchemaResolver) SetInstalledPosition(ctx context.Context, args struct {
	Family   string
	Token    string
	Position model.InstalledPositionInput
}) (*InstalledPositionResolver, error) {
	if _, err := auth.AuthorizeScoped(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}
	stored, err := r.GetApi(ctx).SetInstalledPosition(ctx, args.Family, args.Token, args.Position)
	if err != nil {
		return nil, err
	}
	return &InstalledPositionResolver{M: *stored}, nil
}

// ClearInstalledPosition removes the installed position of a device or an asset.
func (r *SchemaResolver) ClearInstalledPosition(ctx context.Context, args struct {
	Family string
	Token  string
}) (bool, error) {
	if _, err := auth.AuthorizeScoped(ctx, auth.DeviceWrite); err != nil {
		return false, err
	}
	if err := r.GetApi(ctx).ClearInstalledPosition(ctx, args.Family, args.Token); err != nil {
		return false, err
	}
	return true, nil
}
