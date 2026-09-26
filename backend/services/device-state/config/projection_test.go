// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// projection.writers loads as written and defaults to 5.
func TestProjectionWritersLoad(t *testing.T) {
	cfg := &DeviceStateConfiguration{}
	require.NoError(t, core.LoadConfiguration([]byte(`{"projection":{"writers":8}}`), cfg))
	assert.Equal(t, 8, cfg.Projection.Writers)

	cfg = &DeviceStateConfiguration{}
	require.NoError(t, core.LoadConfiguration([]byte(``), cfg))
	assert.Equal(t, DefaultProjectionWriters, cfg.Projection.Writers)
	assert.Equal(t, 5, DefaultProjectionWriters)
}

// projection.writers is bounded by the relational pool it draws from — 20 unless
// rdbConfiguration sets one — and an out-of-range value stops the service, naming it.
func TestProjectionWritersAreBounded(t *testing.T) {
	for _, tc := range []struct {
		name, doc, wantErr string
	}{
		{"negative", `{"projection":{"writers":-1}}`, "projection.writers must be at least 1, got -1"},
		{"at the default pool", `{"projection":{"writers":20}}`,
			"projection.writers is 20, but the connection pool holds 20; keep it below 20 so reads are not starved"},
		{"below the default pool", `{"projection":{"writers":19}}`, ""},
		{"default writers on a small pool", `{"rdbConfiguration":{"maxOpenConnections":8}}`, ""},
		{"default writers on a pool of 5", `{"rdbConfiguration":{"maxOpenConnections":5}}`,
			"projection.writers is 5, but the connection pool holds 5"},
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
