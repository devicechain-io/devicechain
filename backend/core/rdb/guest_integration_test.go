// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// A guest connection driven against a real PostgreSQL server.
//
// The unit tests around it pin what a Guest BUILDS — the database it targets, the
// search_path it refuses to pin, the configuration it rejects at startup — and one thing
// they structurally cannot pin: whether the error a real driver returns for a missing
// database still looks like one by the time it has been through pgx, database/sql, gorm
// and this package's own wrapping. IsMissingDatabase uses errors.As, so a single layer
// that formats instead of wrapping turns "there is no such database" into an opaque
// string, and the ADR-077 telemetry store stops being able to tell a database that does
// not exist from a cluster it could not reach. A hand-built *pgconn.PgError cannot catch
// that, because it starts life at the top of the chain.
//
// Run it against a throwaway server (hack/migration-diff.sh starts one; note its port):
//
//	DC_IT_PGPORT=$PORT go test -tags integration -count=1 ./rdb/... -v
//
// No -run filter: the test that matters most here is not named "Guest", and an earlier
// version of this line recommended one that skipped it.
package rdb

import (
	"context"
	"os"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
)

// liveGuest builds a Guest pointed at the test server, for the given instance id — which
// is the database name it will try to open.
func liveGuest(t *testing.T, instanceId string) *Guest {
	t.Helper()
	port := 5432
	if raw := os.Getenv("DC_IT_PGPORT"); raw != "" {
		p, err := strconv.Atoi(raw)
		require.NoError(t, err, "DC_IT_PGPORT must be a port number")
		port = p
	}
	host := "localhost"
	if h := os.Getenv("DC_IT_PGHOST"); h != "" {
		host = h
	}
	g := NewGuest(
		&core.Microservice{InstanceId: instanceId, FunctionalArea: "user-management"},
		"tsdb",
		config.DatastoreConfiguration{Configuration: map[string]interface{}{
			"hostname": host,
			"port":     port,
			"username": "postgres",
			"password": "postgres",
			"sslMode":  "disable",
		}},
		config.MicroserviceDatastoreConfiguration{},
	)
	require.NoError(t, g.Initialize(context.Background()))
	t.Cleanup(func() { _ = g.Close() })
	return g
}

// TestAGuestConnectsToADatabaseItDoesNotOwn is the control for the test below, and it is
// not ceremony: "connecting failed with a recognisable error" would be satisfied just as
// well by a guest that can never connect to anything.
func TestAGuestConnectsToADatabaseItDoesNotOwn(t *testing.T) {
	ctx := context.Background()
	g := liveGuest(t, "postgres")

	require.NoError(t, g.Connect(ctx))
	require.NoError(t, g.Connect(ctx), "Connect is called before every pass and must be idempotent")

	var path string
	require.NoError(t, g.DB(ctx).Raw("SHOW search_path").Scan(&path).Error)
	assert.Equal(t, "public", path,
		"the server must agree about the path, not merely have been sent one")

	// And it created nothing on the way in. An RdbManager would have made a schema named
	// after this service and an audit_events table inside it; that is the whole reason
	// this type exists, so it is asserted against the server rather than inferred from
	// the absence of a call.
	var schemas int64
	require.NoError(t, g.DB(ctx).Raw(
		`SELECT count(*) FROM information_schema.schemata WHERE schema_name = ?`,
		"user-management").Scan(&schemas).Error)
	assert.Zero(t, schemas, "a guest must not create a schema in a database it does not own")
}

// TestARealMissingDatabaseSurvivesEveryLayerAsOne is the assertion this file exists for.
func TestARealMissingDatabaseSurvivesEveryLayerAsOne(t *testing.T) {
	g := liveGuest(t, "dc-no-such-instance-database")

	err := g.Connect(context.Background())
	require.Error(t, err)
	assert.True(t, IsMissingDatabase(err),
		"the server answered that the database does not exist, but the error did not survive "+
			"the driver and wrapper chain as one: %v.\nThe telemetry store reads this to tell an "+
			"instance that runs no event-management from a cluster it could not reach, and without "+
			"it every purge on such an instance blocks forever", err)
}
