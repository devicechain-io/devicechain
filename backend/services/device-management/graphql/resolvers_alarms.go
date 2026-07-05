// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"

	"github.com/devicechain-io/dc-device-management/model"
	util "github.com/devicechain-io/dc-microservice/graphql"
	gql "github.com/graph-gophers/graphql-go"
)

// nullInt32 adapts a nullable bigint column to an optional GraphQL Int (int32).
func nullInt32(v sql.NullInt64) *int32 {
	if !v.Valid {
		return nil
	}
	n := int32(v.Int64)
	return &n
}

// -------------------------
// Alarm definition resolver
// -------------------------

type AlarmDefinitionResolver struct {
	M model.AlarmDefinition
	S *SchemaResolver
	C context.Context
}

func (r *AlarmDefinitionResolver) Id() gql.ID {
	return gql.ID(fmt.Sprint(r.M.ID))
}

func (r *AlarmDefinitionResolver) CreatedAt() *string {
	return util.FormatTime(r.M.CreatedAt)
}

func (r *AlarmDefinitionResolver) UpdatedAt() *string {
	return util.FormatTime(r.M.UpdatedAt)
}

func (r *AlarmDefinitionResolver) DeletedAt() *string {
	return util.FormatTime(r.M.DeletedAt.Time)
}

func (r *AlarmDefinitionResolver) Token() string { return r.M.Token }

func (r *AlarmDefinitionResolver) Name() *string { return util.NullStr(r.M.Name) }

func (r *AlarmDefinitionResolver) Description() *string { return util.NullStr(r.M.Description) }

func (r *AlarmDefinitionResolver) Metadata() *string { return util.MetadataStr(r.M.Metadata) }

func (r *AlarmDefinitionResolver) AlarmKey() string { return r.M.AlarmKey }

func (r *AlarmDefinitionResolver) MetricKey() string { return r.M.MetricKey }

func (r *AlarmDefinitionResolver) ConditionType() string { return r.M.ConditionType }

func (r *AlarmDefinitionResolver) Operator() string { return r.M.Operator }

func (r *AlarmDefinitionResolver) Severity() string { return r.M.Severity }

func (r *AlarmDefinitionResolver) Enabled() bool { return r.M.Enabled }

func (r *AlarmDefinitionResolver) Threshold() *float64 {
	if r.M.Threshold.Valid {
		v := r.M.Threshold.Float64
		return &v
	}
	return nil
}

func (r *AlarmDefinitionResolver) ThresholdAttr() *string { return util.NullStr(r.M.ThresholdAttr) }

func (r *AlarmDefinitionResolver) DurationSeconds() *int32 { return nullInt32(r.M.DurationSeconds) }

func (r *AlarmDefinitionResolver) RepeatCount() *int32 { return nullInt32(r.M.RepeatCount) }

func (r *AlarmDefinitionResolver) RepeatWindowSeconds() *int32 {
	return nullInt32(r.M.RepeatWindowSeconds)
}

func (r *AlarmDefinitionResolver) DeviceProfile() *DeviceProfileResolver {
	if r.M.DeviceProfile != nil {
		return &DeviceProfileResolver{M: *r.M.DeviceProfile, S: r.S, C: r.C}
	}
	ids := []string{fmt.Sprintf("%d", r.M.DeviceProfileId)}
	rez, err := r.S.DeviceProfilesById(r.C, struct{ Ids []string }{Ids: ids})
	if err != nil || len(rez) == 0 {
		return nil
	}
	return rez[0]
}

// --------------------------------------
// Alarm definition search results resolver
// --------------------------------------

type AlarmDefinitionSearchResultsResolver struct {
	M model.AlarmDefinitionSearchResults
	S *SchemaResolver
	C context.Context
}

func (r *AlarmDefinitionSearchResultsResolver) Results() []*AlarmDefinitionResolver {
	resolvers := make([]*AlarmDefinitionResolver, 0)
	for _, current := range r.M.Results {
		resolvers = append(resolvers,
			&AlarmDefinitionResolver{M: current, S: r.S, C: r.C})
	}
	return resolvers
}

func (r *AlarmDefinitionSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{M: r.M.Pagination, S: r.S, C: r.C}
}
