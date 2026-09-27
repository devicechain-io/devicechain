// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// The idle-in-transaction bound, asked of a real PostgreSQL server rather than of the
// connection string that requests it.
//
// connection_string_test.go proves every builder WRITES the parameter. It cannot prove
// the parameter reaches the transactions the erasure argument is about: that depends on
// initializePostgres handing that string, and not one rebuilt from fields, to gorm, and on
// the fence pool's transactions running on those connections. So the owned leg below
// runs a functional area's real ExecuteInitialize and reads the setting from INSIDE a
// transaction begun through it. The server answers with the value it is enforcing and
// where that value came from, which a connection string cannot fake.
//
// Run it against a throwaway server (hack/migration-diff.sh runs this package with the
// integration tag on both supported PostgreSQL majors):
//
//	DC_IT_PGPORT=$PORT go test -tags integration -count=1 ./rdb/... -v
package rdb

import (
	"context"
	"net"
	"net/url"
	"os"
	"strconv"
	"testing"

	"github.com/go-gormigrate/gormigrate/v2"
	pgx "github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb/rdbtest"
)

// idleSetting is the server's own view of the parameter for the session it runs on.
type idleSetting struct {
	Setting string
	Source  string
}

const idleSettingQuery = `SELECT setting, source FROM pg_settings WHERE name = 'idle_in_transaction_session_timeout'`

func itServer(t *testing.T) (host string, port int) {
	t.Helper()
	port = 5432
	if raw := os.Getenv("DC_IT_PGPORT"); raw != "" {
		p, err := strconv.Atoi(raw)
		require.NoError(t, err, "DC_IT_PGPORT must be a port number")
		port = p
	}
	host = "localhost"
	if h := os.Getenv("DC_IT_PGHOST"); h != "" {
		host = h
	}
	return host, port
}

// idleTimeoutWidget gives the area below a migration to run; the chain refuses to run
// with none.
type idleTimeoutWidget struct {
	ID   uint `gorm:"primaryKey"`
	Name string
}

// A functional area initialized the way a service initializes one, reading the setting
// from inside a transaction begun through the fence pool.
func TestAnOwnedTransactionRunsUnderTheIdleTimeout(t *testing.T) {
	ctx := context.Background()
	host, port := itServer(t)
	const instance = "rdbidletimeout"
	require.NoError(t, rdbtest.EnsureDatabase(ctx, host, port, "postgres", "postgres", instance, ""))

	mgr := &RdbManager{
		Microservice: &core.Microservice{InstanceId: instance, FunctionalArea: "idle-timeout"},
		Migrations: []*gormigrate.Migration{{
			ID:      "20260927000000",
			Migrate: func(tx *gorm.DB) error { return tx.AutoMigrate(&idleTimeoutWidget{}) },
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
	t.Cleanup(func() {
		if sqldb, err := mgr.Database.DB(); err == nil {
			_ = sqldb.Close()
		}
	})

	var got idleSetting
	require.NoError(t, mgr.DB(core.WithSystemContext(ctx)).Transaction(func(tx *gorm.DB) error {
		// The transaction the erasure argument is about is one begun through the fence
		// pool; a pass on some other pool would prove nothing about it.
		if _, ok := tx.Statement.ConnPool.(*fenceTx); !ok {
			t.Errorf("the transaction ran on %T, not on the fence pool's transaction", tx.Statement.ConnPool)
		}
		return tx.Raw(idleSettingQuery).Scan(&got).Error
	}))
	assert.Equal(t, idleSetting{Setting: "60000", Source: "client"}, got,
		"the server is not enforcing the idle-in-transaction bound this service's connections ask for")

	// The control. The same query on a connection that does not ask for the bound must
	// read the server's default, or the assertion above could be passing on whatever the
	// server happens to be configured with.
	u := &url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword("postgres", "postgres"),
		Host:     net.JoinHostPort(host, strconv.Itoa(port)),
		Path:     "/" + instance,
		RawQuery: url.Values{"sslmode": []string{"disable"}}.Encode(),
	}
	conn, err := pgx.Connect(ctx, u.String())
	require.NoError(t, err)
	defer conn.Close(context.Background())
	var control idleSetting
	require.NoError(t, conn.QueryRow(ctx, idleSettingQuery).Scan(&control.Setting, &control.Source))
	assert.Equal(t, idleSetting{Setting: "0", Source: "default"}, control,
		"a connection that does not ask for the bound must read the server default; if it does "+
			"not, the assertion above cannot tell the bound from the server's own configuration")
}

// The guest connection (the telemetry purge's) is built by the same primitives, and
// reaches the server through its own gorm.Open.
func TestAGuestConnectionRunsUnderTheIdleTimeout(t *testing.T) {
	ctx := context.Background()
	g := liveGuest(t, "postgres")
	require.NoError(t, g.Connect(ctx))

	var got idleSetting
	require.NoError(t, g.DB(ctx).Raw(idleSettingQuery).Scan(&got).Error)
	assert.Equal(t, idleSetting{Setting: "60000", Source: "client"}, got)
}
