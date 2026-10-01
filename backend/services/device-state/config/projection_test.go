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

// The projection settings load as written, and an empty document takes the defaults: 10
// writers, batches of up to 32, no linger.
func TestProjectionWritersLoad(t *testing.T) {
	cfg := &DeviceStateConfiguration{}
	require.NoError(t, core.LoadConfiguration([]byte(`{"projection":{"writers":8,"maxBatch":16,"lingerMillis":5}}`), cfg))
	assert.Equal(t, ProjectionConfiguration{Writers: 8, MaxBatch: 16, LingerMillis: 5}, cfg.Projection)
	assert.Equal(t, 5*time.Millisecond, cfg.Projection.Linger())

	cfg = &DeviceStateConfiguration{}
	require.NoError(t, core.LoadConfiguration([]byte(``), cfg))
	assert.Equal(t, ProjectionConfiguration{Writers: 10, MaxBatch: 32, LingerMillis: 0}, cfg.Projection)
	assert.Equal(t, 10, DefaultProjectionWriters)
	assert.Equal(t, 32, DefaultProjectionMaxBatch)
}

// projection.writers is bounded by the relational pool it draws from — 20 unless
// rdbConfiguration sets one — the batch settings by the ranges event-management's share, and
// an out-of-range value stops the service, naming it. The edges of each range are accepted.
func TestProjectionWritersAreBounded(t *testing.T) {
	for _, tc := range []struct {
		name, doc, wantErr string
	}{
		{"negative", `{"projection":{"writers":-1}}`, "projection.writers must be at least 1, got -1"},
		{"at the default pool", `{"projection":{"writers":20}}`,
			"projection.writers is 20, but the connection pool holds 20; keep it below 20 so reads are not starved"},
		{"below the default pool", `{"projection":{"writers":19}}`, ""},
		// A pool set without setting writers runs the default writers on it. The default of 10
		// stops a service whose pool is 10 or smaller, naming the writers and the pool; before
		// the default was raised from 5, pools of 6 to 10 started, and the release notes say so.
		{"default writers on a pool of 11", `{"rdbConfiguration":{"maxOpenConnections":11}}`, ""},
		{"default writers on a pool of 10", `{"rdbConfiguration":{"maxOpenConnections":10}}`,
			"projection.writers is 10, but the connection pool holds 10"},
		{"default writers on a small pool", `{"rdbConfiguration":{"maxOpenConnections":8}}`,
			"projection.writers is 10, but the connection pool holds 8"},
		{"default writers on a pool of 5", `{"rdbConfiguration":{"maxOpenConnections":5}}`,
			"projection.writers is 10, but the connection pool holds 5"},
		// The remedy the release notes give for a small pool: set the writers below it.
		{"writers set below a small pool", `{"rdbConfiguration":{"maxOpenConnections":8},"projection":{"writers":5}}`, ""},
		{"negative maxBatch", `{"projection":{"maxBatch":-1}}`, "projection.maxBatch must be between 1 and 64, got -1"},
		{"maxBatch above the cap", `{"projection":{"maxBatch":65}}`, "projection.maxBatch must be between 1 and 64, got 65"},
		{"maxBatch at the cap", `{"projection":{"maxBatch":64}}`, ""},
		{"maxBatch of one", `{"projection":{"maxBatch":1}}`, ""},
		{"negative lingerMillis", `{"projection":{"lingerMillis":-1}}`, "projection.lingerMillis must be between 0 and 1000, got -1"},
		{"lingerMillis above the cap", `{"projection":{"lingerMillis":1001}}`, "projection.lingerMillis must be between 0 and 1000, got 1001"},
		{"lingerMillis at the cap", `{"projection":{"lingerMillis":1000}}`, ""},
		{"a misspelled key", `{"projection":{"workers":8}}`, `unknown field "workers"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := core.LoadConfiguration([]byte(tc.doc), &DeviceStateConfiguration{})
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
