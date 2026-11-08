// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/geo"
	gqlcore "github.com/devicechain-io/dc-microservice/graphql"
	util "github.com/devicechain-io/dc-microservice/graphql"

	"github.com/devicechain-io/dc-device-management/model"
)

// ContextReportedFixSourceKey carries the ReportedFixSource that Device.effectivePosition reads a
// device's latest reported fix from. device-management does not store reported positions (the
// device-state area does), so the effective rule needs a reader the service is wired with.
const ContextReportedFixSourceKey = gqlcore.ContextKey("reported-fix-source")

// ReportedFixSource reads a device's latest REPORTED fix and says how old a fix may be and still
// count as fresh for the effective rule. It runs under the caller's own context, so a source that
// calls another area does so under the caller's token, never a service's.
type ReportedFixSource interface {
	ReportedFix(ctx context.Context, deviceToken string) (fix *geo.Fix, freshness time.Duration, err error)
}

// ErrNoReportedFixSource is what Device.effectivePosition answers for a device whose profile
// declares that it reports its position, when this build has no reader for reported fixes.
//
// 🔴 IT IS AN ERROR AND NOT THE INSTALLED POSITION. The effective rule prefers a fresh reported
// fix; answering the installed position when the reader is missing would be a plausible value that
// is wrong exactly when the device is moving, and nothing at the call site would show it.
var ErrNoReportedFixSource = errors.New("effectivePosition is not available for a device that reports " +
	"its position: this build has no reader for reported positions. Use installedPosition, or latestLocation " +
	"on the device-state area")

// InstalledPositionResolver resolves an installed position.
type InstalledPositionResolver struct {
	M model.InstalledPosition
}

func (r *InstalledPositionResolver) Latitude() float64  { return r.M.InstalledLat.Float64 }
func (r *InstalledPositionResolver) Longitude() float64 { return r.M.InstalledLon.Float64 }
func (r *InstalledPositionResolver) Elevation() *float64 {
	if !r.M.InstalledElev.Valid {
		return nil
	}
	return &r.M.InstalledElev.Float64
}
func (r *InstalledPositionResolver) Accuracy() *float64 {
	if !r.M.InstalledAccuracy.Valid {
		return nil
	}
	return &r.M.InstalledAccuracy.Float64
}
func (r *InstalledPositionResolver) Source() *string {
	if !r.M.InstalledSource.Valid {
		return nil
	}
	return &r.M.InstalledSource.String
}
func (r *InstalledPositionResolver) UpdatedAt() *string {
	if !r.M.InstalledUpdatedAt.Valid {
		return nil
	}
	return util.FormatTime(r.M.InstalledUpdatedAt.Time)
}

// installedPositionOf is the ONE function both Device.installedPosition and Asset.installedPosition
// answer through.
//
// 🔴 THE AUTHORITY IS CHECKED HERE, FOR THE INSTALLED SOURCE ONLY, AND NOTHING FALLS BACK. A caller
// without location:installed:read (or location:read, which implies it) gets null, and so does a
// caller with it on an entity that has no position; the withheld marker is a separate field,
// answered from the authority alone, so the payload does not say which of the two null is.
// This function never receives a reported fix, so it cannot return one.
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

// EffectivePositionResolver resolves the position the effective rule chose, with its source.
type EffectivePositionResolver struct {
	P   geo.Position
	Src geo.Source
}

func (r *EffectivePositionResolver) Latitude() float64  { return r.P.Lat }
func (r *EffectivePositionResolver) Longitude() float64 { return r.P.Lon }
func (r *EffectivePositionResolver) Elevation() *float64 {
	if !r.P.HasElevation {
		return nil
	}
	return &r.P.Elevation
}
func (r *EffectivePositionResolver) Source() string { return r.Src.String() }

// EffectivePosition is the position a map should draw for the device: its latest reported fix when
// its profile declares a position and the fix is fresh, else its installed position.
//
// 🔴 IT NEEDS location:read, THE AUTHORITY FOR A LIVE FIX. location:installed:read does not
// satisfy it, because the effective rule can hand back a reported fix; a caller holding only the
// installed authority reads installedPosition instead, and is never offered the effective rule.
// A caller without it gets FORBIDDEN, not a withheld marker: refusing is what keeps a caller that
// is not allowed to see live positions from probing for them.
func (r *DeviceResolver) EffectivePosition(ctx context.Context) (*EffectivePositionResolver, error) {
	if _, err := auth.AuthorizeScoped(ctx, auth.LocationRead); err != nil {
		return nil, err
	}
	// The query time is captured once, so the freshness comparison cannot straddle a clock tick.
	at := time.Now()

	var installed *geo.Position
	if p, ok := r.M.InstalledPosition.Position(); ok {
		installed = &p
	}

	var fix *geo.Fix
	var freshness time.Duration
	capability, err := r.S.GetApi(ctx).DeviceLocationDeclaration(ctx, r.M.Token)
	if err != nil {
		return nil, err
	}
	if capability.Declared() {
		src, _ := ctx.Value(ContextReportedFixSourceKey).(ReportedFixSource)
		if src == nil {
			return nil, ErrNoReportedFixSource
		}
		if fix, freshness, err = src.ReportedFix(ctx, r.M.Token); err != nil {
			return nil, fmt.Errorf("reading the reported position: %w", err)
		}
	}

	pos, source, ok := geo.Effective(fix, freshness, installed, at, geo.ModeEffective)
	if !ok {
		return nil, nil
	}
	return &EffectivePositionResolver{P: pos, Src: source}, nil
}

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
