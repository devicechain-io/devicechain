// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-user-management/identity"
)

type AgentApprovalAccessResolver struct{ access identity.ApprovalAccess }

func (r *AgentApprovalAccessResolver) InstanceEnabled() bool { return r.access.InstanceEnabled }
func (r *AgentApprovalAccessResolver) CanApprove() bool      { return r.access.CanApprove }

func (r *SchemaResolver) AgentApprovalAccess(ctx context.Context) (*AgentApprovalAccessResolver, error) {
	claims, ok := auth.ClaimsFromContext(ctx)
	if !ok || claims.Tenant == "" {
		return nil, auth.ErrUnauthenticated
	}
	if auth.IsAgentOrigin(claims) || claims.TokenType != auth.TokenTypeAccess || claims.ScopeAnchors != nil {
		return nil, fmt.Errorf("%w: agentApprovalAccess answers only a person's own unscoped session", auth.ErrForbidden)
	}
	access, err := r.getIdentityManager(ctx).AgentApprovalAccess(ctx, claims)
	if err != nil {
		return nil, err
	}
	return &AgentApprovalAccessResolver{access: access}, nil
}
