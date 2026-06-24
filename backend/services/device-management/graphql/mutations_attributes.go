// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-device-management/model"
)

// Set (upsert) an entity attribute.
func (r *SchemaResolver) SetEntityAttribute(ctx context.Context, args struct {
	Request *model.EntityAttributeSetRequest
}) (*EntityAttributeResolver, error) {
	api := r.GetApi(ctx)
	set, err := api.SetEntityAttribute(ctx, args.Request)
	if err != nil {
		return nil, err
	}
	return &EntityAttributeResolver{M: *set, S: r, C: ctx}, nil
}

// Delete an entity attribute by its natural key (entity, scope, key).
func (r *SchemaResolver) DeleteEntityAttribute(ctx context.Context, args struct {
	EntityType string
	Entity     string
	Scope      string
	AttrKey    string
}) (bool, error) {
	api := r.GetApi(ctx)
	return api.DeleteEntityAttribute(ctx, args.EntityType, args.Entity, args.Scope, args.AttrKey)
}
