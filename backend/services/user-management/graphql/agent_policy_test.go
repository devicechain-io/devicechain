// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"errors"
	"testing"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/devicechain-io/dc-user-management/identity"
	"github.com/stretchr/testify/require"
)

// The governance plane is never agent-authorised: every agent-policy write refuses an
// agent-origin token even when it carries agent:admin, and refuses a person without agent:admin.
// A person's own console token holding it passes.
func TestPolicyWritesRefuseAgentOrigin(t *testing.T) {
	person := accessClaims(string(auth.AgentAdmin))
	_, err := authorizePolicyWrite(auth.WithClaims(context.Background(), person))
	require.NoError(t, err, "control: a person's console token holding agent:admin passes")

	tenantAdmin := accessClaims(string(auth.AuthorityAll))
	_, err = authorizePolicyWrite(auth.WithClaims(context.Background(), tenantAdmin))
	require.NoError(t, err, "a tenant administrator holding * passes")

	agents := map[string]*auth.Claims{
		"oauth client": func() *auth.Claims { c := accessClaims(string(auth.AgentAdmin)); c.ClientId = "agent"; return c }(),
		"oauth scope":  func() *auth.Claims { c := accessClaims(string(auth.AgentAdmin)); c.Scope = "read-only"; return c }(),
		"audience": func() *auth.Claims {
			c := accessClaims(string(auth.AgentAdmin))
			c.Audience = []string{"https://x"}
			return c
		}(),
		"service acct": func() *auth.Claims { c := accessClaims(string(auth.AuthorityAll)); c.ClientId = "dcsa_bot"; return c }(),
	}
	for name, c := range agents {
		_, err := authorizePolicyWrite(auth.WithClaims(context.Background(), c))
		require.True(t, errors.Is(err, auth.ErrForbidden), "%s: got %v", name, err)
	}

	_, err = authorizePolicyWrite(auth.WithClaims(context.Background(), accessClaims(string(auth.MemberWrite))))
	require.Error(t, err, "a person without agent:admin must be refused")
}

func TestTenantKillFlagDisablesActuationInEffect(t *testing.T) {
	r := &AgentPolicyResolver{instance: true, view: &identity.AgentPolicyView{
		Head: &iam.AgentPolicy{}, Published: &iam.AgentPolicyVersion{Document: `{"actuation":"enabled"}`},
	}}
	require.True(t, r.ActuationInEffect(), "control")
	require.False(t, r.ActuationOff())
	r.view.Head.TenantActuationOff = true
	require.True(t, r.ActuationOff())
	require.False(t, r.ActuationInEffect())
}
