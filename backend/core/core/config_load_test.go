// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

type sampleConfig struct {
	Mode    string
	Workers int
}

func (c *sampleConfig) ApplyDefaults() {
	if c.Mode == "" {
		c.Mode = "optional"
	}
	if c.Workers == 0 {
		c.Workers = 5
	}
}

func (c *sampleConfig) Validate() error {
	switch c.Mode {
	case "disabled", "optional", "required":
	default:
		return errors.New("invalid mode")
	}
	if c.Workers < 1 {
		return errors.New("workers must be positive")
	}
	return nil
}

// A well-formed document decodes and passes defaulting + validation.
func TestLoadConfiguration_Valid(t *testing.T) {
	cfg := &sampleConfig{}
	err := LoadConfiguration([]byte(`{"Mode":"required","Workers":3}`), cfg)

	assert.NoError(t, err)
	assert.Equal(t, "required", cfg.Mode)
	assert.Equal(t, 3, cfg.Workers)
}

// Unknown keys are rejected (a typo'd or stale setting fails the load).
func TestLoadConfiguration_UnknownFieldRejected(t *testing.T) {
	cfg := &sampleConfig{}
	err := LoadConfiguration([]byte(`{"Mode":"required","Nope":true}`), cfg)

	assert.Error(t, err)
}

// An empty document yields defaults, not an unvalidated zero value.
func TestLoadConfiguration_EmptyAppliesDefaults(t *testing.T) {
	cfg := &sampleConfig{}
	err := LoadConfiguration(nil, cfg)

	assert.NoError(t, err)
	assert.Equal(t, "optional", cfg.Mode)
	assert.Equal(t, 5, cfg.Workers)
}

// Defaults fill fields the document omits.
func TestLoadConfiguration_PartialDocumentDefaulted(t *testing.T) {
	cfg := &sampleConfig{}
	err := LoadConfiguration([]byte(`{"Mode":"disabled"}`), cfg)

	assert.NoError(t, err)
	assert.Equal(t, "disabled", cfg.Mode)
	assert.Equal(t, 5, cfg.Workers)
}

// Validation runs after defaulting and fails the load closed.
func TestLoadConfiguration_ValidationFails(t *testing.T) {
	cfg := &sampleConfig{}
	err := LoadConfiguration([]byte(`{"Mode":"bogus"}`), cfg)

	assert.Error(t, err)
}

// Malformed JSON is a load-time error.
func TestLoadConfiguration_MalformedJSON(t *testing.T) {
	cfg := &sampleConfig{}
	err := LoadConfiguration([]byte(`{"Mode":`), cfg)

	assert.Error(t, err)
}

// Trailing data after the document is rejected.
func TestLoadConfiguration_TrailingData(t *testing.T) {
	cfg := &sampleConfig{}
	err := LoadConfiguration([]byte(`{"Mode":"optional"} {"Mode":"required"}`), cfg)

	assert.Error(t, err)
}

// A target implementing neither interface still decodes strictly.
type plainConfig struct {
	Name string
}

func TestLoadConfiguration_NoInterfaces(t *testing.T) {
	cfg := &plainConfig{}
	err := LoadConfiguration([]byte(`{"Name":"x"}`), cfg)

	assert.NoError(t, err)
	assert.Equal(t, "x", cfg.Name)
}
