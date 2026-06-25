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

// --------------------------
// Metric definition resolver
// --------------------------

type MetricDefinitionResolver struct {
	M model.MetricDefinition
	S *SchemaResolver
	C context.Context
}

func (r *MetricDefinitionResolver) Id() gql.ID {
	return gql.ID(fmt.Sprint(r.M.ID))
}

func (r *MetricDefinitionResolver) CreatedAt() *string {
	return util.FormatTime(r.M.CreatedAt)
}

func (r *MetricDefinitionResolver) UpdatedAt() *string {
	return util.FormatTime(r.M.UpdatedAt)
}

func (r *MetricDefinitionResolver) DeletedAt() *string {
	return util.FormatTime(r.M.DeletedAt.Time)
}

func (r *MetricDefinitionResolver) Token() string {
	return r.M.Token
}

func (r *MetricDefinitionResolver) Name() *string {
	return util.NullStr(r.M.Name)
}

func (r *MetricDefinitionResolver) Description() *string {
	return util.NullStr(r.M.Description)
}

func (r *MetricDefinitionResolver) Metadata() *string {
	return util.MetadataStr(r.M.Metadata)
}

func (r *MetricDefinitionResolver) MetricKey() string {
	return r.M.MetricKey
}

func (r *MetricDefinitionResolver) DataType() string {
	return r.M.DataType
}

func (r *MetricDefinitionResolver) Unit() *string {
	return util.NullStr(r.M.Unit)
}

func (r *MetricDefinitionResolver) MinValue() *float64 {
	if r.M.MinValue.Valid {
		v := r.M.MinValue.Float64
		return &v
	}
	return nil
}

func (r *MetricDefinitionResolver) MaxValue() *float64 {
	if r.M.MaxValue.Valid {
		v := r.M.MaxValue.Float64
		return &v
	}
	return nil
}

func (r *MetricDefinitionResolver) Enum() *string {
	return util.MetadataStr(r.M.Enum)
}

func (r *MetricDefinitionResolver) Descriptor() *string {
	return util.NullStr(r.M.Descriptor)
}

func (r *MetricDefinitionResolver) DeviceType() *DeviceTypeResolver {
	if r.M.DeviceType != nil {
		return &DeviceTypeResolver{
			M: *r.M.DeviceType,
			S: r.S,
			C: r.C,
		}
	} else {
		ids := []string{fmt.Sprintf("%d", r.M.DeviceTypeId)}
		rez, err := r.S.DeviceTypesById(r.C, struct{ Ids []string }{Ids: ids})
		if err != nil {
			return nil
		}
		return rez[0]
	}
}

// -----------------------------------------
// Metric definition search results resolver
// -----------------------------------------

type MetricDefinitionSearchResultsResolver struct {
	M model.MetricDefinitionSearchResults
	S *SchemaResolver
	C context.Context
}

func (r *MetricDefinitionSearchResultsResolver) Results() []*MetricDefinitionResolver {
	resolvers := make([]*MetricDefinitionResolver, 0)
	for _, current := range r.M.Results {
		resolvers = append(resolvers,
			&MetricDefinitionResolver{
				M: current,
				S: r.S,
				C: r.C,
			})
	}
	return resolvers
}

func (r *MetricDefinitionSearchResultsResolver) Pagination() *SearchResultsPaginationResolver {
	return &SearchResultsPaginationResolver{
		M: r.M.Pagination,
		S: r.S,
		C: r.C,
	}
}
