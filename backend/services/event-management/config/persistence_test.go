// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The persistence settings load as written, and unset ones take their defaults.
func TestPersistenceSettingsLoad(t *testing.T) {
	cfg := &EventManagementConfiguration{}
	require.NoError(t, core.LoadConfiguration([]byte(`{"persistence":{"writers":8,"maxBatch":16,"lingerMillis":5}}`), cfg))
	assert.Equal(t, PersistenceConfiguration{Writers: 8, MaxBatch: 16, LingerMillis: 5}, cfg.Persistence)
	assert.Equal(t, 5*time.Millisecond, cfg.Persistence.Linger())

	cfg = &EventManagementConfiguration{}
	require.NoError(t, core.LoadConfiguration([]byte(``), cfg))
	assert.Equal(t, PersistenceConfiguration{Writers: 5, MaxBatch: 32, LingerMillis: 0}, cfg.Persistence)
}

// Out-of-range settings stop the service at startup, naming the setting; the edges of each
// range are accepted. The writer bound is the connection pool the writers draw from — the
// default of 20 when tsdbConfiguration sets none.
func TestPersistenceSettingsAreBounded(t *testing.T) {
	for _, tc := range []struct {
		name, doc, wantErr string
	}{
		{"negative writers", `{"persistence":{"writers":-1}}`, "persistence.writers must be at least 1, got -1"},
		{"writers at the default pool", `{"persistence":{"writers":20}}`,
			"persistence.writers is 20, but the connection pool holds 20; keep it below 20 so reads are not starved"},
		{"writers just below the default pool", `{"persistence":{"writers":19}}`, ""},
		{"writers at a configured pool", `{"tsdbConfiguration":{"maxOpenConnections":30},"persistence":{"writers":30}}`,
			"persistence.writers is 30, but the connection pool holds 30"},
		{"writers below a configured pool", `{"tsdbConfiguration":{"maxOpenConnections":30},"persistence":{"writers":25}}`, ""},
		// An instance that set a small pool before the setting existed ran 5 writers on it,
		// and keeps starting: the default only stops a service whose pool is 5 or smaller.
		{"default writers on a small pool", `{"tsdbConfiguration":{"maxOpenConnections":8}}`, ""},
		{"default writers on a pool of 5", `{"tsdbConfiguration":{"maxOpenConnections":5}}`,
			"persistence.writers is 5, but the connection pool holds 5"},
		{"negative maxBatch", `{"persistence":{"maxBatch":-1}}`, "persistence.maxBatch must be between 1 and 64, got -1"},
		{"maxBatch above the cap", `{"persistence":{"maxBatch":65}}`, "persistence.maxBatch must be between 1 and 64, got 65"},
		{"maxBatch at the cap", `{"persistence":{"maxBatch":64}}`, ""},
		{"maxBatch of one", `{"persistence":{"maxBatch":1}}`, ""},
		{"negative lingerMillis", `{"persistence":{"lingerMillis":-1}}`, "persistence.lingerMillis must be between 0 and 1000, got -1"},
		{"lingerMillis above the cap", `{"persistence":{"lingerMillis":1001}}`, "persistence.lingerMillis must be between 0 and 1000, got 1001"},
		{"lingerMillis at the cap", `{"persistence":{"lingerMillis":1000}}`, ""},
		{"a misspelled key", `{"persistence":{"workers":8}}`, `unknown field "workers"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := core.LoadConfiguration([]byte(tc.doc), &EventManagementConfiguration{})
			if tc.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			if assert.Error(t, err) {
				assert.Contains(t, err.Error(), tc.wantErr)
			}
		})
	}
}
