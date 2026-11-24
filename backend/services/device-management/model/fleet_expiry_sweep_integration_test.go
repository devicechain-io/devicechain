// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/require"
)

func TestConcurrentFleetExpiryOnPostgres(t *testing.T) {
	api, ctx, _, _ := fleetPGFixture(t, 0)
	assertConcurrentFleetExpiry(t, api, ctx)
}

func TestFleetExpirySweepSkipsPeerLockAndReleasesAfterPass(t *testing.T) {
	api, ctx, tenant, _ := fleetPGFixture(t, 0)
	// This global pass is bounded: put its unique tenant before any backlog from earlier suites.
	ctx = core.WithTenant(ctx, "0-fleet-expiry-"+tenant)
	seedExpiryOperation(t, api, ctx, "singleton", FleetDispatchReady, time.Now())
	pool, err := api.RDB.Database.DB()
	require.NoError(t, err)
	conn, err := pool.Conn(ctx)
	require.NoError(t, err)
	defer conn.Close()
	key := rdb.AdvisoryLockKey(fleetExpirySweepLockName)
	_, err = conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", key)
	require.NoError(t, err)
	defer conn.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", key)
	require.ErrorIs(t, api.SweepExpiredFleetOperations(ctx), core.ErrPassSkipped)
	op, err := api.fleetOperationByToken(ctx, "singleton")
	require.NoError(t, err)
	require.Equal(t, FleetDispatchReady, op.Dispatch)
	_, err = conn.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", key)
	require.NoError(t, err)
	require.NoError(t, api.SweepExpiredFleetOperations(ctx))
	op, err = api.fleetOperationByToken(ctx, "singleton")
	require.NoError(t, err)
	require.Equal(t, FleetDispatchAborted, op.Dispatch)
	// The sweep must release its session lock before returning its connection to the pool.
	var acquired bool
	require.NoError(t, conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", key).Scan(&acquired))
	require.True(t, acquired)
}
