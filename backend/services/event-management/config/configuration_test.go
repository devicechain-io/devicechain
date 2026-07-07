// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/assert"
)

// Loading an empty document succeeds and applies the data-lifecycle defaults
// (ADR-026): 24h chunks, compression on after 7 days, retention off.
func TestLoadEmptyConfiguration(t *testing.T) {
	cfg := &EventManagementConfiguration{}
	err := core.LoadConfiguration([]byte(``), cfg)

	assert.NoError(t, err)
	assert.Equal(t, DefaultChunkIntervalHours, cfg.Lifecycle.ChunkIntervalHours)
	if assert.NotNil(t, cfg.Lifecycle.CompressAfterDays) {
		assert.Equal(t, DefaultCompressAfterDays, *cfg.Lifecycle.CompressAfterDays)
	}
	assert.Equal(t, 0, cfg.Lifecycle.RetentionDays, "retention must default to off")
}

// Explicit lifecycle values survive defaulting, and retention is honored when set.
func TestLoadExplicitLifecycle(t *testing.T) {
	cfg := &EventManagementConfiguration{}
	err := core.LoadConfiguration([]byte(`{"Lifecycle":{"ChunkIntervalHours":6,"CompressAfterDays":14,"RetentionDays":90}}`), cfg)

	assert.NoError(t, err)
	assert.Equal(t, 6, cfg.Lifecycle.ChunkIntervalHours)
	if assert.NotNil(t, cfg.Lifecycle.CompressAfterDays) {
		assert.Equal(t, 14, *cfg.Lifecycle.CompressAfterDays)
	}
	assert.Equal(t, 90, cfg.Lifecycle.RetentionDays)
}

// An explicit compressAfterDays of 0 means "disabled" and must NOT be re-defaulted
// to 7 — the nil pointer is what triggers defaulting, not the zero value.
func TestExplicitZeroCompressionDisables(t *testing.T) {
	cfg := &EventManagementConfiguration{}
	err := core.LoadConfiguration([]byte(`{"Lifecycle":{"CompressAfterDays":0}}`), cfg)

	assert.NoError(t, err)
	if assert.NotNil(t, cfg.Lifecycle.CompressAfterDays) {
		assert.Equal(t, 0, *cfg.Lifecycle.CompressAfterDays, "explicit 0 must stay disabled, not default to 7")
	}
}

// Negative values are rejected (fail closed) so a typo cannot produce a broken or
// data-destroying policy.
func TestNegativeLifecycleRejected(t *testing.T) {
	for _, doc := range []string{
		`{"Lifecycle":{"ChunkIntervalHours":-1}}`,
		`{"Lifecycle":{"CompressAfterDays":-1}}`,
		`{"Lifecycle":{"RetentionDays":-5}}`,
	} {
		cfg := &EventManagementConfiguration{}
		err := core.LoadConfiguration([]byte(doc), cfg)
		assert.Error(t, err, "should reject: %s", doc)
	}
}
