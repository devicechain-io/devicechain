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
