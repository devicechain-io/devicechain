// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package iam

import (
	"context"
	"testing"

	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Which store writes rotate an identity's session epoch, and which must not.
//
// The epoch is the one value every refresh and identity token is checked against at
// redemption, so the rule is carried by the store methods that change a credential —
// no caller can forget it. That puts the whole policy here: a write that rotates when
// it should not signs people out for nothing, and one that does not rotate when it
// should leaves a stolen refresh token alive. Each is asserted by reading the row
// back, not by trusting the struct the method was handed.

func newEpochTestStore(t *testing.T) (*Store, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, rdb.RegisterTokenGrammar(db))
	require.NoError(t, db.AutoMigrate(&TenantTier{}, &Tenant{}, &Role{}, &Identity{}, &Membership{}))
	return NewStore(&rdb.RdbManager{Database: db}), db
}

// storedEpoch reads the column straight from the table.
func storedEpoch(t *testing.T, db *gorm.DB, email string) string {
	t.Helper()
	var epoch string
	require.NoError(t, db.Raw(`SELECT session_epoch FROM iam_identities WHERE email = ?`, email).
		Scan(&epoch).Error)
	return epoch
}

func createIdentity(t *testing.T, s *Store, email string) *Identity {
	t.Helper()
	id := &Identity{Email: email, Enabled: true, PasswordHash: "h0"}
	require.NoError(t, s.CreateIdentity(context.Background(), id))
	return id
}

func TestNewSessionEpochIsA128BitRandomValue(t *testing.T) {
	a, b := NewSessionEpoch(), NewSessionEpoch()
	require.Len(t, a, 22, "16 bytes base64url without padding is 22 characters")
	require.NotEqual(t, a, b)
}

// A new row always gets a fresh epoch, and one the CALLER set is overwritten. That is
// what stops a re-created identity inheriting the previous one's sessions: a caller
// copying fields from the old row cannot carry the epoch across.
func TestBeforeCreateAssignsAFreshEpochAndOverridesACallerSetOne(t *testing.T) {
	s, db := newEpochTestStore(t)
	ctx := context.Background()

	a := createIdentity(t, s, "a@example.com")
	b := createIdentity(t, s, "b@example.com")
	require.NotEmpty(t, storedEpoch(t, db, "a@example.com"))
	require.NotEqual(t, storedEpoch(t, db, "a@example.com"), storedEpoch(t, db, "b@example.com"),
		"two identities must not share a session epoch")
	require.Equal(t, storedEpoch(t, db, "a@example.com"), a.SessionEpoch,
		"the struct handed to Create must reflect what was written")
	_ = b

	planted := &Identity{Email: "c@example.com", Enabled: true, PasswordHash: "h", SessionEpoch: "carried-over"}
	require.NoError(t, s.CreateIdentity(ctx, planted))
	got := storedEpoch(t, db, "c@example.com")
	require.NotEqual(t, "carried-over", got, "a caller-set epoch survived the insert")
	require.NotEmpty(t, got)
}

// The runtime seed is the only way the bootstrap superuser comes into being (the
// baseline migration seeds no identity), so it must get an epoch like any other row —
// otherwise the first sign-in to a fresh instance would be refused at the mint.
func TestSeedSuperuserGetsASessionEpoch(t *testing.T) {
	s, db := newEpochTestStore(t)
	require.NoError(t, s.SeedSuperuser(context.Background(), "root@example.com", "hash",
		[]string{"*"}, []string{"*"}))
	require.NotEmpty(t, storedEpoch(t, db, "root@example.com"))
}

func TestSetPasswordHashRotatesTheEpoch(t *testing.T) {
	s, db := newEpochTestStore(t)
	id := createIdentity(t, s, "a@example.com")
	before := storedEpoch(t, db, "a@example.com")

	require.NoError(t, s.SetPasswordHash(context.Background(), id, "h1"))

	after := storedEpoch(t, db, "a@example.com")
	require.NotEmpty(t, after)
	require.NotEqual(t, before, after, "a password reset must end the identity's sessions")
	require.Equal(t, after, id.SessionEpoch, "the new epoch must be reflected onto the struct")
	var hash string
	require.NoError(t, db.Raw(`SELECT password_hash FROM iam_identities WHERE email = ?`, "a@example.com").Scan(&hash).Error)
	require.Equal(t, "h1", hash, "the hash itself must still be written")
}

// Disabling rotates; enabling does not. Rotating on enable would sign an already
// enabled identity out for no credential event, and NOT rotating on disable would let
// a re-enable revive every stolen token still in the refresh store.
func TestSetIdentityEnabledRotatesOnDisableOnly(t *testing.T) {
	s, db := newEpochTestStore(t)
	ctx := context.Background()
	id := createIdentity(t, s, "a@example.com")
	e0 := storedEpoch(t, db, "a@example.com")

	require.NoError(t, s.SetIdentityEnabled(ctx, id, true))
	require.Equal(t, e0, storedEpoch(t, db, "a@example.com"), "enabling an enabled identity rotated its epoch")

	require.NoError(t, s.SetIdentityEnabled(ctx, id, false))
	e1 := storedEpoch(t, db, "a@example.com")
	require.NotEqual(t, e0, e1, "disabling must rotate the epoch")
	require.Equal(t, e1, id.SessionEpoch)

	require.NoError(t, s.SetIdentityEnabled(ctx, id, true))
	require.Equal(t, e1, storedEpoch(t, db, "a@example.com"), "re-enabling must not rotate the epoch")

	var enabled bool
	require.NoError(t, db.Raw(`SELECT enabled FROM iam_identities WHERE email = ?`, "a@example.com").Scan(&enabled).Error)
	require.True(t, enabled)
}

// Role and membership changes take effect at the next refresh because every refresh
// re-resolves them; they are not credential events and must not sign anyone out. The
// association writes here save the identity through an UPDATE, so BeforeCreate must
// never fire on them — this pins that it does not.
func TestRoleAndMembershipChangesDoNotRotateTheEpoch(t *testing.T) {
	s, db := newEpochTestStore(t)
	ctx := context.Background()
	id := createIdentity(t, s, "a@example.com")
	e0 := storedEpoch(t, db, "a@example.com")

	sys := &Role{Scope: ScopeSystem, Token: SuperuserRoleToken, Authorities: []string{"*"}}
	require.NoError(t, s.CreateRole(ctx, sys))
	ten := &Role{Scope: ScopeTenant, Token: "tenant-role", Authorities: []string{"device:read"}}
	require.NoError(t, s.CreateRole(ctx, ten))

	require.NoError(t, s.ReplaceSystemRoles(ctx, id, []Role{*sys}))
	require.NoError(t, s.AssignSystemRoles(ctx, id, []Role{*sys}))
	mem, err := s.AddMembership(ctx, id.ID, "acme", []Role{*ten})
	require.NoError(t, err)
	require.NoError(t, s.ReplaceMembershipRoles(ctx, mem, nil))
	require.NoError(t, s.SetMembershipEnabled(ctx, mem, false))
	require.NoError(t, s.RemoveMembership(ctx, mem))
	require.NoError(t, s.UpdateIdentityFields(ctx, id, map[string]any{"first_name": "Ada"}))

	require.Equal(t, e0, storedEpoch(t, db, "a@example.com"),
		"a role, membership or profile change rotated the session epoch")
}
