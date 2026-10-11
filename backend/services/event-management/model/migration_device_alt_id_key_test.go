// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestDeviceAltIdKeySnapshot pins the migration's own snapshot by value and its place in the
// chain: appended last, after the per-device index.
func TestDeviceAltIdKeySnapshot(t *testing.T) {
	assert.Equal(t, deviceAltIdKeySnapshot{
		table:    "events",
		name:     "idx_events_tenant_device_alt_id",
		columns:  "tenant_id, device_token, alt_id, occurred_time",
		where:    "alt_id IS NOT NULL",
		replaces: "idx_events_tenant_alt_id",
	}, deviceAltIdKey)

	m := NewDeviceAltIdKeySchema()
	assert.Equal(t, "20261010000000", m.ID)
	assert.Greater(t, m.ID, NewTenantDeviceIndexSchema().ID)
	assert.Equal(t, m.ID, Migrations[len(Migrations)-1].ID, "appended last")
	assert.Nil(t, m.Rollback)
	assert.Contains(t, deviceAltIdKeyManualAdvice, deviceAltIdKey.name)
}

// The hand-run build is chunk by chunk and, like the migration's own, has no IF NOT EXISTS: a
// leftover of that name must fail the statement loudly rather than be skipped as built.
func TestDeviceAltIdKeyManualStatements(t *testing.T) {
	assert.Contains(t, deviceAltIdKeyManualBuild, "CREATE UNIQUE INDEX idx_events_tenant_device_alt_id")
	assert.Contains(t, deviceAltIdKeyManualBuild, "(tenant_id, device_token, alt_id, occurred_time) WHERE alt_id IS NOT NULL")
	assert.NotContains(t, deviceAltIdKeyManualBuild, "transaction_per_chunk", "not accepted for a unique, partial index")
	assert.NotContains(t, deviceAltIdKeyManualBuild, "IF NOT EXISTS")
	assert.Equal(t, `DROP INDEX IF EXISTS "event-management".idx_events_tenant_device_alt_id`, deviceAltIdKeyManualDrop)
}
