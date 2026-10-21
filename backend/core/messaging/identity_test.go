// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/kv"
	dctest "github.com/devicechain-io/dc-microservice/test"
	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"
)

// user-management's sign-in buckets live in the broker's IDENTITY account, reached over a second
// connection with a login only that service holds. These tests run the production connect path
// (ExecuteInitialize) against an embedded broker with the two accounts the infrastructure module
// renders.

const (
	idServicePassword  = "svc"
	idIdentityPassword = "idpw"
)

func startIdentityBroker(t *testing.T) *natsserver.Server {
	t.Helper()
	return startAccountsBroker(t, fmt.Sprintf(
		"IDENTITY: { jetstream: enabled, users: [ {user: dc_identity, password: %s} ] }", idIdentityPassword))
}

// startAccountsBroker starts an embedded broker with the APP account and the given extra accounts.
func startAccountsBroker(t *testing.T, extraAccounts string) *natsserver.Server {
	t.Helper()
	dir := dctest.JetStreamStoreDir(t)
	conf := filepath.Join(dir, "nats.conf")
	require.NoError(t, os.WriteFile(conf, []byte(fmt.Sprintf(`
listen: "127.0.0.1:-1"
server_name: "identity-test"
jetstream { store_dir: %q }
accounts {
  APP: { jetstream: enabled, users: [ {user: dc_service, password: %s} ] }
  %s
}
`, dir, idServicePassword, extraAccounts)), 0o600))
	opts, err := natsserver.ProcessConfigFile(conf)
	require.NoError(t, err)
	opts.NoLog, opts.NoSigs = true, true
	srv, err := natsserver.NewServer(opts)
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(20*time.Second), "embedded broker not ready")
	t.Cleanup(srv.Shutdown)
	return srv
}

// identityManager is a manager configured as a service of an instance whose broker has the
// identity account. password is what UseIdentityLogin is given; nil means the service never
// calls it (every service but user-management).
func identityManager(t *testing.T, srv *natsserver.Server, identityAccount bool, password *string) *NatsManager {
	t.Helper()
	nmgr := managerFor(t, srv)
	nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats.Auth.User = "dc_service"
	nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats.Auth.Password = idServicePassword
	nmgr.Microservice.InstanceConfiguration.Infrastructure.Nats.Auth.IdentityAccount = identityAccount
	if password != nil {
		nmgr.UseIdentityLogin(*password)
	}
	nmgr.connectWait = 3 * time.Second
	return nmgr
}

func asPassword(p string) *string { return &p }

// dialAccount connects as a login and returns its JetStream context.
func dialAccount(t *testing.T, srv *natsserver.Server, user, password string) nats.JetStreamContext {
	t.Helper()
	nc, err := nats.Connect(srv.ClientURL(), nats.UserInfo(user, password))
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := nc.JetStream()
	require.NoError(t, err)
	return js
}

func TestTheSignInBucketsLiveInTheIdentityAccountAndNowhereElse(t *testing.T) {
	srv := startIdentityBroker(t)
	nmgr := identityManager(t, srv, true, asPassword(idIdentityPassword))
	require.NoError(t, nmgr.ExecuteInitialize(t.Context()))
	t.Cleanup(func() { _ = nmgr.ExecuteTerminate(t.Context()) })

	refresh, err := nmgr.KeyValueStore(kv.BucketRefreshTokens, kv.BucketRefreshTokens, time.Minute)
	require.NoError(t, err)
	_, err = refresh.Put("jti-1", []byte("owner@example.com"))
	require.NoError(t, err)
	attempts, err := nmgr.CredentialAttemptStore()
	require.NoError(t, err)
	_, err = attempts.Put("hash", []byte("1"))
	require.NoError(t, err)
	// Not secret-bearing: the distributed lock stays where every area can reach it.
	_, err = nmgr.NewDistributedLock(time.Second)
	require.NoError(t, err)

	idJS := dialAccount(t, srv, "dc_identity", idIdentityPassword)
	appJS := dialAccount(t, srv, "dc_service", idServicePassword)

	_, err = idJS.KeyValue(kv.BucketRefreshTokens)
	require.NoError(t, err, "the refresh-token bucket must exist in the identity account")
	_, err = appJS.KeyValue(kv.BucketRefreshTokens)
	require.ErrorIs(t, err, nats.ErrBucketNotFound, "and must NOT exist in the shared account")
	_, err = appJS.KeyValue(CredentialAttemptsBucketName("test"))
	require.ErrorIs(t, err, nats.ErrBucketNotFound)
	_, err = idJS.KeyValue(CredentialAttemptsBucketName("test"))
	require.NoError(t, err)
	_, err = appJS.KeyValue(LockBucketName("test"))
	require.NoError(t, err, "the lock bucket carries no secret and stays in the shared account")
	_, err = idJS.KeyValue(LockBucketName("test"))
	require.ErrorIs(t, err, nats.ErrBucketNotFound)

	// The service reads and writes through the login: a token written on one handle is read back.
	entry, err := refresh.Get("jti-1")
	require.NoError(t, err)
	require.Equal(t, "owner@example.com", string(entry.Value()))

	// The replication sampler reads the identity account through its own context.
	nmgr.streamMu.Lock()
	tracked := append([]string(nil), nmgr.identityBucketNames...)
	appTracked := append([]string(nil), nmgr.bucketNames...)
	nmgr.streamMu.Unlock()
	require.Contains(t, tracked, kvStreamPrefix+kv.BucketRefreshTokens)
	require.NotContains(t, appTracked, kvStreamPrefix+kv.BucketRefreshTokens)
}

func TestAServiceWithoutTheIdentityLoginCannotOpenTheSignInBuckets(t *testing.T) {
	srv := startIdentityBroker(t)
	other := identityManager(t, srv, true, nil) // every service but user-management
	require.NoError(t, other.ExecuteInitialize(t.Context()))
	t.Cleanup(func() { _ = other.ExecuteTerminate(t.Context()) })

	for _, logical := range []string{kv.BucketRefreshTokens, kv.BucketOAuthCodes, kv.BucketSsoState, kv.BucketSsoCodes} {
		_, err := other.KeyValueStore(logical, logical, time.Minute)
		require.ErrorIs(t, err, ErrIdentityNotAvailable, logical)
	}
	_, err := other.CredentialAttemptStore()
	require.ErrorIs(t, err, ErrIdentityNotAvailable)

	// And nothing was created in the shared account as a fallback.
	appJS := dialAccount(t, srv, "dc_service", idServicePassword)
	for _, name := range IdentityBucketNames("test") {
		_, err := appJS.KeyValue(name)
		require.ErrorIs(t, err, nats.ErrBucketNotFound, name)
	}
}

func TestUserManagementRefusesToStartWithoutItsLoginOrAccount(t *testing.T) {
	srv := startIdentityBroker(t)

	t.Run("an empty password is a refusal, not a fallback to the shared account", func(t *testing.T) {
		nmgr := identityManager(t, srv, true, asPassword(""))
		err := nmgr.ExecuteInitialize(t.Context())
		require.ErrorIs(t, err, ErrIdentityLoginMissing)
		require.Nil(t, nmgr.nc, "a failed initialize must not leave a half-built manager")
	})

	t.Run("a wrong password is a refusal naming the account", func(t *testing.T) {
		nmgr := identityManager(t, srv, true, asPassword("not-the-password"))
		err := nmgr.ExecuteInitialize(t.Context())
		require.Error(t, err)
		require.ErrorIs(t, err, ErrBrokerUnreachable)
		require.Contains(t, err.Error(), "identity account")
	})

	t.Run("an account the broker does not have is a refusal", func(t *testing.T) {
		// A broker built before the account existed: the login is unknown to it.
		legacy := startAccountsBroker(t, "")
		nmgr := identityManager(t, legacy, true, asPassword(idIdentityPassword))
		err := nmgr.ExecuteInitialize(t.Context())
		require.ErrorIs(t, err, ErrBrokerUnreachable)
		require.Contains(t, err.Error(), "identity account")
	})

	t.Run("an account with the login but no JetStream is a refusal at connect", func(t *testing.T) {
		// The login authenticates, so only the JetStream probe can catch this at start; without it the
		// service would come up and fail later, on the first bucket, with an error that names neither.
		noJS := startAccountsBroker(t, fmt.Sprintf(
			"IDENTITY: { users: [ {user: dc_identity, password: %s} ] }", idIdentityPassword))
		nmgr := identityManager(t, noJS, true, asPassword(idIdentityPassword))
		err := nmgr.ExecuteInitialize(t.Context())
		require.Error(t, err)
		require.Contains(t, err.Error(), "has no usable JetStream")
		require.Nil(t, nmgr.nc, "a failed initialize must not leave a half-built manager")
	})
}

// The instance without the account (no broker authentication, or a broker built before it) keeps
// every bucket where it always was.
func TestWithoutTheIdentityAccountTheBucketsStayInTheSharedAccount(t *testing.T) {
	srv := startIdentityBroker(t)
	nmgr := identityManager(t, srv, false, asPassword(idIdentityPassword))
	require.NoError(t, nmgr.ExecuteInitialize(t.Context()))
	t.Cleanup(func() { _ = nmgr.ExecuteTerminate(t.Context()) })
	require.Nil(t, nmgr.identity)

	_, err := nmgr.KeyValueStore(kv.BucketRefreshTokens, kv.BucketRefreshTokens, time.Minute)
	require.NoError(t, err)
	_, err = dialAccount(t, srv, "dc_service", idServicePassword).KeyValue(kv.BucketRefreshTokens)
	require.NoError(t, err)

	retired, err := nmgr.RetireSharedIdentityBuckets()
	require.NoError(t, err)
	require.Empty(t, retired, "with no identity connection the shared buckets are the live ones and must never be deleted")
	_, err = dialAccount(t, srv, "dc_service", idServicePassword).KeyValue(kv.BucketRefreshTokens)
	require.NoError(t, err)
}

// An upgrade leaves the previous release's buckets, and their tokens, in the shared account. They
// are deleted, not copied: this is the documented invalidation of every session.
func TestUpgradeDeletesTheSharedAccountCopiesAndInvalidatesTheirSessions(t *testing.T) {
	srv := startIdentityBroker(t)
	appJS := dialAccount(t, srv, "dc_service", idServicePassword)
	// What the previous release created and filled, under the names it used.
	for _, name := range IdentityBucketNames("test") {
		bucket, err := appJS.CreateKeyValue(&nats.KeyValueConfig{Bucket: name, TTL: time.Hour})
		require.NoError(t, err)
		_, err = bucket.Put("jti-old", []byte("owner@example.com"))
		require.NoError(t, err)
	}
	// ...and a bucket that is NOT an identity bucket, which must survive.
	keep, err := appJS.CreateKeyValue(&nats.KeyValueConfig{Bucket: LockBucketName("test")})
	require.NoError(t, err)

	nmgr := identityManager(t, srv, true, asPassword(idIdentityPassword))
	require.NoError(t, nmgr.ExecuteInitialize(t.Context()))
	t.Cleanup(func() { _ = nmgr.ExecuteTerminate(t.Context()) })

	refresh, err := nmgr.KeyValueStore(kv.BucketRefreshTokens, kv.BucketRefreshTokens, time.Hour)
	require.NoError(t, err)
	_, err = refresh.Get("jti-old")
	require.ErrorIs(t, err, nats.ErrKeyNotFound, "the old token must not exist in the new account")

	retired, err := nmgr.RetireSharedIdentityBuckets()
	require.NoError(t, err)
	require.ElementsMatch(t, IdentityBucketNames("test"), retired)
	for _, name := range IdentityBucketNames("test") {
		_, err := appJS.KeyValue(name)
		require.ErrorIs(t, err, nats.ErrBucketNotFound, "%s must be gone from the shared account", name)
	}
	_, err = appJS.KeyValue(keep.Bucket())
	require.NoError(t, err, "only the identity buckets are retired")

	again, err := nmgr.RetireSharedIdentityBuckets()
	require.NoError(t, err)
	require.Empty(t, again, "retiring is idempotent")
}
