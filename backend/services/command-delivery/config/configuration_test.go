// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"math"
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

// The held-command ceiling gets the same fail-safe treatment as the TTL, and for the
// same reason in the opposite direction: an absent or zero value is the PLATFORM
// DEFAULT, never "unlimited". A governance bound whose missing value reads as no bound
// stops governing exactly when whatever carries it is unset or unreachable.
func TestHeldCommandCeilingFlooredWhenNonPositive(t *testing.T) {
	empty := &CommandDeliveryConfiguration{}
	assert.NoError(t, core.LoadConfiguration([]byte(``), empty))
	assert.Equal(t, DefaultHeldCommandCeiling, empty.HeldCommandCeiling)

	for _, v := range []int{0, -1} {
		cfg := &CommandDeliveryConfiguration{HeldCommandCeiling: v}
		cfg.ApplyDefaults()
		assert.Equal(t, DefaultHeldCommandCeiling, cfg.HeldCommandCeiling,
			"a missing or zero ceiling must mean the platform default, NEVER unlimited")
	}
}

// A configured ceiling survives defaulting untouched; one below the floor is refused.
// A handful of held commands per tenant is not a conservative setting — it refuses a
// sleeping fleet's backlog at enqueue, and the device never learns the commands existed.
func TestHeldCommandCeilingValidation(t *testing.T) {
	kept := &CommandDeliveryConfiguration{DefaultCommandTTLSeconds: 3600, HeldCommandCeiling: 250}
	kept.ApplyDefaults()
	assert.Equal(t, 250, kept.HeldCommandCeiling)
	assert.NoError(t, kept.Validate())

	tooSmall := &CommandDeliveryConfiguration{
		DefaultCommandTTLSeconds: 3600, HeldCommandCeiling: MinHeldCommandCeiling - 1,
	}
	assert.Error(t, tooSmall.Validate())
}

// The delivery machinery reserve gets the same fail-safe treatment as the ceiling above,
// in the same direction: an absent, zero or negative value is the PLATFORM RESERVE, never
// "no reserve". There is deliberately no configured value that switches the reserve off —
// the thing it protects is the platform's ability to deliver at all.
func TestDeliveryMachineryReserveFlooredWhenNonPositive(t *testing.T) {
	empty := &CommandDeliveryConfiguration{}
	assert.NoError(t, core.LoadConfiguration([]byte(``), empty))
	assert.Equal(t, DefaultDeliveryMachineryReserve, empty.DeliveryMachineryReserve)

	for _, v := range []float64{0, -0.5} {
		cfg := &CommandDeliveryConfiguration{DeliveryMachineryReserve: v}
		cfg.ApplyDefaults()
		assert.Equal(t, DefaultDeliveryMachineryReserve, cfg.DeliveryMachineryReserve,
			"a missing or zero reserve must mean the platform reserve, NEVER no reserve")
	}
}

// 🔴 NaN is the case the obvious `<= 0` spelling lets through: it compares false to every
// bound, so it would survive BOTH the defaulting hook and the upper-bound check in
// Validate, and the service would run on a reserve that makes every comparison downstream
// false. YAML has no NaN literal, but a float field is reachable from more than YAML.
func TestDeliveryMachineryReserveRejectsNaN(t *testing.T) {
	cfg := &CommandDeliveryConfiguration{DeliveryMachineryReserve: math.NaN()}
	cfg.ApplyDefaults()
	assert.Equal(t, DefaultDeliveryMachineryReserve, cfg.DeliveryMachineryReserve)
}

// A configured reserve survives defaulting untouched; one above the cap is REFUSED rather
// than clamped. Unlike an absent value, a reserve of 0.8 is an operator saying something
// they did not mean, and quietly substituting the default would leave them believing a
// split that is not in force.
func TestDeliveryMachineryReserveValidation(t *testing.T) {
	kept := &CommandDeliveryConfiguration{
		DefaultCommandTTLSeconds: 3600, HeldCommandCeiling: 250, DeliveryMachineryReserve: 0.3,
	}
	kept.ApplyDefaults()
	assert.Equal(t, 0.3, kept.DeliveryMachineryReserve)
	assert.NoError(t, kept.Validate())

	atTheCap := &CommandDeliveryConfiguration{
		DefaultCommandTTLSeconds: 3600, HeldCommandCeiling: 250,
		DeliveryMachineryReserve: MaxDeliveryMachineryReserve,
	}
	atTheCap.ApplyDefaults()
	assert.NoError(t, atTheCap.Validate(), "the cap itself is a legal setting")

	tooLarge := &CommandDeliveryConfiguration{
		DefaultCommandTTLSeconds: 3600, HeldCommandCeiling: 250,
		DeliveryMachineryReserve: MaxDeliveryMachineryReserve + 0.01,
	}
	tooLarge.ApplyDefaults()
	assert.Error(t, tooLarge.Validate())
}
