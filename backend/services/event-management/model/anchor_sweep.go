// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"

	"github.com/devicechain-io/dc-microservice/entity"
)

// AnchorRef is an entity reference held by event_anchors: a type + stable per-tenant
// token (ADR-044). The reconciliation sweep (ADR-044 decision 3) resolves these
// against device-management to find anchors whose referenced entity was deleted.
type AnchorRef struct {
	Type  string
	Token string
}

// DistinctAnchorTenants returns every tenant that currently has event_anchors. It is
// a cross-tenant read, so the caller MUST pass a system context (the sweep runs
// outside any one tenant); a normal tenant context would fail closed.
//
// 🔴 THIS ONE IS NOT PAGED, AND THE ASYMMETRY IS DELIBERATE. Its result set is one row
// per TENANT, which an instance's own control plane already bounds — a deployment with
// more tenants than fit in memory here has run out of somewhere to put them long
// before. The two reads below are bounded by how much a tenant has ANCHORED, which
// nothing bounds, and that is the difference that decides which needs a cursor.
func (api *Api) DistinctAnchorTenants(ctx context.Context) ([]string, error) {
	tenants := make([]string, 0)
	err := api.RDB.DB(ctx).Model(&EventAnchor{}).
		Distinct().Order("tenant_id").Pluck("tenant_id", &tenants).Error
	return tenants, err
}

// anchorRefPageSize is how many distinct refs one page of either read below returns.
//
// It is also the fan-out of a single existence query, because a page IS the unit that
// gets resolved: the sweep asks device-management about exactly the refs it just read
// and then forgets them. That replaced a chunking loop that sliced an already-loaded
// slice — which bounded the REQUEST while leaving the read that produced it unbounded,
// so the value was right and the thing it was protecting was not.
//
// The ceiling it has to respect is the smaller of svcclient's response cap and the
// target's SQL IN-list limit. A page too large errors, and the sweep fails safe by
// skipping the tenant — so the backstop would stop working for exactly the tenants
// large enough to need it.
const anchorRefPageSize = 500

// DistinctAnchorTargetsAfter returns one page of the current tenant's distinct anchor
// TARGETS — the (anchor_type, anchor_token) pairs — ordered by that pair, starting
// strictly after the cursor. An empty cursor starts at the beginning; a short page
// means the end. Tenant-scoped via the context.
//
// The keyset is the ordered pair itself rather than an offset. DISTINCT makes it
// unique, so there are no ties to break and no row can be skipped or repeated by
// concurrent writes — which OFFSET, over a table the ingest path is appending to
// throughout the sweep, cannot promise.
func (api *Api) DistinctAnchorTargetsAfter(ctx context.Context, afterType, afterToken string) ([]AnchorRef, error) {
	refs := make([]AnchorRef, 0, anchorRefPageSize)
	q := api.RDB.DB(ctx).Model(&EventAnchor{}).
		Select("DISTINCT event_anchors.anchor_type AS type, event_anchors.anchor_token AS token").
		Order("event_anchors.anchor_type, event_anchors.anchor_token").
		Limit(anchorRefPageSize)
	if afterType != "" || afterToken != "" {
		// A row-value comparison, not `type > ? OR (type = ? AND token > ?)`. Both are
		// correct; this one is one expression, so there is no second clause to get
		// wrong when someone adds a column to the key.
		q = q.Where("(event_anchors.anchor_type, event_anchors.anchor_token) > (?, ?)", afterType, afterToken)
	}
	if err := q.Scan(&refs).Error; err != nil {
		return nil, err
	}
	return refs, nil
}

// DistinctAnchorDeviceTokensAfter returns one page of the current tenant's distinct
// anchor SOURCE device tokens, ordered ascending, starting strictly after the cursor.
// An empty cursor starts at the beginning; a short page means the end. Tenant-scoped
// via the context.
func (api *Api) DistinctAnchorDeviceTokensAfter(ctx context.Context, after string) ([]AnchorRef, error) {
	tokens := make([]string, 0, anchorRefPageSize)
	q := api.RDB.DB(ctx).Model(&EventAnchor{}).
		Distinct().
		Order("event_anchors.device_token").
		Limit(anchorRefPageSize)
	if after != "" {
		q = q.Where("event_anchors.device_token > ?", after)
	}
	if err := q.Pluck("event_anchors.device_token", &tokens).Error; err != nil {
		return nil, err
	}
	refs := make([]AnchorRef, 0, len(tokens))
	for _, token := range tokens {
		refs = append(refs, AnchorRef{Type: string(entity.TypeDevice), Token: token})
	}
	return refs, nil
}
