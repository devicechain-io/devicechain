// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestInvitationPasswordScopeMigrationBackfill(t *testing.T) {
	db := newMigratedDB(t)
	migration := NewInvitationPasswordScopeMigration()
	require.NoError(t, migration.Rollback(db))

	created := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	identity := func(email, hash string, at time.Time) uint {
		require.NoError(t, db.Exec(`INSERT INTO iam_identities (email, enabled, password_hash, session_epoch, created_at, updated_at) VALUES (?, true, ?, 'e', ?, ?)`,
			email, hash, at, at).Error)
		var id uint
		require.NoError(t, db.Raw(`SELECT id FROM iam_identities WHERE email = ?`, email).Row().Scan(&id))
		return id
	}
	membership := func(identity uint, tenant string) uint {
		require.NoError(t, db.Exec(`INSERT INTO iam_memberships (identity_id, tenant_id, enabled, created_at, updated_at) VALUES (?, ?, true, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
			identity, tenant).Error)
		var id uint
		require.NoError(t, db.Raw(`SELECT id FROM iam_memberships WHERE identity_id = ? AND tenant_id = ?`, identity, tenant).Row().Scan(&id))
		return id
	}
	digest := byte(0)
	invitation := func(tenant string, mem uint, at time.Time, accepted bool) {
		digest++
		var acceptedAt *time.Time
		if accepted {
			a := at.Add(time.Hour)
			acceptedAt = &a
		}
		require.NoError(t, db.Exec(`INSERT INTO iam_invitations (tenant_id, membership_id, token_digest, expires_at, created_at, accepted_at) VALUES (?, ?, ?, ?, ?, ?)`,
			tenant, mem, []byte{digest}, at.Add(7*24*time.Hour), at, acceptedAt).Error)
	}

	// Made by acme's invitation and accepted there; globex's invitation was revoked and the
	// membership enabled by hand; initech's was added by the operator with no invitation.
	squat := identity("squat@example.com", "hash", created)
	acme := membership(squat, "acme")
	invitation("acme", acme, created.Add(10*time.Millisecond), true)
	globex := membership(squat, "globex")
	invitation("globex", globex, created.Add(48*time.Hour), false)
	initech := membership(squat, "initech")

	// Made by an invitation whose membership was removed since: no membership, no
	// invitation left, and still a password someone else may have chosen.
	identity("removed@example.com", "hash", created)

	// Made by a first invitation that was removed, then invited again much later and
	// enabled by hand after that invitation was revoked.
	reinvited := identity("reinvited@example.com", "hash", created)
	again := membership(reinvited, "globex")
	invitation("globex", again, created.Add(30*24*time.Hour), false)

	// Created by the operator, member by the operator's hand only: untouched.
	op := identity("op@example.com", "hash", created)
	opMem := membership(op, "globex")

	// A system role: the operator's own account, untouched whatever its invitations.
	require.NoError(t, db.Exec(`INSERT INTO iam_roles (scope, token, created_at, updated_at) VALUES ('system', 'backfill-test-role', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`).Error)
	su := identity("su@example.com", "hash", created)
	require.NoError(t, db.Exec(`INSERT INTO iam_identity_system_roles (identity_id, role_id) SELECT ?, id FROM iam_roles WHERE token = 'backfill-test-role'`, su).Error)
	invitation("acme", membership(su, "acme"), created, false)

	// Made by an invitation never accepted: no password, untouched.
	pending := identity("pending@example.com", "", created)
	invitation("acme", membership(pending, "acme"), created, false)

	require.NoError(t, migration.Migrate(db))
	require.NoError(t, migration.Migrate(db), "replay must succeed")

	flag := func(email string) bool {
		var v bool
		require.NoError(t, db.Raw(`SELECT password_from_invitation FROM iam_identities WHERE email = ?`, email).Row().Scan(&v))
		return v
	}
	admits := func(mem uint) bool {
		var v bool
		require.NoError(t, db.Raw(`SELECT admits_invited_password FROM iam_memberships WHERE id = ?`, mem).Row().Scan(&v))
		return v
	}
	require.True(t, flag("squat@example.com"))
	require.True(t, admits(acme), "the accepted membership no longer admits the password")
	require.False(t, admits(globex), "a membership enabled after its invitation was revoked admits the password")
	require.True(t, admits(initech), "an operator-added membership from before the upgrade lost the password")
	require.True(t, flag("removed@example.com"), "an identity whose invitation membership was removed was not marked")
	require.True(t, flag("reinvited@example.com"), "an identity invited long after it was created was not marked")
	require.False(t, admits(again), "a re-invited membership enabled without acceptance admits the password")
	require.False(t, flag("op@example.com"))
	require.False(t, admits(opMem))
	require.False(t, flag("su@example.com"))
	require.False(t, flag("pending@example.com"))

	// An older pod's insert omits both columns and gets false.
	require.NoError(t, db.Exec(`INSERT INTO iam_identities (email, enabled, password_hash, session_epoch) VALUES ('old@example.com', true, 'h', 'e')`).Error)
	require.False(t, flag("old@example.com"))
	require.False(t, db.Migrator().HasTable("invitation_password_identity_snapshots"))
	require.False(t, db.Migrator().HasTable("invitation_password_membership_snapshots"))
}
