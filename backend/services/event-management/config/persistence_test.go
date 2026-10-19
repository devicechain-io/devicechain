// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func millis(n int) *int { return &n }

// The persistence settings load as written, and unset ones take their defaults: 10 writers,
// batches of up to 64, and a 10 ms linger. An explicit 0 linger is a setting of its own and
// survives the defaulting; a null one is unset.
func TestPersistenceSettingsLoad(t *testing.T) {
	assert.Equal(t, 10, DefaultPersistenceLingerMillis)

	cfg := &EventManagementConfiguration{}
	require.NoError(t, core.LoadConfiguration([]byte(`{"persistence":{"writers":8,"maxBatch":16,"lingerMillis":5}}`), cfg))
	assert.Equal(t, PersistenceConfiguration{Writers: 8, MaxBatch: 16, LingerMillis: millis(5)}, cfg.Persistence)
	assert.Equal(t, 5*time.Millisecond, cfg.Persistence.Linger())

	cfg = &EventManagementConfiguration{}
	require.NoError(t, core.LoadConfiguration([]byte(``), cfg))
	assert.Equal(t, PersistenceConfiguration{Writers: 10, MaxBatch: 64, LingerMillis: millis(10)}, cfg.Persistence)
	assert.Equal(t, 10*time.Millisecond, cfg.Persistence.Linger())

	cfg = &EventManagementConfiguration{}
	require.NoError(t, core.LoadConfiguration([]byte(`{"persistence":{"lingerMillis":0}}`), cfg))
	if assert.NotNil(t, cfg.Persistence.LingerMillis) {
		assert.Equal(t, 0, *cfg.Persistence.LingerMillis)
	}
	assert.Equal(t, time.Duration(0), cfg.Persistence.Linger())

	cfg = &EventManagementConfiguration{}
	require.NoError(t, core.LoadConfiguration([]byte(`{"persistence":{"lingerMillis":null}}`), cfg))
	assert.Equal(t, PersistenceConfiguration{Writers: 10, MaxBatch: 64, LingerMillis: millis(10)}, cfg.Persistence)
	assert.Equal(t, 10*time.Millisecond, cfg.Persistence.Linger())
}

// A value built in code and never defaulted reads an unset linger as the default, both
// when it is validated and when it is run, and its bound is checked on what it reads.
func TestPersistenceLingerBuiltInCodeTakesTheDefault(t *testing.T) {
	p := PersistenceConfiguration{Writers: 3, MaxBatch: 4}
	assert.Equal(t, 10*time.Millisecond, p.Linger())
	assert.NoError(t, p.Validate(config.MicroserviceDatastoreConfiguration{}))

	err := PersistenceConfiguration{Writers: 3, MaxBatch: 4, LingerMillis: millis(1001)}.Validate(config.MicroserviceDatastoreConfiguration{})
	if assert.Error(t, err) {
		assert.Contains(t, err.Error(), "persistence.lingerMillis must be between 0 and 1000, got 1001")
	}
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
		// A pool set without setting writers runs the default writers on it. The default of 10
		// stops a service whose pool is 10 or smaller, naming the writers and the pool; before
		// the default was raised from 5, pools of 6 to 10 started, and the release notes say so.
		{"default writers on a pool of 11", `{"tsdbConfiguration":{"maxOpenConnections":11}}`, ""},
		{"default writers on a pool of 10", `{"tsdbConfiguration":{"maxOpenConnections":10}}`,
			"persistence.writers is 10, but the connection pool holds 10"},
		{"default writers on a small pool", `{"tsdbConfiguration":{"maxOpenConnections":8}}`,
			"persistence.writers is 10, but the connection pool holds 8"},
		{"negative maxBatch", `{"persistence":{"maxBatch":-1}}`, "persistence.maxBatch must be between 1 and 64, got -1"},
		{"maxBatch above the cap", `{"persistence":{"maxBatch":65}}`, "persistence.maxBatch must be between 1 and 64, got 65"},
		// The cap is not a statement-size bound, and was kept at 64 on purpose: see writerbatch.MaxSize.
		{"maxBatch of 128", `{"persistence":{"maxBatch":128}}`, "persistence.maxBatch must be between 1 and 64, got 128"},
		{"maxBatch at the cap", `{"persistence":{"maxBatch":64}}`, ""},
		{"maxBatch of one", `{"persistence":{"maxBatch":1}}`, ""},
		{"negative lingerMillis", `{"persistence":{"lingerMillis":-1}}`, "persistence.lingerMillis must be between 0 and 1000, got -1"},
		{"lingerMillis above the cap", `{"persistence":{"lingerMillis":1001}}`, "persistence.lingerMillis must be between 0 and 1000, got 1001"},
		{"lingerMillis at the cap", `{"persistence":{"lingerMillis":1000}}`, ""},
		{"lingerMillis zero", `{"persistence":{"lingerMillis":0}}`, ""},
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
