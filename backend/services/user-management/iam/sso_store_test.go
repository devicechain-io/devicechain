// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package iam

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"testing"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/rdb/sqlitetest"
	"github.com/devicechain-io/dc-microservice/secrets"
	"github.com/devicechain-io/dc-user-management/schema"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// newSsoStore builds a store on the REAL migration chain, with foreign keys enforced.
//
// Not AutoMigrate over the live models, which is what most tests in this package do: the
// foreign keys these tests exist to exercise (the composite one from a role mapping to the
// tenant set, the RESTRICT one to a role) are declared in the migration, so the only
// honest way to test them is against the tables the migration builds. The pragma rides the
// DSN because gorm pools connections and a pragma statement configures only one of them.
func newSsoStore(t *testing.T) *Store {
	t.Helper()
	s, _ := newSsoFixture(t)
	return s
}

// newSsoFixture is newSsoStore plus the REAL secret store over the same database — the
// Postgres-backend store, built through the constructor main uses — so a test can see a
// connection's sealed client secret written and removed alongside its row.
func newSsoFixture(t *testing.T) (*Store, secrets.SecretStore) {
	t.Helper()
	db := sqlitetest.OpenMemory(t, nil, "_pragma=foreign_keys(1)")
	// Migrate BEFORE the callbacks, as production does: gorm's own schema introspection is
	// a statement the tenant guard (rightly) refuses.
	for _, m := range schema.Migrations {
		require.NoErrorf(t, m.Migrate(db), "migration %s", m.ID)
	}
	require.NoError(t, secrets.NewSecretStoreSchema().Migrate(db))
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, rdb.RegisterTokenGrammar(db))
	var fk int
	require.NoError(t, db.Raw(`PRAGMA foreign_keys`).Scan(&fk).Error)
	require.Equal(t, 1, fk, "a test of foreign keys on a database not enforcing them proves nothing")
	raw := make([]byte, 32)
	_, err := rand.Read(raw)
	require.NoError(t, err)
	sealed, err := secrets.NewFromConfig(context.Background(),
		config.SecretsConfiguration{RootKey: base64.StdEncoding.EncodeToString(raw)}, db)
	require.NoError(t, err)
	return NewStore(&rdb.RdbManager{Database: db}), sealed
}

func ssoTenant(t *testing.T, s *Store, token string) {
	t.Helper()
	var tierID uint
	require.NoError(t, s.sys(context.Background()).Model(&TenantTier{}).Select("id").Limit(1).Scan(&tierID).Error)
	require.NotZero(t, tierID)
	require.NoError(t, s.CreateTenant(context.Background(), &Tenant{Token: token, TierID: tierID, Enabled: true}))
}

func ssoConnection(t *testing.T, s *Store, token string, kind IdpKind) *IdpConnection {
	t.Helper()
	c := &IdpConnection{Token: token, Name: token, Kind: kind, Issuer: "https://" + token + ".idp.example", ClientID: "cid"}
	require.NoError(t, s.CreateIdpConnection(context.Background(), c))
	return c
}

func ssoIdentity(t *testing.T, s *Store, email string) *Identity {
	t.Helper()
	id := &Identity{Email: email, Enabled: true}
	require.NoError(t, s.CreateIdentity(context.Background(), id))
	return id
}

func count(t *testing.T, s *Store, model any) int64 {
	t.Helper()
	var n int64
	require.NoError(t, s.sys(context.Background()).Model(model).Count(&n).Error)
	return n
}

func TestCreateConnectionDefaultsTheCeilingAndRefusesAnUnknownKind(t *testing.T) {
	s := newSsoStore(t)
	c := ssoConnection(t, s, "okta", IdpKindTenant)
	got, err := s.IdpConnectionByToken(context.Background(), "okta")
	require.NoError(t, err)
	require.Equal(t, DefaultSessionMaxAgeSeconds, got.SessionMaxAgeSeconds,
		"an unset ceiling must be the 12 hour default, not zero (expire immediately)")
	require.Equal(t, "email", got.EmailClaim)
	require.False(t, got.Enabled, "a connection is created disabled")
	require.Equal(t, c.ID, got.ID)

	err = s.CreateIdpConnection(context.Background(), &IdpConnection{Token: "x", Kind: "weird", Issuer: "i", ClientID: "c"})
	require.ErrorIs(t, err, ErrInvalidIdpConnection)
}

func TestConnectionDeleteDisablesManagedMemberships(t *testing.T) {
	s, sealed := newSsoFixture(t)
	ctx := context.Background()
	ssoTenant(t, s, "acme")
	ssoTenant(t, s, "globex")
	conn := ssoConnection(t, s, "okta", IdpKindTenant)

	person := ssoIdentity(t, s, "p@acme.example")
	managed := uint(conn.ID)
	require.NoError(t, s.sys(ctx).Create(&Membership{IdentityID: person.ID, TenantId: "acme", Enabled: true, ManagedBy: &managed}).Error)
	require.NoError(t, s.sys(ctx).Create(&Membership{IdentityID: person.ID, TenantId: "globex", Enabled: true}).Error)

	require.NoError(t, s.DeleteIdpConnection(ctx, sealed, conn))

	m, err := s.MembershipByIdentityTenant(ctx, person.ID, "acme")
	require.NoError(t, err, "the managed membership must survive the connection: deleting it discards the record")
	require.False(t, m.Enabled, "an IdP-granted membership must stop working when its IdP is deleted")
	require.NotNil(t, m.ManagedBy, "clearing managed_by would turn IdP-granted access into a manual grant")
	require.Equal(t, conn.ID, *m.ManagedBy)

	manual, err := s.MembershipByIdentityTenant(ctx, person.ID, "globex")
	require.NoError(t, err)
	require.True(t, manual.Enabled, "a manual membership is not the connection's to disable")
	require.Nil(t, manual.ManagedBy)
}

func TestConnectionDeleteCascadesLinksDomainsMappings(t *testing.T) {
	s, sealed := newSsoFixture(t)
	ctx := context.Background()
	ssoTenant(t, s, "acme")
	conn := ssoConnection(t, s, "okta", IdpKindTenant)
	other := ssoConnection(t, s, "entra", IdpKindTenant)
	person := ssoIdentity(t, s, "p@acme.example")
	role := &Role{Scope: ScopeTenant, Token: "operator"}
	require.NoError(t, s.CreateRole(ctx, role))

	require.NoError(t, s.ReplaceConnectionTenants(ctx, conn, []string{"acme"}))
	require.NoError(t, s.ReplaceConnectionTenants(ctx, other, []string{"acme"}))
	require.NoError(t, s.sys(ctx).Create(&IdpRoleMapping{ConnectionID: conn.ID, TenantId: "acme", GroupValue: "ops", RoleID: role.ID}).Error)
	require.NoError(t, s.sys(ctx).Create(&IdpRoleMapping{ConnectionID: other.ID, TenantId: "acme", GroupValue: "ops", RoleID: role.ID}).Error)
	_, err := s.AddIdpDomain(ctx, conn.ID, "acme.example", "root")
	require.NoError(t, err)
	_, err = s.AddIdpDomain(ctx, other.ID, "globex.example", "root")
	require.NoError(t, err)
	require.NoError(t, s.CreateIdentityLink(ctx, &IdentityLink{IdentityID: person.ID, ConnectionID: conn.ID, Subject: "sub-1"}))
	require.NoError(t, s.UpsertLinkRequest(ctx, &SsoLinkRequest{ConnectionID: conn.ID, Subject: "sub-2", Email: "q@acme.example"}))
	require.NoError(t, s.UpsertLinkRequest(ctx, &SsoLinkRequest{ConnectionID: other.ID, Subject: "sub-3", Email: "r@globex.example"}))

	require.NoError(t, s.DeleteIdpConnection(ctx, sealed, conn))

	for name, model := range map[string]any{
		"connections": &IdpConnection{}, "tenants": &IdpConnectionTenant{}, "domains": &IdpConnectionDomain{},
		"mappings": &IdpRoleMapping{}, "links": &IdentityLink{}, "requests": &SsoLinkRequest{},
	} {
		// The OTHER connection's rows are the control: a delete that dropped the whole
		// table would pass a count-is-zero assertion.
		want := int64(1)
		if name == "links" {
			want = 0
		}
		require.Equalf(t, want, count(t, s, model), "%s after deleting one of two connections", name)
	}
	_, err = s.IdpConnectionByToken(ctx, "okta")
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)

	// The person is not the connection's to delete.
	_, err = s.IdentityByEmail(ctx, "p@acme.example")
	require.NoError(t, err)
}

func TestIdentityDeleteRemovesLinks(t *testing.T) {
	s := newSsoStore(t)
	ctx := context.Background()
	conn := ssoConnection(t, s, "okta", IdpKindTenant)
	gone := ssoIdentity(t, s, "gone@acme.example")
	stays := ssoIdentity(t, s, "stays@acme.example")
	require.NoError(t, s.CreateIdentityLink(ctx, &IdentityLink{IdentityID: gone.ID, ConnectionID: conn.ID, Subject: "sub-gone"}))
	require.NoError(t, s.CreateIdentityLink(ctx, &IdentityLink{IdentityID: stays.ID, ConnectionID: conn.ID, Subject: "sub-stays"}))

	loaded, err := s.IdentityByEmail(ctx, "gone@acme.example")
	require.NoError(t, err)
	require.NoError(t, s.DeleteIdentity(ctx, nil, loaded))

	_, err = s.LinkBySubject(ctx, conn.ID, "sub-gone")
	require.ErrorIs(t, err, gorm.ErrRecordNotFound, "a link must not outlive the identity it names")
	_, err = s.LinkBySubject(ctx, conn.ID, "sub-stays")
	require.NoError(t, err, "the other identity's link is the control")

	// Re-created at the same email: a NEW row, with no link inherited.
	again := ssoIdentity(t, s, "gone@acme.example")
	links, err := s.LinksForIdentity(ctx, again.ID)
	require.NoError(t, err)
	require.Empty(t, links)
}

func TestDomainUniqueAcrossConnections(t *testing.T) {
	s := newSsoStore(t)
	ctx := context.Background()
	a := ssoConnection(t, s, "okta", IdpKindTenant)
	b := ssoConnection(t, s, "entra", IdpKindTenant)

	_, err := s.AddIdpDomain(ctx, a.ID, "acme.example", "root")
	require.NoError(t, err)
	_, err = s.AddIdpDomain(ctx, b.ID, "acme.example", "root")
	require.ErrorIs(t, err, ErrDomainTaken, "one domain belongs to one IdP")
	_, err = s.AddIdpDomain(ctx, a.ID, "acme.example", "root")
	require.ErrorIs(t, err, ErrDomainTaken)

	got, err := s.IdpConnectionByDomain(ctx, "acme.example")
	require.NoError(t, err)
	require.Equal(t, a.ID, got.ID)

	// The store refuses what is visibly not the normalised form.
	for _, bad := range []string{"ACME.example", " acme.example", "acme.example.", ""} {
		_, err = s.AddIdpDomain(ctx, a.ID, bad, "root")
		require.ErrorIsf(t, err, ErrInvalidIdpConnection, "%q", bad)
	}
}

func TestReAddDomainAfterDelete(t *testing.T) {
	s, sealed := newSsoFixture(t)
	ctx := context.Background()
	a := ssoConnection(t, s, "okta", IdpKindTenant)
	b := ssoConnection(t, s, "entra", IdpKindTenant)

	_, err := s.AddIdpDomain(ctx, a.ID, "acme.example", "root")
	require.NoError(t, err)
	removed, err := s.RemoveIdpDomain(ctx, a.ID, "acme.example")
	require.NoError(t, err)
	require.True(t, removed)

	// The assertion the soft-delete bug cannot survive: the slot is FREE.
	_, err = s.AddIdpDomain(ctx, b.ID, "acme.example", "root")
	require.NoError(t, err, "a removed domain must be re-addable; a soft delete would keep its unique slot")

	// And the same when the whole connection goes.
	require.NoError(t, s.DeleteIdpConnection(ctx, sealed, b))
	_, err = s.AddIdpDomain(ctx, a.ID, "acme.example", "root")
	require.NoError(t, err)

	removed, err = s.RemoveIdpDomain(ctx, a.ID, "nothing.example")
	require.NoError(t, err)
	require.False(t, removed)
}

func TestMappingRefusedForTenantNotServed(t *testing.T) {
	s := newSsoStore(t)
	ctx := context.Background()
	ssoTenant(t, s, "acme")
	ssoTenant(t, s, "globex")
	conn := ssoConnection(t, s, "okta", IdpKindTenant)
	role := &Role{Scope: ScopeTenant, Token: "operator"}
	require.NoError(t, s.CreateRole(ctx, role))
	require.NoError(t, s.ReplaceConnectionTenants(ctx, conn, []string{"acme"}))

	err := s.sys(ctx).Create(&IdpRoleMapping{ConnectionID: conn.ID, TenantId: "globex", GroupValue: "ops", RoleID: role.ID}).Error
	require.Error(t, err, "a mapping for a tenant the connection does not serve must be refused by the database")

	// The control: the same mapping for a tenant it DOES serve is accepted, so the refusal
	// above is the foreign key and not a malformed row.
	require.NoError(t, s.sys(ctx).Create(&IdpRoleMapping{ConnectionID: conn.ID, TenantId: "acme", GroupValue: "ops", RoleID: role.ID}).Error)

	// Dropping a tenant from the set takes its mappings with it.
	require.NoError(t, s.ReplaceConnectionTenants(ctx, conn, nil))
	require.Zero(t, count(t, s, &IdpRoleMapping{}))
}

func TestRoleInUseByMappingCannotBeDeleted(t *testing.T) {
	s := newSsoStore(t)
	ctx := context.Background()
	ssoTenant(t, s, "acme")
	conn := ssoConnection(t, s, "okta", IdpKindTenant)
	role := &Role{Scope: ScopeTenant, Token: "operator"}
	require.NoError(t, s.CreateRole(ctx, role))
	require.NoError(t, s.ReplaceConnectionTenants(ctx, conn, []string{"acme"}))
	require.NoError(t, s.sys(ctx).Create(&IdpRoleMapping{ConnectionID: conn.ID, TenantId: "acme", GroupValue: "ops", RoleID: role.ID}).Error)

	n, err := s.CountIdpRoleMappingsForRole(ctx, role.ID)
	require.NoError(t, err)
	require.Equal(t, int64(1), n)

	require.Error(t, s.DeleteRole(ctx, role), "the RESTRICT foreign key is the backstop under the admin layer's check")
	_, err = s.CatalogueRoleByScopeToken(ctx, ScopeTenant, "operator")
	require.NoError(t, err, "the role must still exist")
}

func TestReplaceConnectionTenants(t *testing.T) {
	s := newSsoStore(t)
	ctx := context.Background()
	ssoTenant(t, s, "acme")
	ssoTenant(t, s, "globex")
	tenantConn := ssoConnection(t, s, "okta", IdpKindTenant)
	instanceConn := ssoConnection(t, s, "ops", IdpKindInstance)

	require.NoError(t, s.ReplaceConnectionTenants(ctx, tenantConn, []string{"globex", "acme", "acme"}))
	got, err := s.ConnectionTenants(ctx, tenantConn.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"acme", "globex"}, got)

	require.NoError(t, s.ReplaceConnectionTenants(ctx, tenantConn, []string{"globex"}))
	got, _ = s.ConnectionTenants(ctx, tenantConn.ID)
	require.Equal(t, []string{"globex"}, got)

	require.ErrorIs(t, s.ReplaceConnectionTenants(ctx, tenantConn, []string{"nope"}), ErrUnknownTenant)
	got, _ = s.ConnectionTenants(ctx, tenantConn.ID)
	require.Equal(t, []string{"globex"}, got, "a refused replace must change nothing")

	require.ErrorIs(t, s.ReplaceConnectionTenants(ctx, instanceConn, []string{"acme"}), ErrNotTenantConnection,
		"only a tenant connection has a tenant set")

	// A tenant mid-purge cannot be newly served.
	ssoTenant(t, s, "leaving")
	leaving, err := s.TenantByToken(ctx, "leaving")
	require.NoError(t, err)
	require.NoError(t, s.sys(ctx).Model(leaving).Update("purge_state", PurgePurging).Error)
	require.ErrorIs(t, s.ReplaceConnectionTenants(ctx, tenantConn, []string{"leaving"}), ErrUnknownTenant)
}

func TestLinkRules(t *testing.T) {
	s := newSsoStore(t)
	ctx := context.Background()
	conn := ssoConnection(t, s, "okta", IdpKindTenant)
	other := ssoConnection(t, s, "entra", IdpKindTenant)
	alice := ssoIdentity(t, s, "alice@acme.example")
	bob := ssoIdentity(t, s, "bob@acme.example")

	require.NoError(t, s.CreateIdentityLink(ctx, &IdentityLink{IdentityID: alice.ID, ConnectionID: conn.ID, Subject: "s-alice", CreatedBy: "root"}))
	got, err := s.LinkBySubject(ctx, conn.ID, "s-alice")
	require.NoError(t, err)
	require.Equal(t, alice.ID, got.IdentityID)

	require.ErrorIs(t, s.CreateIdentityLink(ctx, &IdentityLink{IdentityID: bob.ID, ConnectionID: conn.ID, Subject: "s-alice"}), ErrSubjectLinked,
		"one external account maps to one identity")
	require.ErrorIs(t, s.CreateIdentityLink(ctx, &IdentityLink{IdentityID: alice.ID, ConnectionID: conn.ID, Subject: "s-alice-2"}), ErrIdentityAlreadyLinked,
		"a second subject for a linked identity is the recycled-email shape and must not relink")

	// The same subject string on another connection is a different account.
	require.NoError(t, s.CreateIdentityLink(ctx, &IdentityLink{IdentityID: bob.ID, ConnectionID: other.ID, Subject: "s-alice"}))

	removed, err := s.DeleteIdentityLink(ctx, alice.ID, conn.ID)
	require.NoError(t, err)
	require.True(t, removed)
	_, err = s.LinkBySubject(ctx, conn.ID, "s-alice")
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestUpsertLinkRequestIsOneRowPerAccount(t *testing.T) {
	s := newSsoStore(t)
	ctx := context.Background()
	conn := ssoConnection(t, s, "okta", IdpKindTenant)

	require.NoError(t, s.UpsertLinkRequest(ctx, &SsoLinkRequest{ConnectionID: conn.ID, Subject: "s1", Email: "a@acme.example", ClaimsDigest: "d1"}))
	require.NoError(t, s.UpsertLinkRequest(ctx, &SsoLinkRequest{ConnectionID: conn.ID, Subject: "s1", Email: "b@acme.example", ClaimsDigest: "d2"}))
	require.NoError(t, s.UpsertLinkRequest(ctx, &SsoLinkRequest{ConnectionID: conn.ID, Subject: "s2", Email: "c@acme.example"}))

	all, err := s.ListSsoLinkRequests(ctx)
	require.NoError(t, err)
	require.Len(t, all, 2, "the same account refused twice is one request")
	require.Equal(t, "b@acme.example", all[0].Email, "the request carries the latest attempt")
	require.Equal(t, "d2", all[0].ClaimsDigest)

	one, err := s.SsoLinkRequestByID(ctx, all[1].ID)
	require.NoError(t, err)
	require.Equal(t, "s2", one.Subject)
	gone, err := s.DeleteSsoLinkRequest(ctx, one.ID)
	require.NoError(t, err)
	require.True(t, gone)
	_, err = s.SsoLinkRequestByID(ctx, one.ID)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestConnectionRoundTripsItsJSONColumns(t *testing.T) {
	s := newSsoStore(t)
	ctx := context.Background()
	c := &IdpConnection{
		Token: "google", Name: "Google", Kind: IdpKindTenant, Issuer: "https://accounts.google.com", ClientID: "cid",
		Scopes: []string{"openid", "email"}, RequiredClaims: map[string]string{"hd": "acme.example"},
		RequiredAmr: []string{"mfa"}, DefaultTenantRoles: []string{"viewer"},
		DiscoverySnapshot: &DiscoverySnapshot{Issuer: "https://accounts.google.com", TokenEndpoint: "https://t", JwksURI: "https://j"},
	}
	require.NoError(t, s.CreateIdpConnection(ctx, c))
	got, err := s.IdpConnectionByToken(ctx, "google")
	require.NoError(t, err)
	require.Equal(t, []string{"openid", "email"}, got.Scopes)
	require.Equal(t, map[string]string{"hd": "acme.example"}, got.RequiredClaims)
	require.Equal(t, []string{"mfa"}, got.RequiredAmr)
	require.Equal(t, []string{"viewer"}, got.DefaultTenantRoles)
	require.NotNil(t, got.DiscoverySnapshot)
	require.Equal(t, "https://j", got.DiscoverySnapshot.JwksURI)

	bare := ssoConnection(t, s, "bare", IdpKindInstance)
	got, err = s.IdpConnectionByID(ctx, bare.ID)
	require.NoError(t, err)
	require.Nil(t, got.DiscoverySnapshot, "no snapshot is NULL, not a zero-valued document that reads as approved-empty")
}

func TestSameEndpointsIgnoresFetchTime(t *testing.T) {
	a := DiscoverySnapshot{Issuer: "i", TokenEndpoint: "t", JwksURI: "j"}
	b := a
	b.FetchedAt = b.FetchedAt.AddDate(0, 0, 1)
	require.True(t, a.SameEndpoints(b))
	b.TokenEndpoint = "t2"
	require.False(t, a.SameEndpoints(b))
}

// TestConnectionDeleteRemovesItsClientSecret: the sealed client secret goes with the row,
// in the same transaction. The other connection's secret is the control — a delete keyed
// on the wrong handle, or one that cleared the instance scope, would pass "gone" alone.
func TestConnectionDeleteRemovesItsClientSecret(t *testing.T) {
	s, sealed := newSsoFixture(t)
	ctx := context.Background()
	conn := ssoConnection(t, s, "okta", IdpKindTenant)
	other := ssoConnection(t, s, "entra", IdpKindTenant)
	require.NoError(t, sealed.Put(ctx, IdpConnectionSecretRef(conn.ID), []byte("okta-secret")))
	require.NoError(t, sealed.Put(ctx, IdpConnectionSecretRef(other.ID), []byte("entra-secret")))

	require.Error(t, s.DeleteIdpConnection(ctx, nil, conn), "no secret store must refuse, not leave the secret behind")
	_, err := s.IdpConnectionByID(ctx, conn.ID)
	require.NoError(t, err, "a refused delete must delete nothing")

	require.NoError(t, s.DeleteIdpConnection(ctx, sealed, conn))
	ok, err := sealed.Exists(ctx, IdpConnectionSecretRef(conn.ID))
	require.NoError(t, err)
	require.False(t, ok, "a deleted connection must not leave its client secret sealed in the store")
	ok, err = sealed.Exists(ctx, IdpConnectionSecretRef(other.ID))
	require.NoError(t, err)
	require.True(t, ok, "the other connection's secret is not this delete's to remove")

	// A connection that never had a secret deletes cleanly (the secret delete is idempotent).
	require.NoError(t, s.DeleteIdpConnection(ctx, sealed, other))
	require.NoError(t, s.DeleteIdpConnection(ctx, sealed, ssoConnection(t, s, "bare", IdpKindInstance)))
}

// The connection and identity deletes are guarded TWICE: by the store's explicit statements
// and by the migration's ON DELETE CASCADE foreign keys. Each layer masks the other in the
// tests above, so removing either one alone would pass them. These two tests hold each
// layer on its own.

// TestTheForeignKeysCascadeOnTheirOwn deletes the parents with raw SQL, bypassing the
// store, so only the migration's foreign keys can remove the children.
func TestTheForeignKeysCascadeOnTheirOwn(t *testing.T) {
	s := newSsoStore(t)
	ctx := context.Background()
	ssoTenant(t, s, "acme")
	conn := ssoConnection(t, s, "okta", IdpKindTenant)
	other := ssoConnection(t, s, "entra", IdpKindTenant)
	gone := ssoIdentity(t, s, "gone@acme.example")
	role := &Role{Scope: ScopeTenant, Token: "operator"}
	require.NoError(t, s.CreateRole(ctx, role))
	require.NoError(t, s.ReplaceConnectionTenants(ctx, conn, []string{"acme"}))
	require.NoError(t, s.sys(ctx).Create(&IdpRoleMapping{ConnectionID: conn.ID, TenantId: "acme", GroupValue: "ops", RoleID: role.ID}).Error)
	_, err := s.AddIdpDomain(ctx, conn.ID, "acme.example", "root")
	require.NoError(t, err)
	require.NoError(t, s.UpsertLinkRequest(ctx, &SsoLinkRequest{ConnectionID: conn.ID, Subject: "sub-2", Email: "q@acme.example"}))
	require.NoError(t, s.CreateIdentityLink(ctx, &IdentityLink{IdentityID: gone.ID, ConnectionID: other.ID, Subject: "sub-gone"}))

	require.NoError(t, s.sys(ctx).Exec(`DELETE FROM iam_identities WHERE id = ?`, gone.ID).Error)
	require.Zero(t, count(t, s, &IdentityLink{}), "iam_identity_links.identity_id must cascade")

	require.NoError(t, s.sys(ctx).Exec(`DELETE FROM iam_idp_connections WHERE id = ?`, conn.ID).Error)
	for name, model := range map[string]any{
		"tenants": &IdpConnectionTenant{}, "domains": &IdpConnectionDomain{},
		"mappings": &IdpRoleMapping{}, "requests": &SsoLinkRequest{},
	} {
		require.Zerof(t, count(t, s, model), "%s must cascade from the connection", name)
	}
}

// TestTheStoreDeletesCascadeWithoutForeignKeys runs the store's deletes on a database that
// is NOT enforcing foreign keys, so only the explicit statements can remove the children.
func TestTheStoreDeletesCascadeWithoutForeignKeys(t *testing.T) {
	s, sealed := newSsoFixture(t)
	ctx := context.Background()
	ssoTenant(t, s, "acme")
	conn := ssoConnection(t, s, "okta", IdpKindTenant)
	other := ssoConnection(t, s, "entra", IdpKindTenant)
	gone := ssoIdentity(t, s, "gone@acme.example")
	role := &Role{Scope: ScopeTenant, Token: "operator"}
	require.NoError(t, s.CreateRole(ctx, role))
	require.NoError(t, s.ReplaceConnectionTenants(ctx, conn, []string{"acme"}))
	require.NoError(t, s.sys(ctx).Create(&IdpRoleMapping{ConnectionID: conn.ID, TenantId: "acme", GroupValue: "ops", RoleID: role.ID}).Error)
	_, err := s.AddIdpDomain(ctx, conn.ID, "acme.example", "root")
	require.NoError(t, err)
	require.NoError(t, s.UpsertLinkRequest(ctx, &SsoLinkRequest{ConnectionID: conn.ID, Subject: "sub-2", Email: "q@acme.example"}))
	require.NoError(t, s.CreateIdentityLink(ctx, &IdentityLink{IdentityID: gone.ID, ConnectionID: other.ID, Subject: "sub-gone"}))
	require.NoError(t, s.CreateIdentityLink(ctx, &IdentityLink{IdentityID: gone.ID, ConnectionID: conn.ID, Subject: "sub-gone-2"}))

	// One pooled connection, so the pragma below is the one every statement runs under.
	sqlDB, err := s.sys(ctx).DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, s.sys(ctx).Exec(`PRAGMA foreign_keys = OFF`).Error)
	var fk int
	require.NoError(t, s.sys(ctx).Raw(`PRAGMA foreign_keys`).Scan(&fk).Error)
	require.Zero(t, fk, "this test is about the explicit deletes; the foreign keys must be off")

	require.NoError(t, s.DeleteIdpConnection(ctx, sealed, conn))
	for name, model := range map[string]any{
		"tenants": &IdpConnectionTenant{}, "domains": &IdpConnectionDomain{},
		"mappings": &IdpRoleMapping{}, "requests": &SsoLinkRequest{},
	} {
		require.Zerof(t, count(t, s, model), "%s must be deleted by the store itself", name)
	}
	require.EqualValues(t, 1, count(t, s, &IdentityLink{}), "the connection's link goes; the other connection's stays")

	loaded, err := s.IdentityByEmail(ctx, "gone@acme.example")
	require.NoError(t, err)
	require.NoError(t, s.DeleteIdentity(ctx, nil, loaded))
	require.Zero(t, count(t, s, &IdentityLink{}), "DeleteIdentity must remove the links itself")
}
