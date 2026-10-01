// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"context"
	"database/sql"
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

// burstStats opens a pool through applyPoolSizing, the function both an owned and a guest
// connection are sized by, then twice holds n connections at once and releases them all,
// and returns the pool's statistics afterwards.
//
// A connection released while the idle pool is full is CLOSED, and the next acquire opens
// a new one; against PostgreSQL that is a new TLS handshake and SCRAM login on both the
// service and the server. database/sql counts each such close in MaxIdleClosed.
func burstStats(t *testing.T, cfg config.MicroserviceDatastoreConfiguration, n int) sql.DBStats {
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
	return sqldb.Stats()
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
