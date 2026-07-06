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
func (api *Api) DistinctAnchorTenants(ctx context.Context) ([]string, error) {
	tenants := make([]string, 0)
	err := api.RDB.DB(ctx).Model(&EventAnchor{}).
		Distinct().Order("tenant_id").Pluck("tenant_id", &tenants).Error
	return tenants, err
}

// DistinctAnchorRefs returns the distinct entity references in the current tenant's
// anchors: each source device (a "device" ref) and each (anchor_type, anchor_token)
// target, all addressed by stable per-tenant token (ADR-044). Tenant-scoped via the
// context.
func (api *Api) DistinctAnchorRefs(ctx context.Context) ([]AnchorRef, error) {
	refs := make([]AnchorRef, 0)

	targets := make([]AnchorRef, 0)
	if err := api.RDB.DB(ctx).Model(&EventAnchor{}).
		Select("DISTINCT anchor_type AS type, anchor_token AS token").Scan(&targets).Error; err != nil {
		return nil, err
	}
	refs = append(refs, targets...)

	deviceTokens := make([]string, 0)
	if err := api.RDB.DB(ctx).Model(&EventAnchor{}).
		Distinct().Pluck("device_token", &deviceTokens).Error; err != nil {
		return nil, err
	}
	for _, token := range deviceTokens {
		refs = append(refs, AnchorRef{Type: string(entity.TypeDevice), Token: token})
	}
	return refs, nil
}
