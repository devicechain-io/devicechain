// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"testing"

	mscfg "github.com/devicechain-io/dc-microservice/config"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBackfillDefaults(t *testing.T) {
	cfg := &EventManagementConfiguration{}
	require.NoError(t, core.LoadConfiguration([]byte(``), cfg))
	assert.True(t, cfg.CalculatedBackfill.On(), "the worker is on unless switched off")
	assert.Equal(t, DefaultBackfillRowsPerSecond, cfg.CalculatedBackfill.RowsPerSecond)
}

func TestBackfillCanBeSwitchedOffAndRated(t *testing.T) {
	cfg := &EventManagementConfiguration{}
	require.NoError(t, core.LoadConfiguration([]byte(`{"CalculatedBackfill":{"Enabled":false,"RowsPerSecond":500}}`), cfg))
	assert.False(t, cfg.CalculatedBackfill.On())
	assert.Equal(t, 500, cfg.CalculatedBackfill.RowsPerSecond)
}

// An unknown key is refused at startup, as everywhere in this configuration: a mistyped setting
// must not leave the worker running at a rate nobody chose.
func TestBackfillRefusesUnknownKeysAndBadRates(t *testing.T) {
	for _, doc := range []string{
		`{"CalculatedBackfill":{"RowsPerSecnd":500}}`,
		`{"CalculatedBackfill":{"RowsPerSecond":-1}}`,
		`{"CalculatedBackfill":{"RowsPerSecond":200001}}`,
		`{"CalculatedBackfill":{"Workers":4}}`,
		`{"CalculatedBackfill":{"Enabled":"yes"}}`,
	} {
		cfg := &EventManagementConfiguration{}
		assert.Error(t, core.LoadConfiguration([]byte(doc), cfg), doc)
	}
	// The bounds themselves are allowed.
	for _, doc := range []string{`{"CalculatedBackfill":{"RowsPerSecond":1}}`, `{"CalculatedBackfill":{"RowsPerSecond":200000}}`} {
		cfg := &EventManagementConfiguration{}
		assert.NoError(t, core.LoadConfiguration([]byte(doc), cfg), doc)
	}
}

// The worker holds one pooled connection for its lock, its batches and its refreshes, so the
// writers plus that one plus the headroom must fit the pool; a service that cannot is refused at
// startup naming the settings, unless the worker is off.
func TestBackfillMustFitThePoolBesideTheWriters(t *testing.T) {
	load := func(doc string) error {
		return core.LoadConfiguration([]byte(doc), &EventManagementConfiguration{})
	}
	// The defaults (10 writers, a pool of 20) leave room: 10 + 1 + 4 = 15.
	assert.NoError(t, load(``))
	// 15 writers on the default pool of 20 is 15 + 1 + 4 = 20, which fits exactly.
	assert.NoError(t, load(`{"Persistence":{"Writers":15}}`))
	// 16 pass the writers' own bound (below the pool) and fail with the worker beside them.
	err := load(`{"Persistence":{"Writers":16}}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "persistence.writers")
	assert.Contains(t, err.Error(), "calculatedBackfill.enabled")
	// Off, they fit again.
	assert.NoError(t, load(`{"Persistence":{"Writers":16},"CalculatedBackfill":{"Enabled":false}}`))
	// A bigger pool takes them.
	assert.NoError(t, load(`{"Persistence":{"Writers":16},"TsdbConfiguration":{"MaxOpenConnections":30}}`))
}

func TestBackfillValidateOnAHandBuiltValue(t *testing.T) {
	on := true
	b := CalculatedBackfillConfiguration{Enabled: &on, RowsPerSecond: 100}
	assert.NoError(t, b.Validate(mscfg.MicroserviceDatastoreConfiguration{MaxOpenConnections: 20},
		PersistenceConfiguration{Writers: 10}))
	assert.Error(t, b.Validate(mscfg.MicroserviceDatastoreConfiguration{MaxOpenConnections: 14},
		PersistenceConfiguration{Writers: 10}), "10 + 1 + 4 does not fit 14")
	assert.Error(t, CalculatedBackfillConfiguration{}.Validate(mscfg.MicroserviceDatastoreConfiguration{},
		PersistenceConfiguration{Writers: 1}), "a rate of zero is not a rate")
}
