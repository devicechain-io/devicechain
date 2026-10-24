// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package governance

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The dimension is in the platform's vocabulary: registered (so user-management derives
// its tier keys and the console its editor from it) and spelled the way the spec names it.
func TestProvisioningDimension_IsRegisteredWithItsWireFields(t *testing.T) {
	assert.Contains(t, AllDimensions(), Provisioning)
	assert.Equal(t, "provisioning", Provisioning.Name)
	assert.Equal(t, "provisioningRegistrationsPerSecond", Provisioning.RateField)
	assert.Equal(t, "provisioningBurst", Provisioning.BurstField)
	assert.Equal(t, `query { tenantGovernance { provisioningRegistrationsPerSecond provisioningBurst } }`,
		governanceQuery(Provisioning))
	assert.NotEmpty(t, Provisioning.Label)
	assert.NotEmpty(t, Provisioning.RateUnit)
}

// The platform default is the number the design names, it is metered per second, and it
// is a real ceiling: positive on both axes.
func TestDefaultProvisioningLimits_AreTheDesignedCeiling(t *testing.T) {
	d := DefaultProvisioningLimits()
	assert.Equal(t, Limits{RatePerSecond: 20, Burst: 500}, d)
	assert.Equal(t, d, floorLimits(d), "the default must survive the floor unchanged: it is already a live ceiling")
}

// An override on the wire wins over the platform default, per field; null inherits it.
func TestResolveLimits_ProvisioningOverrideThenDefault(t *testing.T) {
	def := DefaultProvisioningLimits()

	got, floored := resolveLimits(map[string]json.RawMessage{
		"provisioningRegistrationsPerSecond": raw("2.5"),
		"provisioningBurst":                  raw("30"),
	}, def, Provisioning)
	assert.Equal(t, Limits{RatePerSecond: 2.5, Burst: 30}, got, "override wins")
	assert.Empty(t, floored)

	got, _ = resolveLimits(map[string]json.RawMessage{
		"provisioningRegistrationsPerSecond": raw("2.5"),
		"provisioningBurst":                  raw("null"),
	}, def, Provisioning)
	assert.Equal(t, Limits{RatePerSecond: 2.5, Burst: 500}, got, "each field falls back on its own")

	got, floored = resolveLimits(map[string]json.RawMessage{
		"provisioningRegistrationsPerSecond": raw("null"),
		"provisioningBurst":                  raw("null"),
	}, def, Provisioning)
	assert.Equal(t, def, got, "no override inherits the platform default")
	assert.Empty(t, floored)
}

// An unusable stored override is floored to the default and reported, never a zero bucket.
func TestResolveLimits_ProvisioningUnusableOverrideFallsToDefault(t *testing.T) {
	got, floored := resolveLimits(map[string]json.RawMessage{
		"provisioningRegistrationsPerSecond": raw("0"),
		"provisioningBurst":                  raw("-4"),
	}, DefaultProvisioningLimits(), Provisioning)
	assert.Equal(t, DefaultProvisioningLimits(), got)
	assert.ElementsMatch(t, []string{"provisioningRegistrationsPerSecond", "provisioningBurst"}, floored)
}

// Another dimension's override must not govern registrations.
func TestResolveLimits_ProvisioningIgnoresOtherDimensions(t *testing.T) {
	got, _ := resolveLimits(map[string]json.RawMessage{
		"ingestReadingsPerSecond": raw("1"), "ingestBurst": raw("1"),
		"outboundCallsPerSecond": raw("1"), "outboundBurst": raw("1"),
	}, DefaultProvisioningLimits(), Provisioning)
	assert.Equal(t, DefaultProvisioningLimits(), got)
}

// The resolver as a limiter sees it: a cold tenant is metered at the platform default, a
// fetched override replaces it, and an unreachable authority leaves it at the default —
// metered, never unlimited.
func TestProvisioningResolver_DefaultOverrideAndFailOpen(t *testing.T) {
	f := &fakeFetcher{result: Limits{RatePerSecond: 3, Burst: 7}}
	r := NewTenantLimitResolver(f, DefaultProvisioningLimits(), Provisioning.Name)

	rps, burst := pair(r.Ceiling("acme"))
	assert.Equal(t, 20.0, rps, "cold tenant is metered at the platform default")
	assert.Equal(t, 500, burst)
	require.Eventually(t, func() bool {
		rps, burst := pair(r.Ceiling("acme"))
		return rps == 3 && burst == 7
	}, time.Second, 5*time.Millisecond, "the fetched override takes over")

	down := &fakeFetcher{err: errors.New("user-management unreachable")}
	rd := NewTenantLimitResolver(down, DefaultProvisioningLimits(), Provisioning.Name)
	for i := 0; i < 3; i++ {
		rps, burst := pair(rd.Ceiling("acme"))
		assert.Equal(t, 20.0, rps, "an unreachable authority must not unmeter the tenant")
		assert.Equal(t, 500, burst)
		time.Sleep(10 * time.Millisecond)
	}
}
