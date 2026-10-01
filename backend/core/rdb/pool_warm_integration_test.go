// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// A pool keeps its connections after a burst, asked of a real PostgreSQL server.
//
// pool_warm_test.go reads database/sql's own statistics through applyPoolSizing. That
// cannot see a CALLER that stops using the policy: a guest connection that opened its
// pool without applyPoolSizing would get database/sql's default of 2 idle connections and
// every unit test would stay green. So each leg below opens its pool the way a service
// does (an owned area through ExecuteInitialize, a guest through Connect) and identifies
// every connection by the server backend it reached. A reconnect is a NEW backend, which
// the server reports and a client-side counter cannot fake.
//
// Run it against a throwaway server:
//
//	DC_IT_PGPORT=$PORT go test -tags integration -count=1 -run Pool ./rdb/... -v
package rdb

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/go-gormigrate/gormigrate/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb/rdbtest"
)

// warmPoolSize is small on purpose: the rule under test is "unset idle is the open
// count", not the number 20, and the integration suite shares one server with other
// packages' tests.
const warmPoolSize = 6

// backendIdentityQuery names the server backend a connection reached, as its pid and
// start time: the pid alone is not an identity, because the OS can reuse it.
const backendIdentityQuery = `SELECT pid::text || '@' || backend_start::text FROM pg_stat_activity WHERE pid = pg_backend_pid()`

// burstBackends holds n connections of sqldb at once, reads the backend each reached, and
// releases them all.
func burstBackends(t *testing.T, sqldb *sql.DB, n int) map[string]bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conns := make([]*sql.Conn, n)
	seen := map[string]bool{}
	for i := range conns {
		c, err := sqldb.Conn(ctx)
		require.NoError(t, err, "acquiring connection %d of %d", i+1, n)
		conns[i] = c
		var id string
		require.NoError(t, c.QueryRowContext(ctx, backendIdentityQuery).Scan(&id))
		seen[id] = true
	}
	// The fixture: n connections held at once reach n distinct backends. Without it a
	// broken identity query returning one row for all of them would read as "no new
	// backends" below.
	require.Len(t, seen, n, "%d connections held at once did not reach %d distinct backends", n, n)
	for _, c := range conns {
		require.NoError(t, c.Close())
	}
	return seen
}

// newBackends is how many of the second burst's backends the first did not have.
func newBackends(first, second map[string]bool) int {
	n := 0
	for id := range second {
		if !first[id] {
			n++
		}
	}
	return n
}

type poolWarmWidget struct {
	ID uint `gorm:"primaryKey"`
}

func ownedPool(t *testing.T, instance string, cfg config.MicroserviceDatastoreConfiguration) *sql.DB {
	t.Helper()
	ctx := context.Background()
	host, port := itServer(t)
	require.NoError(t, rdbtest.EnsureDatabase(ctx, host, port, "postgres", "postgres", instance, ""))
	mgr := &RdbManager{
		Microservice:       &core.Microservice{InstanceId: instance, FunctionalArea: "pool-warm"},
		MicroserviceConfig: cfg,
		Migrations: []*gormigrate.Migration{{
			ID:      "20260930000000",
			Migrate: func(tx *gorm.DB) error { return tx.AutoMigrate(&poolWarmWidget{}) },
		}},
		InstanceConfig: config.DatastoreConfiguration{
			Type: "postgres",
			Configuration: map[string]interface{}{
				"hostname": host, "port": port, "username": "postgres", "password": "postgres",
				"sslMode": "disable",
			},
		},
	}
	require.NoError(t, mgr.ExecuteInitialize(ctx))
	sqldb, err := mgr.Database.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqldb.Close() })
	return sqldb
}

func TestAnOwnedPoolReusesItsConnectionsAfterABurst(t *testing.T) {
	sqldb := ownedPool(t, "rdbpoolwarm",
		config.MicroserviceDatastoreConfiguration{MaxOpenConnections: warmPoolSize})
	first := burstBackends(t, sqldb, warmPoolSize)
	second := burstBackends(t, sqldb, warmPoolSize)
	assert.Equal(t, 0, newBackends(first, second),
		"the second burst opened new backends, so the pool closed connections it was allowed to keep")
}

// The control: the same comparison detects reconnects when the pool is told to keep
// fewer. Exactly the connections over the idle count are new in the second burst.
func TestAnOwnedPoolThatKeepsFewerReconnects(t *testing.T) {
	sqldb := ownedPool(t, "rdbpoolwarmfewer",
		config.MicroserviceDatastoreConfiguration{MaxOpenConnections: warmPoolSize, MaxIdleConnections: warmPoolSize / 2})
	first := burstBackends(t, sqldb, warmPoolSize)
	second := burstBackends(t, sqldb, warmPoolSize)
	assert.Equal(t, warmPoolSize-warmPoolSize/2, newBackends(first, second))
}

// The guest connection (the telemetry purge's) opens its own pool, and must size it by
// the same policy.
func TestAGuestPoolReusesItsConnectionsAfterABurst(t *testing.T) {
	ctx := context.Background()
	host, port := itServer(t)
	g := NewGuest(
		&core.Microservice{InstanceId: "postgres", FunctionalArea: "user-management"},
		"tsdb",
		config.DatastoreConfiguration{Configuration: map[string]interface{}{
			"hostname": host, "port": port, "username": "postgres", "password": "postgres",
			"sslMode": "disable",
		}},
		config.MicroserviceDatastoreConfiguration{MaxOpenConnections: warmPoolSize},
	)
	require.NoError(t, g.Initialize(ctx))
	t.Cleanup(func() { _ = g.Close() })
	require.NoError(t, g.Connect(ctx))
	sqldb, err := g.DB(ctx).DB()
	require.NoError(t, err)
	require.Equal(t, warmPoolSize, sqldb.Stats().MaxOpenConnections, "the guest's open limit")

	first := burstBackends(t, sqldb, warmPoolSize)
	second := burstBackends(t, sqldb, warmPoolSize)
	assert.Equal(t, 0, newBackends(first, second),
		"the second burst opened new backends, so the guest pool closed connections it was allowed to keep")
}
