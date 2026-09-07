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
// bound, so it would survive BOTH the defaulting hook and the upper-bound check in Validate.
//
// It is defence in depth rather than the only defence — governance.RestrictedCommandLimit
// applies the same negated-positive test and would land on the platform reserve anyway — and
// it is not reachable through the deployed config path: the chart serializes each area's
// config with toJson and LoadConfiguration decodes it with encoding/json, and JSON has no
// NaN literal at all. (YAML does have one, `.nan`; the config document simply is not YAML by
// the time it arrives.) A config struct built in Go is reachable from more than that path,
// which is what this guards.
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

// The sweep cadence gets the same fail-safe direction as everything above it: the
// harmless-looking value lands on the default. Zero seconds is not "as fast as possible",
// and a sweep that never runs stops expiring commands as well as dispatching them, so
// there is deliberately no configured value meaning "off".
func TestSweepIntervalFlooredWhenNonPositive(t *testing.T) {
	empty := &CommandDeliveryConfiguration{}
	assert.NoError(t, core.LoadConfiguration([]byte(``), empty))
	assert.Equal(t, DefaultSweepIntervalSeconds, empty.SweepIntervalSeconds)

	for _, v := range []int{0, -1} {
		cfg := &CommandDeliveryConfiguration{SweepIntervalSeconds: v}
		cfg.ApplyDefaults()
		assert.Equal(t, DefaultSweepIntervalSeconds, cfg.SweepIntervalSeconds,
			"a missing or zero interval must mean the platform default, NEVER a zero-second tick")
	}
}

// Bounded at BOTH ends, because the two ends refuse different mistakes: below the floor the
// pass becomes a spin against the advisory lock, and above the ceiling it delays expiry —
// not merely dispatch — so a command's terminal state drifts with it.
func TestSweepIntervalValidation(t *testing.T) {
	kept := &CommandDeliveryConfiguration{DefaultCommandTTLSeconds: 3600, SweepIntervalSeconds: 5}
	kept.ApplyDefaults()
	assert.Equal(t, 5, kept.SweepIntervalSeconds, "a configured interval must survive defaulting")
	assert.NoError(t, kept.Validate())

	// 🔴 BOTH ENDPOINTS MUST BE ACCEPTED, and this is not pedantry: the bounds are
	// documented inclusive, and the CEILING is the value StrandedSentGrace derives from,
	// so an operator setting exactly the documented maximum being refused would be a
	// contradiction between the knob and the constant built on it. An off-by-one in
	// either comparison passes every other test in this file.
	for _, endpoint := range []int{MinSweepIntervalSeconds, MaxSweepIntervalSeconds} {
		cfg := &CommandDeliveryConfiguration{
			DefaultCommandTTLSeconds: 3600, SweepIntervalSeconds: endpoint,
		}
		cfg.ApplyDefaults()
		assert.Equal(t, endpoint, cfg.SweepIntervalSeconds)
		assert.NoError(t, cfg.Validate(), "the documented bounds are INCLUSIVE; %d was refused", endpoint)
	}

	// 🔑 THE FLOOR CHECK IS ONLY REACHABLE BECAUSE THE FLOOR IS ABOVE 1. ApplyDefaults maps
	// every non-positive value onto the default, so Validate's lower bound can fire only
	// for a value between 1 and the floor. With a floor of 1 that range is empty and this
	// assertion would be guarding nothing -- which is what an earlier version of this test
	// admitted by wrapping itself in `if MinSweepIntervalSeconds > 1`, a condition that was
	// false. A test that skips itself is not a test.
	tooFast := &CommandDeliveryConfiguration{
		DefaultCommandTTLSeconds: 3600, SweepIntervalSeconds: MinSweepIntervalSeconds - 1,
	}
	tooFast.ApplyDefaults()
	assert.Equal(t, MinSweepIntervalSeconds-1, tooFast.SweepIntervalSeconds,
		"a positive sub-floor value must survive defaulting so Validate is the thing that refuses it")
	assert.Error(t, tooFast.Validate())

	tooSlow := &CommandDeliveryConfiguration{
		DefaultCommandTTLSeconds: 3600, SweepIntervalSeconds: MaxSweepIntervalSeconds + 1,
	}
	tooSlow.ApplyDefaults()
	assert.Error(t, tooSlow.Validate(),
		"an over-long interval delays expiry, not just dispatch, and is refused rather than clamped")
}

// 🔴 THE KNOB MUST REJECT WHAT THE TYPED LOADER REJECTS. Fail-closed is the repo rule, and
// a config key that only exists in the struct would be silently ignored by an operator's
// YAML -- the value would read as applied and the default would still be in force.
func TestSweepKeysAreAcceptedFromYaml(t *testing.T) {
	cfg := &CommandDeliveryConfiguration{}
	assert.NoError(t, core.LoadConfiguration([]byte(`{"sweepIntervalSeconds": 5}`), cfg))
	assert.Equal(t, 5, cfg.SweepIntervalSeconds, "sweepIntervalSeconds did not reach the struct")
	assert.NoError(t, cfg.Validate())
}
