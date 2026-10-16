// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The three customer-users migrations (ADR-081), run as the whole chain and checked by
// writing through the columns and reading back — not by asking the migrator whether it
// thinks they exist. migration-diff gates the Postgres DDL; these pin the behaviour a DDL
// diff cannot show: which duplicates the new uniqueness rule refuses.

// TestRolesCarryATenantAndTokensAreUniquePerSide is the precondition of the purge's claim:
// iam_roles has a tenant_id column (so the catalog sweeps a tenant's own roles), and the
// two partial unique indexes refuse exactly the duplicates they should.
func TestRolesCarryATenantAndTokensAreUniquePerSide(t *testing.T) {
	db := newMigratedDB(t)
	insert := func(scope, token string, tenant any) error {
		return db.Exec(`INSERT INTO iam_roles (scope, token, tenant_id, created_at, updated_at) VALUES (?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
			scope, token, tenant).Error
	}
	require.NoError(t, insert("tenant", "auditor", nil))
	require.Error(t, insert("tenant", "auditor", nil),
		"the catalogue held the same tenant-scope token twice — a NULL tenant is distinct in a plain unique index")
	require.NoError(t, insert("system", "auditor", nil), "the same token at the other scope is a different role")

	require.NoError(t, insert("tenant", "tech", "acme"))
	require.NoError(t, insert("tenant", "tech", "globex"), "two tenants may each own a role at one token")
	require.Error(t, insert("tenant", "tech", "acme"), "one tenant held the same token twice")

	var tenants []*string
	require.NoError(t, db.Raw(`SELECT tenant_id FROM iam_roles WHERE token = 'tech' ORDER BY tenant_id`).Scan(&tenants).Error)
	require.Len(t, tenants, 2)
	require.Equal(t, "acme", *tenants[0])

	// The old whole-table index is gone (it would refuse globex's "tech" above, so the
	// insert succeeding already says so); and it is not left behind under its name.
	require.False(t, db.Migrator().HasIndex("iam_roles", "idx_iam_role_scope_token"))
}

// A membership's scope is NULL — the whole tenant — unless set, and round-trips as JSON.
func TestMembershipScopeIsNullUntilSet(t *testing.T) {
	db := newMigratedDB(t)
	require.NoError(t, db.Exec(`INSERT INTO iam_identities (email, enabled, password_hash, session_epoch, created_at, updated_at) VALUES ('a@x.example', true, '', 'e', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO iam_memberships (identity_id, tenant_id, enabled, created_at, updated_at) VALUES (1, 'acme', true, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`).Error)

	var scope *string
	require.NoError(t, db.Raw(`SELECT scope FROM iam_memberships WHERE id = 1`).Scan(&scope).Error)
	require.Nil(t, scope, "an existing membership must read as the whole tenant, not as a scope")

	require.NoError(t, db.Exec(`UPDATE iam_memberships SET scope = ? WHERE id = 1`, `["customer:acme-corp"]`).Error)
	require.NoError(t, db.Raw(`SELECT scope FROM iam_memberships WHERE id = 1`).Scan(&scope).Error)
	require.NotNil(t, scope)
	require.JSONEq(t, `["customer:acme-corp"]`, *scope)
}

// The invitations table exists under its own name, its digest is unique, and it carries
// the tenant column the purge sweeps it by.
func TestTheInvitationsTableIsCreated(t *testing.T) {
	db := newMigratedDB(t)
	require.True(t, db.Migrator().HasTable("iam_invitations"))
	for _, c := range []string{"tenant_id", "membership_id", "token_digest", "invited_by", "expires_at", "accepted_at", "revoked_at"} {
		require.Truef(t, db.Migrator().HasColumn("iam_invitations", c), "iam_invitations has no %s", c)
	}
	require.False(t, db.Migrator().HasTable("invitations"), "the snapshot lost its TableName and created a stray table")

	ins := func(digest string) error {
		return db.Exec(`INSERT INTO iam_invitations (tenant_id, membership_id, token_digest, expires_at, created_at) VALUES ('acme', 1, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
			[]byte(digest)).Error
	}
	require.NoError(t, ins("d1"))
	require.Error(t, ins("d1"), "two invitations share one digest")
}
