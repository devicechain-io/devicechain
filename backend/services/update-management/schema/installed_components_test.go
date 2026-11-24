// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"fmt"
	"testing"

	"github.com/devicechain-io/dc-microservice/rdb/sqlitetest"
	"github.com/stretchr/testify/require"
)

func insertInstalled(dbExec func(string, ...any) error, tenant, device string, incarnation int, component, source string) error {
	return dbExec(`INSERT INTO device_installed_components (tenant_id, device_token, device_incarnation, component, version, observed_at, source)
 VALUES (?, ?, ?, ?, '1.0.0', CURRENT_TIMESTAMP, ?)`, tenant, device, incarnation, component, source)
}

func TestInstalledComponentsMigrationReplaysAndKeepsItsRules(t *testing.T) {
	db := sqlitetest.OpenMemory(t, nil)
	exec := func(q string, args ...any) error { return db.Exec(q, args...).Error }
	m := NewInstalledComponentsSchema()
	require.NoError(t, m.Migrate(db))
	require.True(t, db.Migrator().HasTable("device_installed_components"))
	require.NoError(t, insertInstalled(exec, "acme", "d1", 1, "firmware", "INVENTORY"))
	// A replay over populated data changes nothing.
	require.NoError(t, m.Migrate(db))
	var n int64
	require.NoError(t, db.Raw(`SELECT count(*) FROM device_installed_components`).Scan(&n).Error)
	require.Equal(t, int64(1), n)

	// One row per (tenant, device, incarnation, component); another incarnation, component, device and
	// tenant are each their own.
	require.Error(t, insertInstalled(exec, "acme", "d1", 1, "firmware", "UPDATED"))
	for i, args := range []struct {
		tenant, device string
		inc            int
		component      string
	}{{"acme", "d1", 2, "firmware"}, {"acme", "d1", 1, "modem"}, {"acme", "d2", 1, "firmware"}, {"globex", "d1", 1, "firmware"}} {
		require.NoError(t, insertInstalled(exec, args.tenant, args.device, args.inc, args.component, "INVENTORY"), fmt.Sprint(i))
	}
	// The source is a closed vocabulary.
	require.Error(t, insertInstalled(exec, "acme", "d9", 1, "firmware", "GUESS"))
	require.NoError(t, insertInstalled(exec, "acme", "d9", 1, "firmware", "UPDATED"))

	// No foreign key: a previous-release pod never blocks a deletion on it.
	var fks []struct{ Table string }
	require.NoError(t, db.Raw("PRAGMA foreign_key_list(device_installed_components)").Scan(&fks).Error)
	require.Empty(t, fks)
}
