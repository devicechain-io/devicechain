// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package graphql

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/stretchr/testify/require"
)

func TestAdminManagedMembershipRefusalOnTheWire(t *testing.T) {
	e := newAdminEscalationEnv(t)
	ctx := context.Background()
	_, err := e.svc.AddMembership(ctx, "plain@example.com", "acme", []string{"viewer"})
	require.NoError(t, err)
	id, err := e.store.IdentityByEmail(ctx, "plain@example.com")
	require.NoError(t, err)
	conn := &iam.IdpConnection{Token: "corporate", Kind: iam.IdpKindTenant}
	require.NoError(t, e.store.CreateIdpConnection(ctx, conn))
	sys := e.db.WithContext(core.WithSystemContext(ctx))
	require.NoError(t, sys.Create(&iam.IdpConnectionTenant{ConnectionID: conn.ID, TenantId: "acme"}).Error)
	require.NoError(t, sys.Model(&iam.Membership{}).Where("identity_id = ?", id.ID).Update("managed_by", conn.ID).Error)
	before, err := e.store.MembershipByIdentityTenant(ctx, id.ID, "acme")
	require.NoError(t, err)
	for _, operation := range []string{
		`setMembershipEnabled(email:"plain@example.com",tenant:"acme",enabled:false) { email }`,
		`removeMembership(email:"plain@example.com",tenant:"acme") { email }`,
		`setMembershipRoles(email:"plain@example.com",tenant:"acme",roleTokens:[]) { email }`,
	} {
		response := e.schema.Exec(e.ctx("root"), "mutation {"+operation+"}", "", nil)
		require.Len(t, response.Errors, 1, operation)
		require.Equal(t, "MANAGED_BY_IDP", response.Errors[0].Extensions["code"])
		after, err := e.store.MembershipByIdentityTenant(ctx, id.ID, "acme")
		require.NoError(t, err)
		require.Equal(t, before, after)
	}
}
