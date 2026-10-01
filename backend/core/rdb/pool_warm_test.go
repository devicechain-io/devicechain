// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"context"
	"database/sql"
	"reflect"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/devicechain-io/dc-microservice/config"
)

// burstPool opens a pool through applyPoolSizing, the function both an owned and a guest
// connection are sized by, then twice holds n connections at once, releases them all, and
// returns the pool.
//
// A connection released while the idle pool is full is CLOSED, and the next acquire opens
// a new one; against PostgreSQL that is a new TLS handshake and SCRAM login on both the
// service and the server. database/sql counts each such close in MaxIdleClosed.
func burstPool(t *testing.T, cfg config.MicroserviceDatastoreConfiguration, n int) *sql.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)
	nop := zerolog.Nop()
	require.NoError(t, applyPoolSizing(db, cfg, nop.Info()))
	sqldb, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqldb.Close() })

	for round := 0; round < 2; round++ {
		// A bounded acquire: a connection held by something else is a failure here,
		// not a hang of the whole test binary.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		conns := make([]*sql.Conn, n)
		for i := range conns {
			conns[i], err = sqldb.Conn(ctx)
			require.NoError(t, err)
		}
		cancel()
		// The fixture: n connections really were in use at once, or the assertions the
		// caller makes say nothing about a burst.
		require.Equal(t, n, sqldb.Stats().InUse, "round %d did not hold %d connections at once", round, n)
		for _, c := range conns {
			require.NoError(t, c.Close())
		}
	}
	return sqldb
}

// burstStats is burstPool's statistics straight after the burst.
func burstStats(t *testing.T, cfg config.MicroserviceDatastoreConfiguration, n int) sql.DBStats {
	t.Helper()
	return burstPool(t, cfg, n).Stats()
}

// A pool allowed N connections keeps all N open after N were in use at once. Below that,
// every connection over the idle count is closed on release and reopened by the next
// query, which at 16 device-management resolvers on the default pool of 20 (10 kept) was
// about 15% of the service's CPU, spent logging in to PostgreSQL again.
func TestAPoolKeepsEveryConnectionItIsAllowedWarm(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  config.MicroserviceDatastoreConfiguration
	}{
		{"the default pool", config.MicroserviceDatastoreConfiguration{}},
		{"a configured pool", config.MicroserviceDatastoreConfiguration{MaxOpenConnections: 30}},
		{"a small pool", config.MicroserviceDatastoreConfiguration{MaxOpenConnections: 5}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := EffectiveMaxOpenConnections(tc.cfg)
			st := burstStats(t, tc.cfg, n)
			assert.Equal(t, n, st.MaxOpenConnections, "the pool's open limit")
			assert.Equal(t, n, st.Idle, "connections kept open after the burst was released")
			assert.Equal(t, int64(0), st.MaxIdleClosed,
				"connections closed on release because the idle pool was full")
			assert.Equal(t, n, st.OpenConnections)
		})
	}
}

// The control for the test above, and it is not ceremony: a pool that never released
// anything, or an instrument that cannot see a close, would read Idle == n there just as
// well. An explicit idle count is honoured, and the connections over it are closed on
// release (8 in each of the two rounds).
func TestAnExplicitIdleCountClosesTheRest(t *testing.T) {
	cfg := config.MicroserviceDatastoreConfiguration{MaxOpenConnections: 12, MaxIdleConnections: 4}
	st := burstStats(t, cfg, 12)
	assert.Equal(t, 4, st.Idle)
	assert.Equal(t, int64(16), st.MaxIdleClosed)
	assert.Equal(t, 4, st.OpenConnections)
}

// A pool kept warm must STAY warm while the service is quiet. An idle-time limit
// (SetConnMaxIdleTime) would close the connections a burst left open as soon as traffic
// paused, and the next burst would log in to PostgreSQL again: the reconnect cost moves
// from the busy period to the start of each one, which is the defect above with a delay.
//
// database/sql's connection cleaner runs at most once a second, so assertions made
// straight after a burst cannot see such a limit. This one waits past the first tick.
func TestAWarmPoolSurvivesAQuietPeriod(t *testing.T) {
	cfg := config.MicroserviceDatastoreConfiguration{MaxOpenConnections: 5}
	n := EffectiveMaxOpenConnections(cfg)
	sqldb := burstPool(t, cfg, n)
	require.Equal(t, n, sqldb.Stats().Idle, "the fixture: the burst left every connection idle")

	time.Sleep(1500 * time.Millisecond)
	st := sqldb.Stats()
	assert.Equal(t, n, st.Idle, "connections still open after a quiet period")
	assert.Equal(t, int64(0), st.MaxIdleTimeClosed, "connections closed for sitting idle")
	assert.Equal(t, int64(0), st.MaxLifetimeClosed, "connections closed for their age")
}

// The other half of that bargain: a connection is still closed connMaxLifetime after it
// was opened, which is what eventually returns the connections a burst opened and moves a
// long-lived pool onto a restarted or failed-over server. An hour cannot be waited for and
// database/sql reports the limit through no accessor, so this reads the value the pool
// holds. A field database/sql renames fails here by name, never as a silent pass.
func TestAPooledConnectionIsClosedAnHourAfterItOpened(t *testing.T) {
	sqldb := burstPool(t, config.MicroserviceDatastoreConfiguration{}, 1)
	for field, want := range map[string]time.Duration{
		"maxLifetime": time.Hour,
		"maxIdleTime": 0, // no idle-time limit; see the test above
	} {
		v := reflect.ValueOf(sqldb).Elem().FieldByName(field)
		require.True(t, v.IsValid(), "database/sql.DB has no field %q any more", field)
		require.Equal(t, reflect.Int64, v.Kind(), "database/sql.DB.%s", field)
		assert.Equal(t, want, time.Duration(v.Int()), "the pool's %s", field)
	}
}
