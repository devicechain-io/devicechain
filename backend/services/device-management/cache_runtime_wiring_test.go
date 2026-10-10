// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-device-management/config"
	"github.com/devicechain-io/dc-device-management/model"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/rdb"
	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
	dctest "github.com/devicechain-io/dc-microservice/test"
)

// restartableBroker is one JetStream server whose port and store survive a restart, so a
// client of it reconnects to the same address and finds the same buckets.
type restartableBroker struct {
	t    *testing.T
	dir  string
	port int
	mu   sync.Mutex
	srv  *natsserver.Server
}

func newRestartableBroker(t *testing.T) *restartableBroker {
	t.Helper()
	b := &restartableBroker{t: t, dir: dctest.JetStreamStoreDir(t)}
	b.start(-1)
	t.Cleanup(b.stop)
	u, err := url.Parse(b.srv.ClientURL())
	require.NoError(t, err)
	b.port, err = strconv.Atoi(u.Port())
	require.NoError(t, err)
	return b
}

func (b *restartableBroker) start(port int) {
	srv, err := natsserver.NewServer(&natsserver.Options{
		Host: "127.0.0.1", Port: port, JetStream: true, StoreDir: b.dir, NoLog: true, NoSigs: true,
	})
	require.NoError(b.t, err)
	go srv.Start()
	require.True(b.t, srv.ReadyForConnections(10*time.Second), "the broker never became ready")
	b.mu.Lock()
	b.srv = srv
	b.mu.Unlock()
}

func (b *restartableBroker) stop() {
	b.mu.Lock()
	srv := b.srv
	b.mu.Unlock()
	if srv != nil {
		srv.Shutdown()
		srv.WaitForShutdown()
	}
}

// restart stops the server and starts another on the same port over the same store.
func (b *restartableBroker) restart() {
	b.stop()
	b.start(b.port)
}

// 🔴 A REPLICA THAT LOSES ITS BROKER AND GETS IT BACK HAS MISSED EVICTION MESSAGES, AND MUST
// TRUST NOTHING IT HOLDS. With the time to live in minutes, a replica that kept its copies
// across a reconnect would serve a revoked credential, or a changed device, for as long as the
// time. This restarts the one broker a replica built by buildApis is connected to, changes
// both a bucket entry and a credential behind its back, with no message for it to hear, and
// requires that it stops serving what it held once it reconnects.
func TestAReplicaThatReconnectsToItsBrokerDropsWhatItHeld(t *testing.T) {
	broker := newRestartableBroker(t)
	instance := fmt.Sprintf("reconnect%d", time.Now().UnixNano())
	db := putest.NewSQLiteDB(t, evictionTables()...)
	r := newReplica(t, "127.0.0.1", uint32(broker.port), instance, db)
	ctx := seedBasicCredential(t, r.api)
	stmts := countCredentialStatements(t, db)
	now := time.Now()
	caches := r.capi.Caches()

	// Warm both, and prove each is answered from memory.
	require.NoError(t, caches.DeviceByToken.Set(ctx, "acme|k", "v"))
	var got string
	found, err := caches.DeviceByToken.Get(ctx, "acme|k", &got)
	require.NoError(t, err)
	require.True(t, found && got == "v")
	_, err = r.capi.AuthenticateDevice(ctx, basicCred(), now)
	require.NoError(t, err)
	stmts.n.Store(0)
	_, err = r.capi.AuthenticateDevice(ctx, basicCred(), now)
	require.NoError(t, err)
	require.Zero(t, stmts.n.Load(), "the credential is not held in memory, so the test proves nothing")

	broker.restart()
	require.Eventually(t, func() bool { return r.nmgr.Conn().IsConnected() }, 20*time.Second, 50*time.Millisecond,
		"the replica never reconnected")

	// Behind its back: a bucket write from another replica (a Set sends no message), and a
	// revocation with no evictor attached (so no broadcast at all).
	other := newReplica(t, "127.0.0.1", uint32(broker.port), instance, db)
	require.NoError(t, other.capi.Caches().DeviceByToken.Set(ctx, "acme|k", "changed"))
	other.api.CacheEvictor = nil
	disable(t, ctx, other.api)

	require.Eventually(t, func() bool {
		var v string
		found, err := caches.DeviceByToken.Get(ctx, "acme|k", &v)
		return err == nil && found && v == "changed"
	}, 10*time.Second, 50*time.Millisecond, "the reconnected replica still serves its pre-restart bucket entry from memory")
	require.Eventually(t, func() bool {
		_, err := r.capi.AuthenticateDevice(ctx, basicCred(), now)
		return err == model.ErrCredentialNotResolved
	}, 10*time.Second, 50*time.Millisecond, "the reconnected replica still authenticates a revoked credential from memory")
}

// 🔑 THE PERFORMANCE FIX ITSELF, THROUGH THE SERVICE'S OWN ASSEMBLY. Every cache must keep what
// it read for the configured time, which is minutes (the 5 s it replaces expired between two
// reports of a device that reports every 10 s), and the credential cache must be as large as
// configured. The caches are built by buildApis, as the service builds them.
func TestTheServiceBuildsItsCachesWithTheConfiguredTimesAndSizes(t *testing.T) {
	host, port := startEmbeddedNats(t)
	db := putest.NewSQLiteDB(t, evictionTables()...)

	check := func(t *testing.T, cfg *config.DeviceManagementConfiguration, want map[string]time.Duration, credTTL time.Duration, credBytes int) {
		t.Helper()
		nmgr := startNatsManager(t, host, port, fmt.Sprintf("cfgwiring%d", time.Now().UnixNano()))
		_, capi, err := buildApis(nmgr, &rdb.RdbManager{Database: db}, cfg)
		require.NoError(t, err)
		c := capi.Caches()
		for name, cache := range map[string]interface{ LocalTTL() time.Duration }{
			model.CACHE_NAME_DEVICE_BY_TOKEN:            c.DeviceByToken,
			model.CACHE_NAME_RELATIONSHIPS_BY_SOURCE:    c.RelationshipsBySource,
			model.CACHE_NAME_PROFILE_RESOLUTION_BY_TYPE: c.ProfileResolutionByType,
			model.CACHE_NAME_MEMBERSHIPS_BY_ENTITY:      c.MembershipsByEntity,
			model.CACHE_NAME_SCOPED_GROUPS_EXIST:        c.ScopedGroupsExist,
		} {
			require.Equal(t, want[name], cache.LocalTTL(), "%s keeps what it read for", name)
		}
		require.Equal(t, credTTL, c.Credentials.TTL(), "the credential cache's time")
		require.Equal(t, credBytes, c.Credentials.MaxBytes(), "the credential cache's byte bound")
	}

	t.Run("defaults", func(t *testing.T) {
		five := 5 * time.Minute
		check(t, config.NewDeviceManagementConfiguration(), map[string]time.Duration{
			model.CACHE_NAME_DEVICE_BY_TOKEN: five, model.CACHE_NAME_RELATIONSHIPS_BY_SOURCE: five,
			model.CACHE_NAME_PROFILE_RESOLUTION_BY_TYPE: five, model.CACHE_NAME_MEMBERSHIPS_BY_ENTITY: five,
			model.CACHE_NAME_SCOPED_GROUPS_EXIST: five,
		}, five, 32<<20)
		require.Greater(t, five, 5*time.Second, "the point of the change: a device reporting every 10 s is answered from memory")
	})

	t.Run("configured", func(t *testing.T) {
		cfg := config.NewDeviceManagementConfiguration()
		cfg.InMemoryCache.TtlSeconds = 90
		cfg.InMemoryCache.CredentialCacheMiB = 48
		cfg.DeviceCacheTtlSeconds = 30 // a bucket's own time still caps its in-memory copy
		check(t, cfg, map[string]time.Duration{
			model.CACHE_NAME_DEVICE_BY_TOKEN: 30 * time.Second, model.CACHE_NAME_RELATIONSHIPS_BY_SOURCE: 90 * time.Second,
			model.CACHE_NAME_PROFILE_RESOLUTION_BY_TYPE: 90 * time.Second, model.CACHE_NAME_MEMBERSHIPS_BY_ENTITY: 90 * time.Second,
			model.CACHE_NAME_SCOPED_GROUPS_EXIST: 90 * time.Second,
		}, 90*time.Second, 48<<20)
	})
}

// Updating a relationship type evicts the cached set of EVERY device with an edge of that type.
// It does so with bounded concurrency and one broadcast, not one bucket round trip after another
// inside the mutation, so a type with tens of thousands of edges finishes in seconds. Here 60
// devices' sets, all warm on another replica, are all dropped by one update.
func TestUpdatingARelationshipTypeEvictsEveryDevicesSetOnOtherReplicas(t *testing.T) {
	r := newEvictionRig(t)
	_, err := r.a.api.CreateDeviceType(r.ctx, &model.DeviceTypeCreateRequest{Token: "dt"})
	require.NoError(t, err)
	_, err = r.a.api.CreateEntityRelationshipType(r.ctx, &model.EntityRelationshipTypeCreateRequest{Token: "watches", Tracked: true})
	require.NoError(t, err)
	_, err = r.a.api.CreateDevice(r.ctx, &model.DeviceCreateRequest{Token: "target", DeviceTypeToken: "dt"})
	require.NoError(t, err)
	var ids []uint
	var edges []*model.EntityRelationshipCreateRequest
	for i := 0; i < 60; i++ {
		tok := fmt.Sprintf("d%d", i)
		d, err := r.a.api.CreateDevice(r.ctx, &model.DeviceCreateRequest{Token: tok, DeviceTypeToken: "dt"})
		require.NoError(t, err)
		ids = append(ids, d.ID)
		edges = append(edges, &model.EntityRelationshipCreateRequest{Token: "e-" + tok, SourceType: "device", Source: tok,
			TargetType: "device", Target: "target", RelationshipType: "watches"})
	}
	_, err = r.a.api.CreateEntityRelationships(r.ctx, edges)
	require.NoError(t, err)
	for _, id := range ids {
		require.Equal(t, 1, trackedCount(t, r.ctx, r.b, id), "warm")
	}

	started := time.Now()
	_, err = r.a.api.UpdateEntityRelationshipType(r.ctx, "watches", &model.EntityRelationshipTypeUpdateRequest{
		Tracked: dcgraphql.OptionalBoolOf(false)})
	require.NoError(t, err)
	require.Less(t, time.Since(started), 5*time.Second)

	for _, id := range ids {
		id := id
		eventually(t, fmt.Sprintf("device %d's set was not evicted on b", id), func() bool {
			return trackedCount(t, r.ctx, r.b, id) == 0
		})
	}
}
