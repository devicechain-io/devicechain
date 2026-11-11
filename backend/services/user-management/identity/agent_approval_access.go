// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-microservice/auth"
)

// ApprovalAccess is visibility of the caller's own approvals inbox, not authorization to act.
type ApprovalAccess struct {
	InstanceEnabled bool
	CanApprove      bool
}

// AgentApprovalAccess reads only the caller's current standing. Alarm proposals are the
// proposal kinds supported this release; their raw write authority is alarm:write.
func (m *Manager) AgentApprovalAccess(ctx context.Context, claims *auth.Claims) (ApprovalAccess, error) {
	if claims == nil || claims.Tenant == "" {
		return ApprovalAccess{}, auth.ErrUnauthenticated
	}
	if auth.IsAgentOrigin(claims) || claims.TokenType != auth.TokenTypeAccess || claims.ScopeAnchors != nil {
		return ApprovalAccess{}, fmt.Errorf("%w: agentApprovalAccess answers only a person's own unscoped session", auth.ErrForbidden)
	}
	out := ApprovalAccess{InstanceEnabled: m.agentActuation}
	if !out.InstanceEnabled {
		return out, nil
	}
	_, _, doc, _, err := m.publishedPolicy(ctx, claims.Tenant)
	if err != nil {
		return ApprovalAccess{}, err
	}
	if doc == nil {
		return out, nil
	}
	out.CanApprove, err = m.approverStanding(ctx, claims.Tenant, claims.Username, doc.ApproverRoles(), auth.AlarmWrite)
	return out, err
}
