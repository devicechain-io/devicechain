// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

package model

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/stretchr/testify/require"
)

// The start gate's orphan probe, bounded by a statement timeout, refuses as unverified when the
// bound is hit, never as "orphans remain"; and it asks for one orphan, not a count of them.
func TestFleetOrphanGateTimeoutIsUnverifiedOnPostgres(t *testing.T) {
	api := NewApi(driverPGManager(t))
	ctx := core.WithTenant(context.Background(), "bcs-orphans")
	// (Finding an orphan is the SQLite suite's; the database guard admits no attribute row here
	// without the writer protocol. This is the probe's shape and its bound.)

	session, capture := capturing(api.RDB.Database)
	found, err := NewApi(&rdb.RdbManager{Database: session}).HasOrphanedDeviceAttributes(ctx)
	require.NoError(t, err)
	require.False(t, found)
	probes := 0
	for _, s := range capture.statements {
		if strings.Contains(s, "NOT EXISTS (SELECT 1 FROM devices") {
			probes++
			require.Contains(t, s, "LIMIT 1", "the probe asks for one orphan")
			require.NotContains(t, strings.ToLower(s), "count(", "the probe does not count")
		}
	}
	require.Equal(t, 2, probes, "one probe of each table when neither holds an orphan")
	require.NoError(t, api.fleetCheckOrphans(ctx), "nothing orphaned, and the check completed: the gate opens")

	// Hold the attribute table so the probe waits past its bound.
	prev := fleetOrphanProbeTimeout
	fleetOrphanProbeTimeout = "300ms"
	t.Cleanup(func() { fleetOrphanProbeTimeout = prev })
	pool, err := api.RDB.Database.DB()
	require.NoError(t, err)
	conn, err := pool.Conn(context.Background())
	require.NoError(t, err)
	defer conn.Close()
	tx, err := conn.BeginTx(context.Background(), &sql.TxOptions{})
	require.NoError(t, err)
	defer tx.Rollback()
	_, err = tx.Exec(`LOCK TABLE "device-management".entity_attributes IN ACCESS EXCLUSIVE MODE`)
	require.NoError(t, err)
	require.NoError(t, api.fleetCheckOrphansErr(ctx, FleetCodeActivationUnverified))
}

// fleetCheckOrphansErr runs the gate and checks the refusal code it answers.
func (api *Api) fleetCheckOrphansErr(ctx context.Context, want string) error {
	err := api.fleetCheckOrphans(ctx)
	fe, ok := err.(*FleetOperationError)
	if !ok || fe.Code != want {
		return &FleetOperationError{Code: "UNEXPECTED", Message: "want " + want + ", got " + errString(err)}
	}
	return nil
}

func errString(err error) string {
	if err == nil {
		return "nil"
	}
	return err.Error()
}
