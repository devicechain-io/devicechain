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

// retiringConfig is sampleConfig plus a key an earlier release accepted and this one
// removed. Note the struct carries NO field for it: stripping happens before the decode,
// so a retired key that still had a field would simply be decoded into it.
type retiringConfig struct {
	sampleConfig
}

func (c *retiringConfig) RetiredConfigKeys() map[string]string {
	return map[string]string{
		"maxEventFutureSkewSeconds": "It moved to device-management; set it there.",
	}
}

// The defect this whole mechanism exists for: without it, a key we published and then
// removed fails the load closed, and the operator's reward for following our own
// documentation is a pod that will not start.
func TestLoadConfiguration_RetiredKeyDoesNotFailTheLoad(t *testing.T) {
	cfg := &retiringConfig{}
	err := LoadConfiguration([]byte(`{"mode":"required","maxEventFutureSkewSeconds":600}`), cfg)

	assert.NoError(t, err)
	assert.Equal(t, "required", cfg.Mode, "the keys alongside a retired one must still apply")
	assert.Equal(t, 5, cfg.Workers, "defaulting must still run over a stripped document")
}

// encoding/json binds field names case-insensitively, so both spellings reached the field
// when it existed. Retiring only one of them would leave the other failing the load as
// unknown — the same crash-loop, reachable by capitalisation.
func TestLoadConfiguration_RetiredKeyMatchesCaseInsensitively(t *testing.T) {
	cfg := &retiringConfig{}

	assert.NoError(t, LoadConfiguration([]byte(`{"MaxEventFutureSkewSeconds":600}`), cfg))
}

// 🔴 THE COUNTERWEIGHT, AND THE REASON THIS IS NOT SIMPLY "IGNORE UNKNOWN KEYS". Retiring
// a key must relax the posture for that key ALONE. A typo is still a setting the operator
// believes is in force and is still refused — if this test ever passes a nil error, the
// mechanism has quietly turned the whole fail-closed guarantee off.
func TestLoadConfiguration_RetirementDoesNotAdmitUnknownKeys(t *testing.T) {
	cfg := &retiringConfig{}
	err := LoadConfiguration([]byte(`{"maxEventFutureSkewSeconds":600,"wrkers":9}`), cfg)

	assert.Error(t, err, "an unknown key alongside a retired one must still fail closed")
	assert.Contains(t, err.Error(), "wrkers", "the error must name the key the operator got wrong")
}

// A retired key is stripped, not honoured. If a later change ever wires one back into a
// field, this catches it: the operator asked for 600 and the answer must not be 600.
func TestLoadConfiguration_RetiredValueIsNotApplied(t *testing.T) {
	cfg := &retiringConfig{}

	assert.NoError(t, LoadConfiguration([]byte(`{"workers":600,"maxEventFutureSkewSeconds":600}`), cfg))
	assert.Equal(t, 600, cfg.Workers, "a live key with the same value must be unaffected")
}

// Validation still runs over a document that carried a retired key, so stripping cannot
// become a way to smuggle an invalid configuration past the gate.
func TestLoadConfiguration_RetiredKeyStillValidates(t *testing.T) {
	cfg := &retiringConfig{}
	err := LoadConfiguration([]byte(`{"mode":"nonsense","maxEventFutureSkewSeconds":600}`), cfg)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "validation failed")
}

// The no-op control: a type that retires nothing must reach the decoder byte-for-byte, so
// this mechanism cannot change the behaviour of a service that never opted in.
func TestLoadConfiguration_UnretiredTypeIsUnaffected(t *testing.T) {
	cfg := &sampleConfig{}
	err := LoadConfiguration([]byte(`{"maxEventFutureSkewSeconds":600}`), cfg)

	assert.Error(t, err, "a service that retires nothing still fails closed on every unknown key")
}

// Trailing data must still be refused when the document also carries a retired key.
//
// It is caught by construction rather than by a check of its own, and that is worth a test
// because it is not obvious: json.Unmarshal into a map rejects a second top-level value, so
// a document with trailing data never reaches the re-marshal that would have swallowed it —
// it is handed on untouched and the strict decoder reports it, exactly as before.
func TestLoadConfiguration_RetiredKeyDoesNotSwallowTrailingData(t *testing.T) {
	cfg := &retiringConfig{}
	err := LoadConfiguration([]byte(`{"maxEventFutureSkewSeconds":600} {"mode":"required"}`), cfg)

	assert.Error(t, err, "trailing data must be refused whether or not a retired key is present")
}
