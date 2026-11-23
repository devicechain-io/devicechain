// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"crypto/md5"
	"encoding/hex"
	"testing"

	"github.com/devicechain-io/dc-device-management/schema"
	"github.com/devicechain-io/dc-microservice/rdb/sqlitetest"
	"github.com/stretchr/testify/require"
)

// The activation check and readiness compare md5(prosrc) of the installed function with a constant
// compiled into the service; the migration ships the text. This holds the two in step, so a change
// to either one fails here rather than reading as NOT_INSTALLED on every instance.
func TestEnforcementFunctionHashIsPinned(t *testing.T) {
	sum := md5.Sum([]byte(schema.AttributeWriterProtocolFunctionSource))
	require.Equal(t, FleetOperationEnforcementFunctionMD5, hex.EncodeToString(sum[:]),
		"the enforcing function's text changed: it is frozen once shipped; add a new function and migration instead")
}

func TestActivationCheckRefusesWithoutAGuardItCanSee(t *testing.T) {
	db := sqlitetest.OpenMemory(t, nil)
	// Outside a transaction it cannot take the lock that makes its answer last.
	require.Error(t, CheckFleetOperationActivation(db))
	require.Error(t, CheckFleetOperationActivation(nil))
	// Inside one, SQLite has no such guard to find: fail closed, never a plausible yes.
	require.ErrorIs(t, db.Transaction(CheckFleetOperationActivation), ErrFleetOperationNotActivated)
}

func TestTheMigrationIsANoOpWhereThereAreNoTriggers(t *testing.T) {
	db := sqlitetest.OpenMemory(t, nil)
	migration := schema.NewAttributeWriterProtocolSchema()
	require.NoError(t, migration.Migrate(db))
	require.NoError(t, migration.Migrate(db))
	require.Error(t, migration.Rollback(db))
}

func TestFleetOperationCatalogDecisionOverFromStates(t *testing.T) {
	require.Equal(t, "ENFORCED", fleetOperationEnforcementFromStates([]string{"O", "A"}))
	require.Equal(t, "DISABLED", fleetOperationEnforcementFromStates([]string{"O"}))
	require.Equal(t, "NOT_INSTALLED", fleetOperationEnforcementFromStates(nil))
}
