// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// raiseAlarm.workers loads as written, and a document that omits it gets the default.
func TestRaiseAlarmWorkersLoadAndDefault(t *testing.T) {
	cfg := &DeviceManagementConfiguration{}
	require.NoError(t, core.LoadConfiguration([]byte(`{"raiseAlarm":{"workers":3}}`), cfg))
	assert.Equal(t, 3, cfg.RaiseAlarm.Workers)

	cfg = &DeviceManagementConfiguration{}
	require.NoError(t, core.LoadConfiguration([]byte(``), cfg))
	assert.Equal(t, DefaultRaiseAlarmWorkers, cfg.RaiseAlarm.Workers)
	assert.Equal(t, DefaultRaiseAlarmWorkers, NewDeviceManagementConfiguration().RaiseAlarm.Workers)
}

// raiseAlarm.workers is bounded by the relational pool, an out-of-range value stops the service
// naming the setting, and an unknown key under raiseAlarm is refused.
func TestRaiseAlarmWorkersAreBounded(t *testing.T) {
	for _, tc := range []struct{ name, doc, wantErr string }{
		{"negative", `{"raiseAlarm":{"workers":-1}}`, "raiseAlarm.workers must be at least 1, got -1"},
		{"at the default pool", `{"raiseAlarm":{"workers":20}}`,
			"raiseAlarm.workers is 20, but the connection pool holds 20"},
		{"below the default pool", `{"raiseAlarm":{"workers":19}}`, ""},
		{"one worker", `{"raiseAlarm":{"workers":1}}`, ""},
		{"at a configured pool", `{"rdbConfiguration":{"maxOpenConnections":12},"raiseAlarm":{"workers":12}}`,
			"raiseAlarm.workers is 12, but the connection pool holds 12"},
		{"a misspelled key", `{"raiseAlarm":{"worker":8}}`, `unknown field "worker"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &DeviceManagementConfiguration{}
			err := core.LoadConfiguration([]byte(tc.doc), cfg)
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// An unset count is held below a small pool rather than refused, so a pool sized before the
// setting existed still starts; an explicit count is validated as written.
func TestRaiseAlarmWorkersDefaultFitsASmallPool(t *testing.T) {
	cfg := &DeviceManagementConfiguration{}
	require.NoError(t, core.LoadConfiguration([]byte(`{"rdbConfiguration":{"maxOpenConnections":8},"resolution":{"workers":4}}`), cfg))
	assert.Equal(t, 7, cfg.RaiseAlarm.Workers)
}
