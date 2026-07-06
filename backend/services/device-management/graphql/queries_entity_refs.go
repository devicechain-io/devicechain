// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"fmt"
	"strconv"

	"github.com/devicechain-io/dc-microservice/auth"
	gql "github.com/graph-gophers/graphql-go"
)

// EntityRefResolver resolves a uniform (type, id) entity reference (ADR-013).
type EntityRefResolver struct {
	T string
	I uint
}

func (r *EntityRefResolver) Type() string {
	return r.T
}

func (r *EntityRefResolver) Id() gql.ID {
	return gql.ID(fmt.Sprint(r.I))
}

// ExistingEntityRefs returns the subset of the given (type, id) references that
// resolve to an existing entity in the caller's tenant (ADR-044 decision 3). The
// event-management reconciliation sweep uses this to find orphaned event_anchors
// whose referenced entity was deleted.
func (r *SchemaResolver) ExistingEntityRefs(ctx context.Context, args struct {
	Refs []struct {
		Type string
		Id   gql.ID
	}
}) ([]*EntityRefResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	// Group the requested ids by entity type so each type is checked in one query.
	byType := make(map[string][]uint)
	order := make([]string, 0)
	for _, ref := range args.Refs {
		id, err := strconv.ParseUint(string(ref.Id), 10, 64)
		if err != nil {
			return nil, err
		}
		if _, seen := byType[ref.Type]; !seen {
			order = append(order, ref.Type)
		}
		byType[ref.Type] = append(byType[ref.Type], uint(id))
	}

	api := r.GetApi(ctx)
	result := make([]*EntityRefResolver, 0)
	for _, etype := range order {
		existing, err := api.ExistingEntityIds(ctx, etype, byType[etype])
		if err != nil {
			return nil, err
		}
		for _, id := range existing {
			result = append(result, &EntityRefResolver{T: etype, I: id})
		}
	}
	return result, nil
}
