// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"slices"
	"testing"

	"github.com/devicechain-io/dc-microservice/kv"
	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The areas with a login are the areas that use the broker. The catalog says which those are: every
// area that writes or reads a stream, or opens a bucket, must have a login, or it would start with
// none (failing closed) or never be given one. mcp and ai-inference are the exemptions, and this is
// the check that they really open neither.
func TestEveryAreaThatUsesTheBrokerHasALoginAndTheExemptOnesUseNone(t *testing.T) {
	using := map[string]bool{}
	for _, s := range streams.All {
		for _, a := range streams.WritersOf(s.Suffix) {
			using[a] = true
		}
		for _, r := range streams.ReadersOf(s.Suffix) {
			using[r.Area] = true
		}
		for _, a := range streams.AdminsOf(s.Suffix) {
			using[a] = true
		}
	}
	for _, b := range kv.All {
		for _, a := range b.Writers {
			using[a] = true
		}
		for _, a := range b.Readers {
			using[a] = true
		}
	}
	require.NotEmpty(t, using, "the catalog named no area, so this check would pass for anything")
	for a := range using {
		assert.True(t, NeedsNatsLogin(a), "%s uses the broker and has no login", a)
	}
	for _, a := range natsLoginExemptAreas {
		assert.False(t, using[a], "%s is exempt from a broker login and uses the broker", a)
		assert.True(t, streams.IsPlatformArea(a), "%s is exempt but is not a platform area: a typo here exempts nothing", a)
	}
	// Every login area is a platform area, and the list is sorted and has no duplicates.
	areas := NatsLoginAreas()
	assert.True(t, slices.IsSorted(areas))
	assert.Len(t, slices.Compact(slices.Clone(areas)), len(areas))
	assert.Equal(t, append(slices.Clone(areas), NatsAdminPrincipal), NatsLoginPrincipals())
}

func TestApplyAreaLoginOverridesOnlyWhenTheBrokerHasTheLogins(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	own := map[string]string{EnvNatsUser: "dc-device-state-g1", EnvNatsPassword: "pw"}

	t.Run("the flag off leaves the shared login alone", func(t *testing.T) {
		a := NatsAuthConfiguration{User: "dc_service", Password: "shared"}
		require.NoError(t, a.ApplyAreaLogin("device-state", env(own)))
		assert.Equal(t, NatsAuthConfiguration{User: "dc_service", Password: "shared"}, a)
	})
	t.Run("the flag on replaces it with the area's own", func(t *testing.T) {
		a := NatsAuthConfiguration{User: "dc_service", Password: "shared", AreaLogins: true}
		require.NoError(t, a.ApplyAreaLogin("device-state", env(own)))
		assert.Equal(t, "dc-device-state-g1", a.User)
		assert.Equal(t, "pw", a.Password)
	})
	t.Run("either half missing is refused", func(t *testing.T) {
		for _, m := range []map[string]string{
			{}, {EnvNatsUser: "dc-device-state-g1"}, {EnvNatsPassword: "pw"},
		} {
			a := NatsAuthConfiguration{User: "dc_service", Password: "shared", AreaLogins: true}
			require.Error(t, a.ApplyAreaLogin("device-state", env(m)))
			assert.Equal(t, "shared", a.Password, "a refused apply must not have touched the shared login")
		}
	})
	t.Run("another area's login is refused", func(t *testing.T) {
		a := NatsAuthConfiguration{AreaLogins: true}
		err := a.ApplyAreaLogin("device-state", env(map[string]string{EnvNatsUser: "dc-event-sources-g1", EnvNatsPassword: "pw"}))
		require.Error(t, err)
	})
	t.Run("a later generation of its own login is accepted", func(t *testing.T) {
		a := NatsAuthConfiguration{AreaLogins: true}
		require.NoError(t, a.ApplyAreaLogin("device-state", env(map[string]string{EnvNatsUser: "dc-device-state-g2", EnvNatsPassword: "pw"})))
	})
	t.Run("the exempt areas are not held to one", func(t *testing.T) {
		a := NatsAuthConfiguration{User: "dc_service", Password: "shared", AreaLogins: true}
		require.NoError(t, a.ApplyAreaLogin("mcp", env(nil)))
		assert.Equal(t, "shared", a.Password)
	})
}
