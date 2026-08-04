// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package rdb

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
)

// guestFixture builds a Guest as user-management builds the telemetry one: a service
// whose functional area is user-management, visiting the instance database on another
// cluster.
func guestFixture(t *testing.T, cfg map[string]interface{}) *Guest {
	t.Helper()
	return NewGuest(
		&core.Microservice{InstanceId: "prod", FunctionalArea: "user-management"},
		core.NewNoOpLifecycleCallbacks(), "tsdb",
		config.DatastoreConfiguration{Configuration: cfg},
		config.MicroserviceDatastoreConfiguration{},
	)
}

func guestPgConfig() map[string]interface{} {
	return map[string]interface{}{
		"hostname": "dc-timescaledb-single.dc-system",
		"port":     5432,
		"username": "devicechain",
		"password": "devicechain",
	}
}

// TestAGuestNeverPinsTheVisitingServicesSchema is the assertion that separates this type
// from an RdbManager, and it is checked through pgconn's own parser rather than by
// matching the string we just formatted.
//
// 🔴 THE FAILURE IT GUARDS IS SILENT. An owned connection pins search_path to the
// caller's functional area so its unqualified statements resolve there. Carry that over
// to a database the caller owns nothing in and the path names a schema that does not
// exist — every unqualified statement then resolves into public or fails, and the
// difference between "swept the telemetry cluster" and "swept nothing" comes down to
// whether a query happened to be schema-qualified.
func TestAGuestNeverPinsTheVisitingServicesSchema(t *testing.T) {
	g := guestFixture(t, guestPgConfig())
	require.NoError(t, g.ExecuteInitialize(context.Background()))

	cfg := parse(t, g.dsn(g.resolved))

	assert.Equal(t, "public", cfg.RuntimeParams["search_path"],
		"a guest owns no schema in this database; pinning its own functional area would name "+
			"one that is not there")
	assert.NotContains(t, cfg.RuntimeParams["search_path"], "user-management")
}

// TestAGuestTargetsTheInstanceDatabaseOnTheOtherCluster pins the other half: the database
// name is the instance id, because every cluster hosts one database per instance and the
// areas inside it are schemas. A guest that invented a database name would connect
// somewhere that does not exist, or worse, somewhere that does.
func TestAGuestTargetsTheInstanceDatabaseOnTheOtherCluster(t *testing.T) {
	g := guestFixture(t, guestPgConfig())
	require.NoError(t, g.ExecuteInitialize(context.Background()))

	cfg := parse(t, g.dsn(g.resolved))

	assert.Equal(t, "prod", cfg.Database)
	assert.Equal(t, "dc-timescaledb-single.dc-system", cfg.Host,
		"the guest must reach the OTHER cluster, not the one this service owns")
	assert.Equal(t, "devicechain", cfg.User)
}

// TestAGuestRejectsAnUnusableConfigurationAtStartup pins where the verdict lands.
//
// Connecting is lazy, so nothing else about a guest happens until a purge runs. That
// makes it tempting to defer the configuration check too — and then an operator who
// misspells sslMode learns about it weeks later, from a purge that will not complete,
// rather than from the pod that would not start. Initialize resolves the configuration
// for exactly this reason.
func TestAGuestRejectsAnUnusableConfigurationAtStartup(t *testing.T) {
	bad := guestPgConfig()
	bad["sslMode"] = "requrie"
	err := guestFixture(t, bad).ExecuteInitialize(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requrie")

	// An unknown key is the same class of mistake and is rejected the same way: silently
	// ignoring a key someone deliberately wrote leaves the posture at its default while
	// they believe they changed it.
	unknown := guestPgConfig()
	unknown["ssl_mode"] = "require"
	require.Error(t, guestFixture(t, unknown).ExecuteInitialize(context.Background()))
}

// TestAnUninitializedGuestSaysSoRatherThanPanicking covers the order dependency the lazy
// connect introduces. Connect reads configuration Initialize resolved, so calling it
// first must produce a sentence, not a nil dereference somewhere in the driver.
func TestAnUninitializedGuestSaysSoRatherThanPanicking(t *testing.T) {
	err := guestFixture(t, guestPgConfig()).Connect(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not initialized")
}

// TestTerminatingAGuestThatNeverConnectedIsANoOp is the common case, not an edge one: on
// every instance where no tenant was ever purged, the telemetry guest is built, resolved
// and never used. Shutdown must not fail there.
func TestTerminatingAGuestThatNeverConnectedIsANoOp(t *testing.T) {
	g := guestFixture(t, guestPgConfig())
	require.NoError(t, g.ExecuteInitialize(context.Background()))
	require.NoError(t, g.ExecuteTerminate(context.Background()))
}

// TestOnlyAMissingDatabaseCountsAsAMissingDatabase pins the discrimination a caller draws
// a "there is nothing here to erase" conclusion from.
//
// That conclusion is the most dangerous kind — it lets a purge complete and release a
// token — so it must rest on Postgres having ANSWERED, not on any failure to reach it. A
// refused connection, a bad password and a dropped socket all mean "come back later"; only
// 3D000 means "I am here, and there is no such database".
func TestOnlyAMissingDatabaseCountsAsAMissingDatabase(t *testing.T) {
	assert.True(t, IsMissingDatabase(
		fmt.Errorf("connecting to the %q database: %w", "tsdb", &pgconn.PgError{Code: "3D000"})),
		"it has to survive being wrapped — Connect wraps every driver error it returns")

	for name, err := range map[string]error{
		"authentication failed":  &pgconn.PgError{Code: "28P01"},
		"insufficient privilege": &pgconn.PgError{Code: "42501"},
		"connection refused":     errors.New("dial tcp 10.0.0.1:5432: connect: connection refused"),
		"nothing at all":         nil,
	} {
		assert.Falsef(t, IsMissingDatabase(err),
			"%q is not the database answering that it does not exist; treating it as one would "+
				"complete a purge over a store that was never reached", name)
	}
}
