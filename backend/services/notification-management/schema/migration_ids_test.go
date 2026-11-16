// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/rdb"
)

// TestMigrationIDsAreAcceptedByGormigrate runs the same ID check the service runs before
// gormigrate.New. A duplicate ID makes gormigrate refuse the whole chain at startup, and
// this package's DDL tests call each migration directly, so without this nothing short
// of a real Postgres would see it.
func TestMigrationIDsAreAcceptedByGormigrate(t *testing.T) {
	require.NoError(t, rdb.CheckMigrationIDs(Migrations))
	require.Equal(t, "20261114000100", Migrations[len(Migrations)-1].ID)
	for _, previous := range Migrations[:len(Migrations)-1] {
		require.Less(t, previous.ID, Migrations[len(Migrations)-1].ID)
	}
}
