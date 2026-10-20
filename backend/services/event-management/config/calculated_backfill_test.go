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
	assert.Nil(t, cfg.CalculatedBackfill.Enabled, "unset stays unset: it decides differently from true")
	run, why, _ := cfg.CalculatedBackfill.Runs(cfg.TsdbConfiguration, cfg.Persistence)
	assert.True(t, run, "the worker runs on the defaults: %s", why)
	assert.Equal(t, DefaultBackfillRowsPerSecond, cfg.CalculatedBackfill.RowsPerSecond)
}

func TestBackfillCanBeSwitchedOffAndRated(t *testing.T) {
	cfg := &EventManagementConfiguration{}
	require.NoError(t, core.LoadConfiguration([]byte(`{"CalculatedBackfill":{"Enabled":false,"RowsPerSecond":500}}`), cfg))
	run, why, _ := cfg.CalculatedBackfill.Runs(cfg.TsdbConfiguration, cfg.Persistence)
	assert.False(t, run)
	assert.Contains(t, why, "calculatedBackfill.enabled is false")
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
// writers plus that one plus the headroom must fit the pool. 🔴 WHAT HAPPENS WHEN THEY DO NOT
// depends on who asked for the worker. An operator who set enabled: true is refused at startup
// naming the settings. An unset switch is the default, and an instance whose writers and pool
// were tuned before the worker existed must start exactly as it did: the worker stays off, and
// Runs says why (the startup log carries it).
func TestBackfillMustFitThePoolBesideTheWriters(t *testing.T) {
	load := func(doc string) (*EventManagementConfiguration, error) {
		cfg := &EventManagementConfiguration{}
		return cfg, core.LoadConfiguration([]byte(doc), cfg)
	}
	runs := func(doc string) (bool, string) {
		cfg, err := load(doc)
		require.NoError(t, err, doc)
		run, why, _ := cfg.CalculatedBackfill.Runs(cfg.TsdbConfiguration, cfg.Persistence)
		return run, why
	}
	// The defaults (10 writers, a pool of 20) leave room: 10 + 1 + 4 = 15.
	run, _ := runs(``)
	assert.True(t, run)
	// 15 writers on the default pool of 20 is 15 + 1 + 4 = 20, which fits exactly.
	run, _ = runs(`{"Persistence":{"Writers":15}}`)
	assert.True(t, run)
	_, err := load(`{"Persistence":{"Writers":15},"CalculatedBackfill":{"Enabled":true}}`)
	assert.NoError(t, err)

	// 16 to 19 writers on the default pool, and the default writers on pools of 11 to 14, all
	// started before the worker existed. Unset, they still start, with the worker off.
	for _, doc := range []string{
		`{"Persistence":{"Writers":16}}`,
		`{"Persistence":{"Writers":19}}`,
		`{"TsdbConfiguration":{"MaxOpenConnections":11}}`,
		`{"TsdbConfiguration":{"MaxOpenConnections":14}}`,
	} {
		run, why := runs(doc)
		assert.False(t, run, doc)
		assert.Contains(t, why, "persistence.writers", doc)
		assert.Contains(t, why, "tsdbConfiguration.maxOpenConnections", doc)
		assert.Contains(t, why, "calculatedBackfill.enabled", doc)
	}

	// Asked for explicitly, the same pool is refused, naming the settings.
	_, err = load(`{"Persistence":{"Writers":16},"CalculatedBackfill":{"Enabled":true}}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "calculatedBackfill.enabled is true")
	assert.Contains(t, err.Error(), "persistence.writers")
	_, err = load(`{"TsdbConfiguration":{"MaxOpenConnections":14},"CalculatedBackfill":{"Enabled":true}}`)
	require.Error(t, err)

	// Off, nothing is checked against the pool.
	run, _ = runs(`{"Persistence":{"Writers":16},"CalculatedBackfill":{"Enabled":false}}`)
	assert.False(t, run)
	// A bigger pool takes them, asked for or not.
	run, _ = runs(`{"Persistence":{"Writers":16},"TsdbConfiguration":{"MaxOpenConnections":30}}`)
	assert.True(t, run)
	_, err = load(`{"Persistence":{"Writers":16},"TsdbConfiguration":{"MaxOpenConnections":30},"CalculatedBackfill":{"Enabled":true}}`)
	assert.NoError(t, err)
}

func TestBackfillValidateOnAHandBuiltValue(t *testing.T) {
	on := true
	b := CalculatedBackfillConfiguration{Enabled: &on, RowsPerSecond: 100}
	assert.NoError(t, b.Validate(mscfg.MicroserviceDatastoreConfiguration{MaxOpenConnections: 20},
		PersistenceConfiguration{Writers: 10}))
	assert.Error(t, b.Validate(mscfg.MicroserviceDatastoreConfiguration{MaxOpenConnections: 14},
		PersistenceConfiguration{Writers: 10}), "10 + 1 + 4 does not fit 14")
	unset := CalculatedBackfillConfiguration{RowsPerSecond: 100}
	assert.NoError(t, unset.Validate(mscfg.MicroserviceDatastoreConfiguration{MaxOpenConnections: 14},
		PersistenceConfiguration{Writers: 10}), "unset is not refused for the pool")
	run, _, _ := unset.Runs(mscfg.MicroserviceDatastoreConfiguration{MaxOpenConnections: 14}, PersistenceConfiguration{Writers: 10})
	assert.False(t, run, "...but does not run on it")
	assert.Error(t, CalculatedBackfillConfiguration{}.Validate(mscfg.MicroserviceDatastoreConfiguration{},
		PersistenceConfiguration{Writers: 1}), "a rate of zero is not a rate")
}
