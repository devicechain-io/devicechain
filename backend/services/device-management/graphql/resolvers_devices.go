// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/devicechain-io/dc-device-management/model"
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

func (r *DeviceResolver) Name() *string {
	return util.NullStr(r.M.Name)
}

func (r *DeviceResolver) Description() *string {
	return util.NullStr(r.M.Description)
}

func (r *DeviceResolver) Metadata() *string {
	return util.MetadataStr(r.M.Metadata)
}

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

// ---------------------
// Device group resolver
// ---------------------

type DeviceGroupResolver struct {
	M model.DeviceGroup
	S *SchemaResolver
	C context.Context
}

func (r *DeviceGroupResolver) Id() gql.ID {
	return gql.ID(fmt.Sprint(r.M.ID))
}

func (r *DeviceGroupResolver) CreatedAt() *string {
	return util.FormatTime(r.M.CreatedAt)
}

func (r *DeviceGroupResolver) UpdatedAt() *string {
	return util.FormatTime(r.M.UpdatedAt)
}

func (r *DeviceGroupResolver) DeletedAt() *string {
	return util.FormatTime(r.M.DeletedAt.Time)
}

func (r *DeviceGroupResolver) Token() string {
	return r.M.Token
}

func (r *DeviceGroupResolver) Name() *string {
	return util.NullStr(r.M.Name)
}

func (r *DeviceGroupResolver) Description() *string {
	return util.NullStr(r.M.Description)
}

func (r *DeviceGroupResolver) ImageUrl() *string {
	return util.NullStr(r.M.ImageUrl)
}

func (r *DeviceGroupResolver) Icon() *string {
	return util.NullStr(r.M.Icon)
}

func (r *DeviceGroupResolver) BackgroundColor() *string {
	return util.NullStr(r.M.BackgroundColor)
}

func (r *DeviceGroupResolver) ForegroundColor() *string {
	return util.NullStr(r.M.ForegroundColor)
}

func (r *DeviceGroupResolver) BorderColor() *string {
	return util.NullStr(r.M.BorderColor)
}

func (r *DeviceGroupResolver) Metadata() *string {
	return util.MetadataStr(r.M.Metadata)
}

// ------------------------------------
// Device group search results resolver
// ------------------------------------

type DeviceGroupSearchResultsResolver struct {
	M model.DeviceGroupSearchResults
	S *SchemaResolver
	C context.Context
}

func (r *DeviceGroupSearchResultsResolver) Results() []*DeviceGroupResolver {
	resolvers := make([]*DeviceGroupResolver, 0)
	for _, current := range r.M.Results {
		resolvers = append(resolvers,
			&DeviceGroupResolver{
				M: current,
				S: r.S,
				C: r.C,
			})
	}
	return resolvers
}

func (r *DeviceGroupSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{
		M: r.M.Pagination,
		S: r.S,
		C: r.C,
	}
}

// Metric definitions declared on this device type — relocating to its DeviceProfile
// in ADR-045 slice b (ADR-016).
func (r *DeviceTypeResolver) MetricDefinitions() ([]*MetricDefinitionResolver, error) {
	api := r.S.GetApi(r.C)
	found, err := api.MetricDefinitionsByDeviceType(r.C, r.M.ID)
	if err != nil {
		return nil, err
	}

	result := make([]*MetricDefinitionResolver, 0)
	for _, md := range found {
		result = append(result, &MetricDefinitionResolver{
			M: *md,
			S: r.S,
			C: r.C,
		})
	}
	return result, nil
}

// Command definitions declared on this device type — relocating to its DeviceProfile
// in ADR-045 slice b (ADR-043).
func (r *DeviceTypeResolver) CommandDefinitions() ([]*CommandDefinitionResolver, error) {
	api := r.S.GetApi(r.C)
	found, err := api.CommandDefinitionsByDeviceType(r.C, r.M.ID)
	if err != nil {
		return nil, err
	}

	result := make([]*CommandDefinitionResolver, 0)
	for _, cd := range found {
		result = append(result, &CommandDefinitionResolver{
			M: *cd,
			S: r.S,
			C: r.C,
		})
	}
	return result, nil
}

// Alarm definitions declared on this device type — relocating to its DeviceProfile
// in ADR-045 slice b (ADR-041).
func (r *DeviceTypeResolver) AlarmDefinitions() ([]*AlarmDefinitionResolver, error) {
	api := r.S.GetApi(r.C)
	found, err := api.AlarmDefinitionsByDeviceType(r.C, r.M.ID)
	if err != nil {
		return nil, err
	}

	result := make([]*AlarmDefinitionResolver, 0)
	for _, ad := range found {
		result = append(result, &AlarmDefinitionResolver{
			M: *ad,
			S: r.S,
			C: r.C,
		})
	}
	return result, nil
}
