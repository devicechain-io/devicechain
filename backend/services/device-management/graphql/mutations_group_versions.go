// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-microservice/auth"
)

// PublishEntityGroup freezes a dynamic group's current draft selector into a new
// immutable version and makes it the active version a rule-scoped resolve reads
// (ADR-062 S1). Gated on DeviceWrite, consistent with group create/update. Unlike a
// profile publish it needs no cache eviction — a group version is not resolved on
// the ingest path (the membership read-model that reads it lands in S2/S3).
func (r *SchemaResolver) PublishEntityGroup(ctx context.Context, args struct {
	Token       string
	Label       *string
	Description *string
}) (*EntityGroupVersionResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}
	api := r.GetApi(ctx)
	version, err := api.PublishEntityGroup(ctx, args.Token, args.Label, args.Description, publisher(ctx))
	if err != nil {
		return nil, err
	}
	return &EntityGroupVersionResolver{M: *version, S: r, C: ctx}, nil
}

// RollbackEntityGroup re-points a dynamic group's active version at an existing
// version — a non-destructive rollback; the draft is untouched (ADR-062 S1).
func (r *SchemaResolver) RollbackEntityGroup(ctx context.Context, args struct {
	Token   string
	Version int32
}) (*EntityGroupResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceWrite); err != nil {
		return nil, err
	}
	api := r.GetApi(ctx)
	updated, err := api.RollbackEntityGroup(ctx, args.Token, args.Version)
	if err != nil {
		return nil, err
	}
	return &EntityGroupResolver{M: *updated, S: r, C: ctx}, nil
}
