// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	util "github.com/devicechain-io/dc-microservice/graphql"
	gql "github.com/graph-gophers/graphql-go"
)

// --------------------
// Device type resolver
// --------------------

type DeviceTypeResolver struct {
	M model.DeviceType
	S *SchemaResolver
	C context.Context
}

func (r *DeviceTypeResolver) Id() gql.ID {
	return gql.ID(fmt.Sprint(r.M.ID))
}

func (r *DeviceTypeResolver) CreatedAt() *string {
	return util.FormatTime(r.M.CreatedAt)
}

func (r *DeviceTypeResolver) UpdatedAt() *string {
	return util.FormatTime(r.M.UpdatedAt)
}

func (r *DeviceTypeResolver) DeletedAt() *string {
	return util.FormatTime(r.M.DeletedAt.Time)
}

func (r *DeviceTypeResolver) Token() string {
	return r.M.Token
}

func (r *DeviceTypeResolver) Name() *string {
	return util.NullStr(r.M.Name)
}

func (r *DeviceTypeResolver) Description() *string {
	return util.NullStr(r.M.Description)
}

func (r *DeviceTypeResolver) ImageUrl() *string {
	return util.NullStr(r.M.ImageUrl)
}

func (r *DeviceTypeResolver) Icon() *string {
	return util.NullStr(r.M.Icon)
}

func (r *DeviceTypeResolver) BackgroundColor() *string {
	return util.NullStr(r.M.BackgroundColor)
}

func (r *DeviceTypeResolver) ForegroundColor() *string {
	return util.NullStr(r.M.ForegroundColor)
}

func (r *DeviceTypeResolver) BorderColor() *string {
	return util.NullStr(r.M.BorderColor)
}

func (r *DeviceTypeResolver) Metadata() *string {
	return util.MetadataStr(r.M.Metadata)
}

// Profile resolves the adopted DeviceProfile (ADR-045), or nil when the type has
// none. Uses the preloaded association when present, else loads by id. A lookup
// error is surfaced (not swallowed to nil) so a transient failure never
// masquerades as "no profile"; a genuinely absent id resolves to nil.
func (r *DeviceTypeResolver) Profile() (*DeviceProfileResolver, error) {
	if r.M.Profile != nil {
		return &DeviceProfileResolver{M: *r.M.Profile, S: r.S, C: r.C}, nil
	}
	if r.M.ProfileId == nil {
		return nil, nil
	}
	found, err := r.S.GetApi(r.C).DeviceProfilesById(r.C, []uint{*r.M.ProfileId})
	if err != nil {
		return nil, err
	}
	if len(found) == 0 {
		return nil, nil
	}
	return &DeviceProfileResolver{M: *found[0], S: r.S, C: r.C}, nil
}

func (r *DeviceTypeResolver) Manufacturer() *string {
	return util.NullStr(r.M.Manufacturer)
}

func (r *DeviceTypeResolver) Model() *string {
	return util.NullStr(r.M.ModelName)
}

// -----------------------------------
// Device type search results resolver
// -----------------------------------

type DeviceTypeSearchResultsResolver struct {
	M model.DeviceTypeSearchResults
	S *SchemaResolver
	C context.Context
}

func (r *DeviceTypeSearchResultsResolver) Results() []*DeviceTypeResolver {
	resolvers := make([]*DeviceTypeResolver, 0)
	for _, current := range r.M.Results {
		resolvers = append(resolvers,
			&DeviceTypeResolver{
				M: current,
				S: r.S,
				C: r.C,
			})
	}
	return resolvers
}

func (r *DeviceTypeSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{
		M: r.M.Pagination,
		S: r.S,
		C: r.C,
	}
}

// ---------------
// Device resolver
// ---------------

type DeviceResolver struct {
	M model.Device
	S *SchemaResolver
	C context.Context
}

func (r *DeviceResolver) Id() gql.ID {
	return gql.ID(fmt.Sprint(r.M.ID))
}

func (r *DeviceResolver) CreatedAt() *string {
	return util.FormatTime(r.M.CreatedAt)
}

func (r *DeviceResolver) UpdatedAt() *string {
	return util.FormatTime(r.M.UpdatedAt)
}

func (r *DeviceResolver) DeletedAt() *string {
	return util.FormatTime(r.M.DeletedAt.Time)
}

func (r *DeviceResolver) Token() string {
	return r.M.Token
}

func (r *DeviceResolver) ExternalId() *string {
	return util.NullStr(r.M.ExternalId)
}

func (r *DeviceResolver) Name() *string {
	return util.NullStr(r.M.Name)
}

func (r *DeviceResolver) Description() *string {
	return util.NullStr(r.M.Description)
}

func (r *DeviceResolver) Metadata() *string {
	return util.MetadataStr(r.M.Metadata)
}

func (r *DeviceResolver) Enabled() bool {
	return !r.M.IsDisabled()
}

func (r *DeviceResolver) DisabledAt() *string {
	if r.M.DisabledAt.Valid {
		return util.FormatTime(r.M.DisabledAt.Time)
	}
	return nil
}

// Gateway resolves the gateway the device is behind, or nil when it is behind none (or the
// caller cannot see the gateway: the store applies the caller's scope to the read).
func (r *DeviceResolver) Gateway(ctx context.Context) (*DeviceResolver, error) {
	if r.M.GatewayId == nil {
		return nil, nil
	}
	if _, err := auth.AuthorizeScoped(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}
	gateway, err := r.S.GetApi(ctx).DeviceGateway(ctx, &r.M)
	if err != nil || gateway == nil {
		return nil, err
	}
	return &DeviceResolver{M: *gateway, S: r.S, C: ctx}, nil
}

// Children pages the devices behind this device.
func (r *DeviceResolver) Children(ctx context.Context, args struct {
	Pagination PaginationInput
}) (*DeviceSearchResultsResolver, error) {
	if _, err := auth.AuthorizeScoped(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}
	found, err := r.S.GetApi(ctx).DeviceChildren(ctx, r.M.Token, rdbPagination(args.Pagination))
	if err != nil {
		return nil, err
	}
	return &DeviceSearchResultsResolver{M: *found, S: r.S, C: ctx}, nil
}

// ExpectedProvisioningProfile is the profile the device is earmarked for, or nil.
func (r *DeviceResolver) ExpectedProvisioningProfile(ctx context.Context) (*ProvisioningProfileResolver, error) {
	if r.M.ExpectedProvisioningProfileId == nil {
		return nil, nil
	}
	return r.S.provisioningProfileByID(ctx, *r.M.ExpectedProvisioningProfileId)
}

// Aliases are the names a sender other than the platform knows the device by. They are shown to
// an unscoped caller holding device:read. The alias table declares no scope rule, so a caller
// limited to part of the tenant gets NULL, not an empty list: the field is nullable for that
// reason, "none" is [] and "not shown to you" is null, and the rest of the device is unaffected
// (the same answer Gateway gives for a gateway the caller cannot see).
func (r *DeviceResolver) Aliases(ctx context.Context) (*[]*DeviceAliasResolver, error) {
	scope, err := auth.AuthorizeScoped(ctx, auth.DeviceRead)
	if err != nil {
		return nil, err
	}
	if scope.IsScoped() {
		return nil, nil
	}
	found, err := r.S.GetApi(ctx).DeviceAliases(ctx, r.M.ID)
	if err != nil {
		return nil, err
	}
	out := make([]*DeviceAliasResolver, 0, len(found))
	for _, a := range found {
		out = append(out, &DeviceAliasResolver{M: a})
	}
	return &out, nil
}

// DeviceAliasResolver serves one alias of a device.
type DeviceAliasResolver struct {
	M model.DeviceAlias
}

func (r *DeviceAliasResolver) Scheme() string { return r.M.Scheme }
func (r *DeviceAliasResolver) Value() string  { return r.M.Value }

func (r *DeviceResolver) DeviceType() *DeviceTypeResolver {
	if r.M.DeviceType != nil {
		return &DeviceTypeResolver{
			M: *r.M.DeviceType,
			S: r.S,
			C: r.C,
		}
	} else {
		ids := []string{fmt.Sprintf("%d", r.M.DeviceTypeId)}
		rez, err := r.S.DeviceTypesById(r.C, struct{ Ids []string }{Ids: ids})
		if err != nil || len(rez) == 0 {
			return nil
		}
		return rez[0]
	}
}

// ------------------------------
// Device search results resolver
// ------------------------------

type DeviceSearchResultsResolver struct {
	M model.DeviceSearchResults
	S *SchemaResolver
	C context.Context
}

func (r *DeviceSearchResultsResolver) Results() []*DeviceResolver {
	resolvers := make([]*DeviceResolver, 0)
	for _, current := range r.M.Results {
		resolvers = append(resolvers,
			&DeviceResolver{
				M: current,
				S: r.S,
				C: r.C,
			})
	}
	return resolvers
}

func (r *DeviceSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{
		M: r.M.Pagination,
		S: r.S,
		C: r.C,
	}
}
