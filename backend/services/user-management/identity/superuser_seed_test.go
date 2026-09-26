// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/credential"
	"github.com/devicechain-io/dc-microservice/credential/credentialtest"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-microservice/secrets"
	dctest "github.com/devicechain-io/dc-microservice/test"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/devicechain-io/dc-user-management/model"
	"github.com/glebarez/sqlite"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// seedFixture is what Manager.Initialize needs to run its real seed: an embedded
// JetStream broker for the distributed lock seeding runs under, an identity store, and
// the instance secret store the signing key's private half is sealed in (Initialize
// loads or creates that key before it seeds).
type seedFixture struct {
	ms     *core.Microservice
	lock   *messaging.DistributedLock
	rdbm   *rdb.RdbManager
	secret secrets.SecretStore
	// creds is the credential checker Initialize requires; the seed never compares a
	// secret, so an in-memory attempt store under the service's own policies is enough.
	creds *credential.Checker
}

func newSeedFixture(t *testing.T) *seedFixture {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{
		Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: dctest.JetStreamStoreDir(t),
	})
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(5*time.Second))
	t.Cleanup(srv.Shutdown)

	u, err := url.Parse(srv.ClientURL())
	require.NoError(t, err)
	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)
	ms := &core.Microservice{InstanceId: "seedtest", FunctionalArea: "user-management", Readiness: core.NewReadinessGate()}
	ms.InstanceConfiguration.Infrastructure.Nats.Hostname = u.Hostname()
	ms.InstanceConfiguration.Infrastructure.Nats.Port = uint32(port)
	ms.Readiness.MarkReadyWithoutAuthSurface()
	nmgr := &messaging.NatsManager{Microservice: ms}
	require.NoError(t, nmgr.ExecuteInitialize(context.Background()))
	t.Cleanup(func() {
		if c := nmgr.Conn(); c != nil {
			c.Close()
		}
	})
	lock, err := nmgr.NewDistributedLock(5 * time.Second)
	require.NoError(t, err)

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, rdb.RegisterTenantScoping(db))
	require.NoError(t, rdb.RegisterTokenGrammar(db))
	require.NoError(t, db.AutoMigrate(
		&iam.TenantTier{}, &iam.Tenant{}, &iam.Role{}, &iam.Identity{}, &iam.Membership{},
		&iam.OAuthClient{}, &rdb.AuditEvent{}, &model.SigningKey{},
	))
	require.NoError(t, secrets.NewSecretStoreSchema().Migrate(db))
	store, err := secrets.NewFromConfig(context.Background(), rootKeyConfig(t), db)
	require.NoError(t, err)
	creds, err := credential.NewChecker(credentialtest.NewStore(), CredentialPolicies)
	require.NoError(t, err)
	return &seedFixture{ms: ms, lock: lock, rdbm: &rdb.RdbManager{Database: db}, secret: store, creds: creds}
}

func (f *seedFixture) manager(password string) *Manager {
	return NewManager(f.ms, f.rdbm, f.lock, f.secret, time.Minute, time.Hour, "", BootstrapConfig{
		SuperuserEmail:    "superuser@devicechain.local",
		SuperuserPassword: password,
	}, f.creds)
}

func (f *seedFixture) identities(t *testing.T) int64 {
	t.Helper()
	n, err := iam.NewStore(f.rdbm).CountIdentities(context.Background())
	require.NoError(t, err)
	return n
}

// 🔴 AN EMPTY IDENTITY TABLE WITH NO SEED PASSWORD IS A REFUSAL, NOT A SUPERUSER. This is
// the defect the change closes: the config defaults used to fill the password with a
// literal, so the seed always ran and the instance's `*` superuser had the same
// password on every installation. Asserted on the store as well as the error, so a
// seed that errored AFTER writing the row would not pass.
//
// Whitespace-only counts as none: a hand-made Secret holding a trailing newline and
// nothing else is no more a password than an absent one.
func TestAnEmptyIdentityTableIsNotSeededWithoutAPassword(t *testing.T) {
	for _, blank := range []string{"", " \n\t"} {
		f := newSeedFixture(t)
		err := f.manager(blank).Initialize(context.Background(), nil, nil)

		require.ErrorIs(t, err, ErrNoSuperuserSeedPassword, "seed password %q", blank)
		require.Equal(t, int64(0), f.identities(t), "no identity may be written when the seed is refused")
	}
}

// With a password supplied, the superuser is seeded from EXACTLY that value: it signs in
// with it, holds authority `*`, and the literal every earlier release published is
// rejected.
func TestTheSuperuserIsSeededFromTheSuppliedPassword(t *testing.T) {
	f := newSeedFixture(t)
	m := f.manager("minted-by-dcctl")
	ctx := context.Background()
	require.NoError(t, m.Initialize(ctx, nil, nil))
	require.Equal(t, int64(1), f.identities(t))

	res, err := m.Login(ctx, "superuser@devicechain.local", "minted-by-dcctl")
	require.NoError(t, err)
	require.True(t, res.Superuser)
	claims, err := m.validator.ValidateIdentity(res.IdentityToken)
	require.NoError(t, err)
	require.Equal(t, []string{"*"}, claims.Authorities)

	_, err = m.Login(ctx, "superuser@devicechain.local", "devicechain")
	require.True(t, errors.Is(err, ErrInvalidCredentials), "the old published password must not sign in: %v", err)
}

// An instance whose superuser already exists starts WITHOUT a seed password — an
// instance seeded before dcctl generated one has no Secret to project it from — and its
// superuser is left exactly as it was: still signing in with its own password.
func TestAnExistingSuperuserNeedsNoSeedPassword(t *testing.T) {
	f := newSeedFixture(t)
	ctx := context.Background()
	require.NoError(t, f.manager("the-first-one").Initialize(ctx, nil, nil))

	restarted := f.manager("")
	require.NoError(t, restarted.Initialize(ctx, nil, nil), "a restart over a seeded table must not need the seed password")
	require.Equal(t, int64(1), f.identities(t))
	_, err := restarted.Login(ctx, "superuser@devicechain.local", "the-first-one")
	require.NoError(t, err, "the existing superuser's password must be untouched")

	// ...and a DIFFERENT seed password on a later start changes nothing either: the seed
	// is a first-start input, not a way to reset the superuser.
	again := f.manager("a-different-one")
	require.NoError(t, again.Initialize(ctx, nil, nil))
	_, err = again.Login(ctx, "superuser@devicechain.local", "a-different-one")
	require.True(t, errors.Is(err, ErrInvalidCredentials), "a later seed password must not reach an existing superuser: %v", err)
}
