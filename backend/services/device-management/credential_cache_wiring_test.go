// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/devicechain-io/dc-device-management/config"
	"github.com/devicechain-io/dc-device-management/model"
	dmtest "github.com/devicechain-io/dc-device-management/test"
	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	dcgraphql "github.com/devicechain-io/dc-microservice/graphql"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/rdb"
	putest "github.com/devicechain-io/dc-microservice/rdb/partialupdatetest"
)

// 🔑 THE SERVICE'S CREDENTIAL CHECK, AS THE SERVICE ASSEMBLES IT. A credentialed event used
// to cost one database statement every time, which a profile of a loaded replica found to
// be most of its CPU. The model tests prove the cache's rules; these prove the service is
// built with it, and built so that a revocation reaches it.

// credentialTables is what a credential check reads, plus what seeding a device needs.
func credentialTables() []any {
	return []any{&model.Device{}, &model.DeviceType{}, &model.DeviceProfile{}, &model.DeviceProfileVersion{},
		&model.MetricDefinition{}, &model.CommandDefinition{}, &model.DetectionRule{},
		&model.DetectionRuleScopeRef{}, &model.DeviceCredential{}}
}

// credentialStatements counts the query statements that read device_credentials on db.
type credentialStatements struct{ n atomic.Int64 }

var credentialStatementsSeq atomic.Int64

func countCredentialStatements(t *testing.T, db *gorm.DB) *credentialStatements {
	t.Helper()
	c := &credentialStatements{}
	name := fmt.Sprintf("test:count-credential-statements-%d", credentialStatementsSeq.Add(1))
	require.NoError(t, db.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
		if strings.Contains(tx.Statement.SQL.String(), "device_credentials") {
			c.n.Add(1)
		}
	}))
	return c
}

// startNatsManager connects a NatsManager to the broker at host:port as one replica of an
// instance, with a metrics registry of its own.
func startNatsManager(t *testing.T, host string, port uint32, instanceId string) *messaging.NatsManager {
	t.Helper()
	ms := &core.Microservice{InstanceId: instanceId, FunctionalArea: "device-management"}
	ms.UseMetricsRegistry(prometheus.NewRegistry())
	ms.InstanceConfiguration.Infrastructure.Nats = mscfg.NatsConfiguration{Hostname: host, Port: port}
	nmgr := messaging.NewNatsManager(ms, core.NewNoOpLifecycleCallbacks(),
		func(*messaging.NatsManager) error { return nil })
	nmgr.RecordMaxDeliveries(func(*messaging.NatsManager) (messaging.MaxDeliveryFunc, error) {
		return func(context.Context, messaging.MaxDelivery) (messaging.MaxDeliveryOutcome, error) {
			return messaging.MaxDeliveryLettered, nil
		}, nil
	})
	require.NoError(t, nmgr.Initialize(context.Background()))
	require.NoError(t, nmgr.Start(context.Background()))
	t.Cleanup(func() { _ = nmgr.Stop(context.Background()) })
	return nmgr
}

// seedBasicCredential seeds device "dev" of type "dt" in tenant acme with the MQTT_BASIC
// credential cred-1 / s3cret, token c-1.
func seedBasicCredential(t *testing.T, api *model.Api) context.Context {
	t.Helper()
	ctx := core.WithTenant(context.Background(), "acme")
	_, err := api.CreateDeviceType(ctx, &model.DeviceTypeCreateRequest{Token: "dt"})
	require.NoError(t, err)
	_, err = api.CreateDevice(ctx, &model.DeviceCreateRequest{Token: "dev", DeviceTypeToken: "dt"})
	require.NoError(t, err)
	secret := "s3cret"
	_, err = api.CreateDeviceCredential(ctx, &model.DeviceCredentialCreateRequest{
		Token: "c-1", DeviceToken: "dev", CredentialType: string(model.CredentialMqttBasic),
		CredentialId: "cred-1", CredentialValue: &secret, Enabled: true,
	})
	require.NoError(t, err)
	return ctx
}

func basicCred() *model.PresentedCredential {
	secret := "s3cret"
	return &model.PresentedCredential{CredentialType: string(model.CredentialMqttBasic),
		CredentialId: "cred-1", Secret: &secret}
}

func disable(t *testing.T, ctx context.Context, api *model.Api) {
	t.Helper()
	_, err := api.UpdateDeviceCredential(ctx, "c-1",
		&model.DeviceCredentialUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(false)})
	require.NoError(t, err)
}

// A credential checked twice within 5 s is read from the database once. It is assembled
// from the same pieces the service assembles (InitializeCaches over a real broker, the
// cached decorator over the plain Api, the evictor wired), so a service that stopped
// building its credential cache fails here by the statement count.
//
// The disable at the end is the guard: whatever the count says, a revoked credential must
// be refused on the next check.
func TestTheServiceAnswersARepeatedCredentialCheckFromMemory(t *testing.T) {
	host, port := startEmbeddedNats(t)
	nmgr := startNatsManager(t, host, port, fmt.Sprintf("credcache%d", time.Now().UnixNano()))
	caches, err := model.InitializeCaches(nmgr, config.NewDeviceManagementConfiguration())
	require.NoError(t, err)

	db := putest.NewSQLiteDB(t, credentialTables()...)
	api := model.NewApi(&rdb.RdbManager{Database: db})
	api.DeviceSecretKey = dmtest.DeviceSecretKey()
	capi := model.NewCachedApi(api, caches)
	api.CacheEvictor = capi
	ctx := seedBasicCredential(t, api)
	stmts := countCredentialStatements(t, db)
	now := time.Now()

	// Negative control first: the counter sees the first check's read, so a 0 below is the
	// cache and not a counter that counts nothing.
	d, err := capi.AuthenticateDevice(ctx, basicCred(), now)
	require.NoError(t, err)
	require.Equal(t, "dev", d.Token)
	require.GreaterOrEqual(t, stmts.n.Load(), int64(1), "the first check read no credential statement")

	stmts.n.Store(0)
	d, err = capi.AuthenticateDevice(ctx, basicCred(), now)
	require.NoError(t, err)
	require.Equal(t, "dev", d.Token)
	if got := stmts.n.Load(); got != 0 {
		t.Fatalf("a second check of the same credential within 5s ran %d statements, want 0", got)
	}

	disable(t, ctx, api)
	_, err = capi.AuthenticateDevice(ctx, basicCred(), now)
	require.ErrorIs(t, err, model.ErrCredentialNotResolved, "a disabled credential authenticated on the next check")
}

// replica is one device-management replica's Apis, built by buildApis, as the service
// builds them.
type replica struct {
	api  *model.Api
	capi *model.CachedApi
}

func newReplica(t *testing.T, host string, port uint32, instanceId string, db *gorm.DB) replica {
	t.Helper()
	nmgr := startNatsManager(t, host, port, instanceId)
	api, capi, err := buildApis(nmgr, &rdb.RdbManager{Database: db}, dmtest.DeviceSecretKey(), config.NewDeviceManagementConfiguration())
	require.NoError(t, err)
	return replica{api: api, capi: capi}
}

// A revocation made through one replica is seen by every other replica well inside the
// 5 s a copy lives, so only the eviction broadcast can explain it; and on the replica that
// made it, on the very next check. Every replica is built by buildApis, so a service that
// stopped wiring its evictor (main.go) fails here: nothing would be evicted anywhere.
//
// The control is a third replica on a broker of its own, sharing the database. It is the
// same code with the broadcast path cut, and it must STILL authenticate after the
// revocation: that shows the test can see a missing broadcast, and that nothing but the
// broadcast (not the TTL, not a re-read) refuses the credential on the other replica.
func TestARevocationThroughOneReplicaReachesTheOthersBeforeTheTTL(t *testing.T) {
	host, port := startEmbeddedNats(t)
	isolatedHost, isolatedPort := startEmbeddedNats(t)
	instance := fmt.Sprintf("credcachexr%d", time.Now().UnixNano())

	db := putest.NewSQLiteDB(t, credentialTables()...)
	a := newReplica(t, host, port, instance, db)
	b := newReplica(t, host, port, instance, db)
	isolated := newReplica(t, isolatedHost, isolatedPort, instance, db)
	ctx := seedBasicCredential(t, b.api)
	stmts := countCredentialStatements(t, db)
	now := time.Now()

	// Warm every replica, and prove each is answering from memory before the revocation:
	// without that, a refusal below would only show a database read.
	for name, r := range map[string]replica{"a": a, "b": b, "isolated": isolated} {
		_, err := r.capi.AuthenticateDevice(ctx, basicCred(), now)
		require.NoError(t, err, "warm %s", name)
		stmts.n.Store(0)
		_, err = r.capi.AuthenticateDevice(ctx, basicCred(), now)
		require.NoError(t, err, "second check on %s", name)
		require.Zero(t, stmts.n.Load(), "replica %s's second check read the database: it holds no copy", name)
	}

	revokedAt := time.Now()
	disable(t, ctx, b.api)

	// The replica that made the change: refused on the very next check.
	_, err := b.capi.AuthenticateDevice(ctx, basicCred(), now)
	require.ErrorIs(t, err, model.ErrCredentialNotResolved, "the replica that revoked still authenticated")

	// Another replica: refused within 1 s, far inside the 5 s its copy would otherwise live.
	deadline := revokedAt.Add(time.Second)
	for {
		_, err = a.capi.AuthenticateDevice(ctx, basicCred(), now)
		if errors.Is(err, model.ErrCredentialNotResolved) {
			break
		}
		require.NoError(t, err)
		if time.Now().After(deadline) {
			t.Fatalf("replica a still authenticated a credential revoked through b %v ago", time.Since(revokedAt))
		}
		time.Sleep(10 * time.Millisecond)
	}

	// The control: no broadcast reaches the isolated replica, and its copy still answers
	// 500 ms after the revocation.
	time.Sleep(time.Until(revokedAt.Add(500 * time.Millisecond)))
	d, err := isolated.capi.AuthenticateDevice(ctx, basicCred(), now)
	require.NoError(t, err, "the control replica refused without a broadcast: the test cannot tell the broadcast from something else")
	require.Equal(t, "dev", d.Token)
}

// 🔴 A CONNECT IS NEVER ANSWERED FROM MEMORY. The broker grants a connect a session that
// lasts hours, so a connect answered from a copy a revocation had not yet reached would
// keep the revoked token's session, and its command subscription, long after the copy
// expired. The callout checks an access token through AuthenticateDeviceConnect; on the
// cached Api the service hands the callout, that must read the database every time.
func TestAnAccessTokenConnectReadsTheDatabaseEveryTime(t *testing.T) {
	host, port := startEmbeddedNats(t)
	db := putest.NewSQLiteDB(t, credentialTables()...)
	r := newReplica(t, host, port, fmt.Sprintf("credcacheconn%d", time.Now().UnixNano()), db)
	ctx := seedBasicCredential(t, r.api)
	_, err := r.api.CreateDeviceCredential(ctx, &model.DeviceCredentialCreateRequest{
		Token: "c-2", DeviceToken: "dev", CredentialType: string(model.CredentialAccessToken),
		CredentialId: "tok-1", Enabled: true,
	})
	require.NoError(t, err)
	token := &model.PresentedCredential{CredentialType: string(model.CredentialAccessToken), CredentialId: "tok-1"}
	stmts := countCredentialStatements(t, db)
	now := time.Now()

	// The per-event check of the same token IS cached: the connect's reads below are the
	// connect path's own, not a cache that is simply not working.
	_, err = r.capi.AuthenticateDevice(ctx, token, now)
	require.NoError(t, err)
	stmts.n.Store(0)
	_, err = r.capi.AuthenticateDevice(ctx, token, now)
	require.NoError(t, err)
	require.Zero(t, stmts.n.Load(), "the per-event check is not answered from memory")

	for i := 1; i <= 2; i++ {
		stmts.n.Store(0)
		d, err := r.capi.AuthenticateDeviceConnect(ctx, token, now)
		require.NoError(t, err)
		require.Equal(t, "dev", d.Token)
		require.Equal(t, int64(1), stmts.n.Load(), "connect %d read the database %d times, want 1", i, stmts.n.Load())
	}

	// And a revocation that evicts nothing (evictor detached) is still refused at connect.
	r.api.CacheEvictor = nil
	_, err = r.api.UpdateDeviceCredential(ctx, "c-2",
		&model.DeviceCredentialUpdateRequest{Enabled: dcgraphql.OptionalBoolOf(false)})
	require.NoError(t, err)
	_, err = r.capi.AuthenticateDeviceConnect(ctx, token, now)
	require.ErrorIs(t, err, model.ErrCredentialNotResolved)
}
