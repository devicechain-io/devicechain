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

// The location retention override is UNSET by default: nil means "inherit
// RetentionDays", which is what keeps an operator who never sets it on exactly the
// previous behavior. A defaulting hook that filled it in would erase the
// distinction between "inherit" and "an explicit window that happens to match".
func TestLocationRetentionUnsetByDefault(t *testing.T) {
	cfg := &EventManagementConfiguration{}
	err := core.LoadConfiguration([]byte(``), cfg)

	assert.NoError(t, err)
	assert.Nil(t, cfg.Lifecycle.LocationRetentionDays,
		"an unset location override must stay nil so location inherits the uniform window")
}

// An explicit location window survives loading, independently of the uniform one.
func TestLoadExplicitLocationRetention(t *testing.T) {
	cfg := &EventManagementConfiguration{}
	err := core.LoadConfiguration([]byte(`{"Lifecycle":{"RetentionDays":365,"LocationRetentionDays":30}}`), cfg)

	assert.NoError(t, err)
	assert.Equal(t, 365, cfg.Lifecycle.RetentionDays)
	if assert.NotNil(t, cfg.Lifecycle.LocationRetentionDays) {
		assert.Equal(t, 30, *cfg.Lifecycle.LocationRetentionDays)
	}
}

// An explicit 0 is a real setting — retention off for location alone — and must not
// be re-read as "unset".
func TestExplicitZeroLocationRetentionIsNotUnset(t *testing.T) {
	cfg := &EventManagementConfiguration{}
	err := core.LoadConfiguration([]byte(`{"Lifecycle":{"RetentionDays":365,"LocationRetentionDays":0}}`), cfg)

	assert.NoError(t, err)
	if assert.NotNil(t, cfg.Lifecycle.LocationRetentionDays, "an explicit 0 must not collapse to unset") {
		assert.Equal(t, 0, *cfg.Lifecycle.LocationRetentionDays)
	}
}

// A negative location window is rejected at startup, the same fail-closed posture
// the uniform windows already have.
func TestNegativeLocationRetentionRejected(t *testing.T) {
	cfg := &EventManagementConfiguration{}
	err := core.LoadConfiguration([]byte(`{"Lifecycle":{"LocationRetentionDays":-1}}`), cfg)
	assert.Error(t, err, "a negative location retention window must be rejected")
}

// Typed config fails closed on an unknown key, so a misspelled override cannot be
// silently ignored — which would leave position on the uniform window while the
// operator believes it is shortened.
func TestMisspelledLocationRetentionRejected(t *testing.T) {
	cfg := &EventManagementConfiguration{}
	err := core.LoadConfiguration([]byte(`{"Lifecycle":{"LocationRetentionDay":30}}`), cfg)
	assert.Error(t, err, "an unknown lifecycle key must be rejected at startup")
}
