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

func (r *MetricDefinitionResolver) DeviceProfile() *DeviceProfileResolver {
	if r.M.DeviceProfile != nil {
		return &DeviceProfileResolver{
			M: *r.M.DeviceProfile,
			S: r.S,
			C: r.C,
		}
	} else {
		ids := []string{fmt.Sprintf("%d", r.M.DeviceProfileId)}
		rez, err := r.S.DeviceProfilesById(r.C, struct{ Ids []string }{Ids: ids})
		if err != nil || len(rez) == 0 {
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

// Expression is a calculated metric's formula, or null for a measured metric.
func (r *MetricDefinitionResolver) Expression() *string {
	return util.NullStr(r.M.Expression)
}

// Calculated reports whether the metric is computed from its formula.
func (r *MetricDefinitionResolver) Calculated() bool {
	return r.M.Calculated()
}

// MetricFormulaTestResolver resolves the result of testMetricFormula.
type MetricFormulaTestResolver struct {
	M model.MetricFormulaTestResult
}

func (r *MetricFormulaTestResolver) CompileError() *string  { return r.M.CompileError }
func (r *MetricFormulaTestResolver) Value() *string         { return r.M.Value }
func (r *MetricFormulaTestResolver) SkippedReason() *string { return r.M.SkippedReason }
func (r *MetricFormulaTestResolver) Inputs() []string       { return r.M.Inputs }
func (r *MetricFormulaTestResolver) Constants() []string    { return r.M.Constants }
func (r *MetricFormulaTestResolver) Warnings() []string     { return r.M.Warnings }
func (r *MetricFormulaTestResolver) EstimatedCost() *int32 {
	if r.M.EstimatedCost == nil {
		return nil
	}
	v := int32(*r.M.EstimatedCost)
	return &v
}
