// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/stretchr/testify/assert"
)

// Loading an empty document succeeds and floors the command TTL to the platform
// default through the ADR-022 decision-1 defaulting hook — the fail-safe that keeps
// an unset value from disabling expiry (which would resurrect the stuck-in-SENT
// gap, ADR-075 L4b).
func TestLoadEmptyConfiguration(t *testing.T) {
	cfg := &CommandDeliveryConfiguration{}
	err := core.LoadConfiguration([]byte(``), cfg)

	assert.NoError(t, err)
	assert.Equal(t, DefaultCommandTTLSeconds, cfg.DefaultCommandTTLSeconds)
}

// A non-positive TTL is a misconfiguration that must not silently disable expiry:
// ApplyDefaults floors it to the platform default rather than leaving it at zero.
func TestDefaultCommandTTLFlooredWhenNonPositive(t *testing.T) {
	for _, v := range []int{0, -1} {
		cfg := &CommandDeliveryConfiguration{DefaultCommandTTLSeconds: v}
		cfg.ApplyDefaults()
		assert.Equal(t, DefaultCommandTTLSeconds, cfg.DefaultCommandTTLSeconds)
	}
}

// A caller-supplied positive TTL survives defaulting untouched, but one below the
// floor is rejected by Validate — a sub-minute horizon would expire commands before
// a device on a marginal radio could answer.
func TestDefaultCommandTTLValidation(t *testing.T) {
	kept := &CommandDeliveryConfiguration{DefaultCommandTTLSeconds: 3600}
	kept.ApplyDefaults()
	assert.Equal(t, 3600, kept.DefaultCommandTTLSeconds)
	assert.NoError(t, kept.Validate())

	tooSmall := &CommandDeliveryConfiguration{DefaultCommandTTLSeconds: MinCommandTTLSeconds - 1}
	assert.Error(t, tooSmall.Validate())
}
