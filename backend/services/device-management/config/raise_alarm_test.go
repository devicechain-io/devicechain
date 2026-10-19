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
	assert.Equal(t, 6, cfg.RaiseAlarm.Workers)
	assert.Equal(t, 6, NewDeviceManagementConfiguration().RaiseAlarm.Workers)
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

// An unset count is what the pool has left after the resolvers and the headroom, capped at the
// default and floored at 1, so the two worker pools leave the headroom free whenever the pool
// has room for it, and a pool sized before the setting existed still starts (the floor wins
// over the headroom there).
func TestRaiseAlarmWorkersDefaultLeavesHeadroom(t *testing.T) {
	for _, tc := range []struct {
		name, doc string
		pool, want int
	}{
		{"default pool 20, 10 resolvers", ``, 20, 6},
		{"pool 11, 10 resolvers", `{"rdbConfiguration":{"maxOpenConnections":11}}`, 11, 1},
		{"pool 8, 3 resolvers", `{"rdbConfiguration":{"maxOpenConnections":8},"resolution":{"workers":3}}`, 8, 1},
		{"pool 8, 2 resolvers", `{"rdbConfiguration":{"maxOpenConnections":8},"resolution":{"workers":2}}`, 8, 2},
		{"large pool is capped at the default", `{"rdbConfiguration":{"maxOpenConnections":64}}`, 64, DefaultRaiseAlarmWorkers},
		{"small pool, 1 resolver", `{"rdbConfiguration":{"maxOpenConnections":3},"resolution":{"workers":1}}`, 3, 1},
		{"explicit resolvers, unset pool", `{"resolution":{"workers":12}}`, 20, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &DeviceManagementConfiguration{}
			require.NoError(t, core.LoadConfiguration([]byte(tc.doc), cfg))
			assert.Equal(t, tc.want, cfg.RaiseAlarm.Workers)
			held := cfg.Resolution.Workers + cfg.RaiseAlarm.Workers
			if tc.want > 1 {
				assert.LessOrEqual(t, held+PoolHeadroom, tc.pool, "above the floor, the headroom is left free")
			} else {
				assert.LessOrEqual(t, held, tc.pool, "at the floor, the workers never exceed the pool")
			}
		})
	}
}

// An explicit count passes through untouched, whatever the resolvers hold: it is validated on
// its own against the pool, as it was before the default took headroom into account.
func TestRaiseAlarmWorkersExplicitIsNotAdjusted(t *testing.T) {
	cfg := &DeviceManagementConfiguration{}
	require.NoError(t, core.LoadConfiguration([]byte(`{"raiseAlarm":{"workers":8}}`), cfg))
	assert.Equal(t, 8, cfg.RaiseAlarm.Workers)
	assert.Equal(t, 10, cfg.Resolution.Workers)

	cfg = &DeviceManagementConfiguration{}
	require.NoError(t, core.LoadConfiguration([]byte(`{"rdbConfiguration":{"maxOpenConnections":8},"raiseAlarm":{"workers":7},"resolution":{"workers":4}}`), cfg))
	assert.Equal(t, 7, cfg.RaiseAlarm.Workers)
}
