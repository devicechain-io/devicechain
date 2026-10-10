// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

//go:build integration

// Integration tests for the device-scoped alternate-id key (NewDeviceAltIdKeySchema), against
// a real TimescaleDB. Run as postgres_integration_test.go describes.
package model

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const (
	oldAltIdIndexDef = `CREATE UNIQUE INDEX idx_events_tenant_alt_id ON "event-management".events USING btree (tenant_id, alt_id, occurred_time) WHERE (alt_id IS NOT NULL)`
	newAltIdIndexDef = `CREATE UNIQUE INDEX idx_events_tenant_device_alt_id ON "event-management".events USING btree (tenant_id, device_token, alt_id, occurred_time) WHERE (alt_id IS NOT NULL)`
)

func insertAltEvent(db *gorm.DB, tenant, device, alt string, id byte, at time.Time) error {
	return db.Exec(`INSERT INTO "event-management".events
		(tenant_id, event_id, device_token, event_type, occurred_time, source, alt_id, processed_time)
		VALUES (?, ?, ?, 1, ?, 'it', ?, ?)`, tenant, []byte{id}, device, at, alt, at).Error
}

// The migration replaces the tenant-wide key with the device-scoped one, keeps every stored
// row and is a no-op when run again.
// Before it, two devices of one tenant cannot share an alternate id at one instant; after
// it they can, while one device still cannot repeat it.
func TestIntegrationDeviceAltIdKeyReplacesTheTenantWideKey(t *testing.T) {
	inst := freshInstance(t, "italtid")
	mgr := newPostgresManagerWith(t, inst, migrationsThrough(t, NewTenantDeviceIndexSchema().ID))
	sys := systemDB(mgr)
	at := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)

	require.NoError(t, insertAltEvent(sys, "acme", "dev-a", "m1", 1, at))
	require.Error(t, insertAltEvent(sys, "acme", "dev-b", "m1", 2, at), "tenant-wide key: the second device collides")
	assert.Equal(t, oldAltIdIndexDef, hypertableIndexes(t, sys, "events")["idx_events_tenant_alt_id"])

	require.NoError(t, NewDeviceAltIdKeySchema().Migrate(sys))
	idx := hypertableIndexes(t, sys, "events")
	assert.Equal(t, newAltIdIndexDef, idx["idx_events_tenant_device_alt_id"])
	assert.NotContains(t, idx, "idx_events_tenant_alt_id")

	var n int64
	require.NoError(t, sys.Raw(`SELECT count(*) FROM "event-management".events`).Scan(&n).Error)
	assert.EqualValues(t, 1, n, "stored rows survive")
	require.NoError(t, insertAltEvent(sys, "acme", "dev-b", "m1", 2, at), "another device may reuse the alternate id")
	require.Error(t, insertAltEvent(sys, "acme", "dev-a", "m1", 3, at), "one device may not repeat it at one instant")

	oid := deviceIndexOID(t, sys, "idx_events_tenant_device_alt_id")
	require.NoError(t, NewDeviceAltIdKeySchema().Migrate(sys), "re-run")
	assert.Equal(t, oid, deviceIndexOID(t, sys, "idx_events_tenant_device_alt_id"), "a re-run rebuilds nothing")
}

// An index of the new name that is not the one this migration builds ends it before any DDL.
func TestIntegrationDeviceAltIdKeyRefusesAForeignIndexOfItsName(t *testing.T) {
	inst := freshInstance(t, "italtidforeign")
	mgr := newPostgresManagerWith(t, inst, migrationsThrough(t, NewTenantDeviceIndexSchema().ID))
	sys := systemDB(mgr)
	require.NoError(t, sys.Exec(`CREATE INDEX idx_events_tenant_device_alt_id
		ON "event-management".events (tenant_id, alt_id, occurred_time)`).Error)

	err := NewDeviceAltIdKeySchema().Migrate(sys)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not replace an index it did not write")
	assert.Contains(t, hypertableIndexes(t, sys, "events"), "idx_events_tenant_alt_id", "nothing was dropped")
}
