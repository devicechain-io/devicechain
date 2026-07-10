// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-device-management/model"
	util "github.com/devicechain-io/dc-microservice/graphql"
	gql "github.com/graph-gophers/graphql-go"
)

// -----------------------
// Device profile resolver
// -----------------------

type DeviceProfileResolver struct {
	M model.DeviceProfile
	S *SchemaResolver
	C context.Context
}

func (r *DeviceProfileResolver) Id() gql.ID {
	return gql.ID(fmt.Sprint(r.M.ID))
}

func (r *DeviceProfileResolver) CreatedAt() *string {
	return util.FormatTime(r.M.CreatedAt)
}

func (r *DeviceProfileResolver) UpdatedAt() *string {
	return util.FormatTime(r.M.UpdatedAt)
}

func (r *DeviceProfileResolver) DeletedAt() *string {
	return util.FormatTime(r.M.DeletedAt.Time)
}

func (r *DeviceProfileResolver) Token() string {
	return r.M.Token
}

func (r *DeviceProfileResolver) Name() *string {
	return util.NullStr(r.M.Name)
}

func (r *DeviceProfileResolver) Description() *string {
	return util.NullStr(r.M.Description)
}

func (r *DeviceProfileResolver) Category() *string {
	return util.NullStr(r.M.Category)
}

func (r *DeviceProfileResolver) Metadata() *string {
	return util.MetadataStr(r.M.Metadata)
}

// ActiveVersion is the published version a device resolves through this profile
// (ADR-045 versioning), or null when the profile has never been published.
func (r *DeviceProfileResolver) ActiveVersion() *int32 {
	if !r.M.ActiveVersion.Valid {
		return nil
	}
	v := r.M.ActiveVersion.Int32
	return &v
}

// DeviceTypeCount is how many device types currently adopt this profile (ADR-045);
// the authoring UI shows it so a shared profile's blast radius is visible.
func (r *DeviceProfileResolver) DeviceTypeCount() (int32, error) {
	return r.S.GetApi(r.C).DeviceTypeCountForProfile(r.C, r.M.ID)
}

// The typed capability definitions the profile owns (ADR-045 slice b): metrics
// (ADR-016), commands (ADR-043), alarm rules (ADR-041).

func (r *DeviceProfileResolver) MetricDefinitions() ([]*MetricDefinitionResolver, error) {
	found, err := r.S.GetApi(r.C).MetricDefinitionsByDeviceProfile(r.C, r.M.ID)
	if err != nil {
		return nil, err
	}
	result := make([]*MetricDefinitionResolver, 0)
	for _, md := range found {
		result = append(result, &MetricDefinitionResolver{M: *md, S: r.S, C: r.C})
	}
	return result, nil
}

func (r *DeviceProfileResolver) CommandDefinitions() ([]*CommandDefinitionResolver, error) {
	found, err := r.S.GetApi(r.C).CommandDefinitionsByDeviceProfile(r.C, r.M.ID)
	if err != nil {
		return nil, err
	}
	result := make([]*CommandDefinitionResolver, 0)
	for _, cd := range found {
		result = append(result, &CommandDefinitionResolver{M: *cd, S: r.S, C: r.C})
	}
	return result, nil
}

func (r *DeviceProfileResolver) AlarmDefinitions() ([]*AlarmDefinitionResolver, error) {
	found, err := r.S.GetApi(r.C).AlarmDefinitionsByDeviceProfile(r.C, r.M.ID)
	if err != nil {
		return nil, err
	}
	result := make([]*AlarmDefinitionResolver, 0)
	for _, ad := range found {
		result = append(result, &AlarmDefinitionResolver{M: *ad, S: r.S, C: r.C})
	}
	return result, nil
}

func (r *DeviceProfileResolver) DetectionRules() ([]*DetectionRuleResolver, error) {
	found, err := r.S.GetApi(r.C).DetectionRulesByDeviceProfile(r.C, r.M.ID)
	if err != nil {
		return nil, err
	}
	result := make([]*DetectionRuleResolver, 0)
	for _, dr := range found {
		result = append(result, &DetectionRuleResolver{M: *dr, S: r.S, C: r.C})
	}
	return result, nil
}

// --------------------------------------
// Device profile search results resolver
// --------------------------------------

type DeviceProfileSearchResultsResolver struct {
	M model.DeviceProfileSearchResults
	S *SchemaResolver
	C context.Context
}

func (r *DeviceProfileSearchResultsResolver) Results() []*DeviceProfileResolver {
	resolvers := make([]*DeviceProfileResolver, 0)
	for _, current := range r.M.Results {
		resolvers = append(resolvers, &DeviceProfileResolver{
			M: current,
			S: r.S,
			C: r.C,
		})
	}
	return resolvers
}

func (r *DeviceProfileSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{
		M: r.M.Pagination,
		S: r.S,
		C: r.C,
	}
}
