// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
)

// ImportFacetValues uses the same authority as setEntityAttribute and device writes.
func (r *SchemaResolver) ImportFacetValues(ctx context.Context, args struct {
	Rows   []*model.FacetValueImportRow
	DryRun bool
}) ([]*FacetValueImportRowResultResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}
	rows, err := r.GetApi(ctx).ImportFacetValues(ctx, args.Rows, args.DryRun)
	if err != nil {
		return nil, err
	}
	out := make([]*FacetValueImportRowResultResolver, 0, len(rows))
	for _, row := range rows {
		out = append(out, &FacetValueImportRowResultResolver{M: row})
	}
	return out, nil
}

// FacetValueImportRowResultResolver serves a row report without echoing its value.
type FacetValueImportRowResultResolver struct {
	M *model.FacetValueImportRowResult
}

func (r *FacetValueImportRowResultResolver) Row() int32         { return r.M.Row }
func (r *FacetValueImportRowResultResolver) EntityType() string { return r.M.EntityType }
func (r *FacetValueImportRowResultResolver) Token() string      { return r.M.Token }
func (r *FacetValueImportRowResultResolver) Key() string        { return r.M.Key }
func (r *FacetValueImportRowResultResolver) Outcome() string    { return r.M.Outcome }
func (r *FacetValueImportRowResultResolver) Code() *string      { return r.M.Code }
func (r *FacetValueImportRowResultResolver) Message() *string   { return r.M.Message }
