// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"github.com/devicechain-io/dc-microservice/auth"

	"github.com/devicechain-io/dc-device-management/model"
)

// Create a new area type.
func (r *SchemaResolver) CreateAreaType(ctx context.Context, args struct {
	Request *model.AreaTypeCreateRequest
}) (*AreaTypeResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	created, err := api.CreateAreaType(ctx, args.Request)
	if err != nil {
		return nil, err
	}

	dt := &AreaTypeResolver{
		M: *created,
		S: r,
		C: ctx,
	}
	return dt, nil
}

// Update an existing area type.
// The request is a PARTIAL update: a field the caller omitted is left alone, an
// explicit null clears it, a value sets it. It is declared non-null in the schema and
// is therefore a VALUE here, not a pointer — graphql-go refuses a pointer field for a
// required argument.
//
// 🔴 Non-null refuses a MISSING request (`request: null`), not an EMPTY one. `{}` is a
// perfectly good non-null input object, and it is accepted as a no-op — which is the
// correct reading of "change nothing", and what the harness's
// EmptyRequestChangesNothing asserts. An earlier version of this comment claimed an
// update naming no fields was a caller error; nothing enforced that, and nothing
// should.
func (r *SchemaResolver) UpdateAreaType(ctx context.Context, args struct {
	Token   string
	Request model.AreaTypeUpdateRequest
}) (*AreaTypeResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	updated, err := api.UpdateAreaType(ctx, args.Token, &args.Request)
	if err != nil {
		return nil, err
	}

	dt := &AreaTypeResolver{
		M: *updated,
		S: r,
		C: ctx,
	}
	return dt, nil
}

// Create a new area.
func (r *SchemaResolver) CreateArea(ctx context.Context, args struct {
	Request *model.AreaCreateRequest
}) (*AreaResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	created, err := api.CreateArea(ctx, args.Request)
	if err != nil {
		return nil, err
	}

	dt := &AreaResolver{
		M: *created,
		S: r,
		C: ctx,
	}
	return dt, nil
}

// Update an existing area.
// The request is a PARTIAL update: a field the caller omitted is left alone, an
// explicit null clears it, a value sets it. It is declared non-null in the schema and
// is therefore a VALUE here, not a pointer — graphql-go refuses a pointer field for a
// required argument.
//
// 🔴 Non-null refuses a MISSING request (`request: null`), not an EMPTY one. `{}` is a
// perfectly good non-null input object, and it is accepted as a no-op — which is the
// correct reading of "change nothing", and what the harness's
// EmptyRequestChangesNothing asserts. An earlier version of this comment claimed an
// update naming no fields was a caller error; nothing enforced that, and nothing
// should.
func (r *SchemaResolver) UpdateArea(ctx context.Context, args struct {
	Token   string
	Request model.AreaUpdateRequest
}) (*AreaResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}

	api := r.GetApi(ctx)
	updated, err := api.UpdateArea(ctx, args.Token, &args.Request)
	if err != nil {
		return nil, err
	}

	dt := &AreaResolver{
		M: *updated,
		S: r,
		C: ctx,
	}
	return dt, nil
}
