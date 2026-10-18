// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	_ "embed"
	"github.com/devicechain-io/dc-microservice/auth"
	util "github.com/devicechain-io/dc-microservice/graphql"

	"github.com/devicechain-io/dc-device-management/model"
)

// Find metric definitions by unique id.
func (r *SchemaResolver) MetricDefinitionsById(ctx context.Context, args struct {
	Ids []string
}) ([]*MetricDefinitionResolver, error) {
	if err := auth.AuthorizeCatalogue(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	ids, err := util.AsUintIds(args.Ids)
	if err != nil {
		return nil, err
	}
	found, err := api.MetricDefinitionsById(ctx, ids)
	if err != nil {
		return nil, err
	}

	result := make([]*MetricDefinitionResolver, 0)
	for _, md := range found {
		mdr := &MetricDefinitionResolver{
			M: *md,
			S: r,
			C: ctx,
		}
		result = append(result, mdr)
	}
	return result, nil
}

// Find metric definitions by unique token.
func (r *SchemaResolver) MetricDefinitionsByToken(ctx context.Context, args struct {
	Tokens []string
}) ([]*MetricDefinitionResolver, error) {
	if err := auth.AuthorizeCatalogue(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	found, err := api.MetricDefinitionsByToken(ctx, args.Tokens)
	if err != nil {
		return nil, err
	}

	result := make([]*MetricDefinitionResolver, 0)
	for _, md := range found {
		mdr := &MetricDefinitionResolver{
			M: *md,
			S: r,
			C: ctx,
		}
		result = append(result, mdr)
	}
	return result, nil
}

// List all metric definitions that match the given criteria.
func (r *SchemaResolver) MetricDefinitions(ctx context.Context, args struct {
	Criteria model.MetricDefinitionSearchCriteria
}) (*MetricDefinitionSearchResultsResolver, error) {
	if err := auth.AuthorizeCatalogue(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	found, err := api.MetricDefinitions(ctx, args.Criteria)
	if err != nil {
		return nil, err
	}

	// Return as resolver.
	return &MetricDefinitionSearchResultsResolver{
		M: *found,
		S: r,
		C: ctx,
	}, nil
}

// TestMetricFormula compiles a calculated-metric formula against a profile's draft
// measured metrics and evaluates it against a hand-typed sample, through the same code the
// resolver will use. Nothing is stored. An authoring tool, so it is closed to a scoped
// member (Authorize, not AuthorizeCatalogue), like every other authoring door.
func (r *SchemaResolver) TestMetricFormula(ctx context.Context, args struct {
	ProfileToken string
	Expression   string
	DataType     string
	MetricKey    *string
	MinValue     *float64
	MaxValue     *float64
	Sample       []model.MetricValueInput
	Constants    *[]model.MetricValueInput
}) (*MetricFormulaTestResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}
	res, err := r.GetApi(ctx).TestMetricFormula(ctx, &model.MetricFormulaTestRequest{
		ProfileToken: args.ProfileToken, Expression: args.Expression, DataType: args.DataType,
		MetricKey: args.MetricKey, MinValue: args.MinValue, MaxValue: args.MaxValue, Sample: args.Sample,
		Constants: derefList(args.Constants),
	})
	if err != nil {
		return nil, err
	}
	return &MetricFormulaTestResolver{M: *res}, nil
}

// derefList is the list an optional list argument carries, or none.
func derefList[T any](in *[]T) []T {
	if in == nil {
		return nil
	}
	return *in
}
