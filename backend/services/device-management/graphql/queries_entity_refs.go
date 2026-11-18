// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-device-management/model"
	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/limit"
	"github.com/devicechain-io/dc-microservice/rdb"
)

// EntityRefResolver resolves a uniform (type, token) entity reference (ADR-013/044).
type EntityRefResolver struct {
	T   string
	Tok string
}

func (r *EntityRefResolver) Type() string {
	return r.T
}

func (r *EntityRefResolver) Token() string {
	return r.Tok
}

// ExistingEntityRefs returns the subset of the given (type, token) references that
// resolve to an existing entity in the caller's tenant (ADR-044 decision 3). The
// event-management reconciliation sweep uses this to find orphaned event_anchors
// whose referenced entity was deleted — addressing the ref by its stable per-tenant
// token, not a device-management row id.
func (r *SchemaResolver) ExistingEntityRefs(ctx context.Context, args struct {
	Refs []struct {
		Type  string
		Token string
	}
}) ([]*EntityRefResolver, error) {
	if err := auth.Authorize(ctx, auth.DeviceRead); err != nil {
		return nil, err
	}

	// Above this aggregate bound at least one type is unknown or exceeds the lookup limit, so
	// the request cannot succeed; refuse it before grouping it.
	if len(args.Refs) > model.EntityTypeCount()*rdb.MaxLookupKeys {
		return nil, limit.Exceeded("lookup keys", len(args.Refs), rdb.MaxLookupKeys)
	}
	// Group the requested tokens by entity type so each type is checked in one query.
	byType := make(map[string][]string)
	order := make([]string, 0)
	for _, ref := range args.Refs {
		if _, seen := byType[ref.Type]; !seen {
			order = append(order, ref.Type)
		}
		byType[ref.Type] = append(byType[ref.Type], ref.Token)
	}

	// Refuse what the per-type lookups below would refuse, before ANY type is queried: an
	// unknown type, or more tokens of one type than a lookup takes. Walking the types in the
	// same order as the lookups keeps the refusal (and its count) the one a caller got when
	// it surfaced from the lookup itself.
	for _, etype := range order {
		if !model.IsEntityType(etype) {
			return nil, fmt.Errorf("unknown entity type %q", etype)
		}
		if n := len(byType[etype]); n > rdb.MaxLookupKeys {
			return nil, limit.Exceeded("lookup keys", n, rdb.MaxLookupKeys)
		}
	}

	api := r.GetApi(ctx)
	result := make([]*EntityRefResolver, 0)
	for _, etype := range order {
		existing, err := api.ExistingEntityTokens(ctx, etype, byType[etype])
		if err != nil {
			return nil, err
		}
		for _, token := range existing {
			result = append(result, &EntityRefResolver{T: etype, Tok: token})
		}
	}
	return result, nil
}
