// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-user-management/iam"
)

// AdminTenantAgentActuationResolver resolves AdminTenantAgentActuation: the operator's view of
// agent actions in one tenant, and the per-tenant override.
type AdminTenantAgentActuationResolver struct {
	tenant   string
	head     *iam.AgentPolicy
	instance bool
}

func (r *AdminTenantAgentActuationResolver) Tenant() string        { return r.tenant }
func (r *AdminTenantAgentActuationResolver) InstanceEnabled() bool { return r.instance }
func (r *AdminTenantAgentActuationResolver) Suspended() bool {
	return r.head != nil && r.head.OperatorSuspended
}
func (r *AdminTenantAgentActuationResolver) SuspendedAt() *string {
	if r.head == nil {
		return nil
	}
	return rfc3339(r.head.SuspendedAt)
}
func (r *AdminTenantAgentActuationResolver) SuspendedBy() *string {
	if r.head == nil {
		return nil
	}
	return r.head.SuspendedBy
}
func (r *AdminTenantAgentActuationResolver) PublishedVersion() int32 {
	if r.head == nil {
		return 0
	}
	return int32(r.head.PublishedVersion)
}

// TenantAgentActuation reads agent actions in one tenant (requires tenant:read).
func (r *AdminResolver) TenantAgentActuation(ctx context.Context, args struct{ Tenant string }) (*AdminTenantAgentActuationResolver, error) {
	if err := auth.Authorize(ctx, auth.TenantRead); err != nil {
		return nil, err
	}
	m := r.sso(ctx)
	view, err := m.AgentPolicy(ctx, args.Tenant)
	if err != nil {
		return nil, err
	}
	return &AdminTenantAgentActuationResolver{tenant: args.Tenant, head: view.Head, instance: m.AgentActuationEnabled()}, nil
}

// SetTenantAgentActuationSuspended is the operator's per-tenant override (requires tenant:write).
func (r *AdminResolver) SetTenantAgentActuationSuspended(ctx context.Context, args struct {
	Tenant    string
	Suspended bool
}) (*AdminTenantAgentActuationResolver, error) {
	if err := auth.Authorize(ctx, auth.TenantWrite); err != nil {
		return nil, err
	}
	claims, ok := auth.ClaimsFromContext(ctx)
	if !ok {
		return nil, auth.ErrUnauthenticated
	}
	m := r.sso(ctx)
	head, err := m.SetTenantAgentActuationSuspended(ctx, args.Tenant, args.Suspended, claims.Username)
	if err != nil {
		return nil, err
	}
	return &AdminTenantAgentActuationResolver{tenant: args.Tenant, head: head, instance: m.AgentActuationEnabled()}, nil
}
