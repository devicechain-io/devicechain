// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package core

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An area's own broker login is applied where the instance document is LOADED, so every connection
// the process opens presents it. These go through LoadInstanceConfigurationFrom, the function a
// service calls at startup, because what has to be gated is the wiring: a test of the config
// method passes just as happily with this call missing from the load path.

const areaLoginsDoc = `{
  "infrastructure": {
    "nats": {"hostname": "dc-nats.dc-system", "port": 4222,
             "auth": {"user": "dc_service", "password": "the-shared-one", "areaLogins": true}},
    "userManagement": {"hostname": "user-management", "port": 8080}
  }
}`

func TestTheLoadedDocumentPresentsTheAreasOwnLogin(t *testing.T) {
	t.Setenv(ENV_MS_FUNCTIONAL_AREA, "device-state")
	t.Setenv(config.EnvNatsUser, "dc-device-state-g1")
	t.Setenv(config.EnvNatsPassword, "its-own")

	ms := &Microservice{}
	require.NoError(t, ms.LoadInstanceConfigurationFrom(instanceDoc(t, areaLoginsDoc)))
	auth := ms.InstanceConfiguration.Infrastructure.Nats.Auth
	assert.Equal(t, "dc-device-state-g1", auth.User)
	assert.Equal(t, "its-own", auth.Password)
}

// 🔴 THE FAIL-CLOSED HALF: an area that has no login of its own does not start as the shared one.
func TestAnAreaWithoutItsLoginDoesNotStartOnTheSharedOne(t *testing.T) {
	t.Setenv(ENV_MS_FUNCTIONAL_AREA, "device-state")
	t.Setenv(config.EnvNatsUser, "")
	t.Setenv(config.EnvNatsPassword, "")

	ms := &Microservice{}
	err := ms.LoadInstanceConfigurationFrom(instanceDoc(t, areaLoginsDoc))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "device-state")
	assert.Empty(t, ms.InstanceConfiguration.Infrastructure.Nats.Auth.Password,
		"a refused load must not leave a usable configuration behind")
}

// ...and the areas that never connect are not held to one: mcp and ai-inference mount no Secret.
func TestAnAreaThatNeverConnectsNeedsNoLogin(t *testing.T) {
	for _, area := range []string{"mcp", "ai-inference"} {
		t.Run(area, func(t *testing.T) {
			t.Setenv(ENV_MS_FUNCTIONAL_AREA, area)
			t.Setenv(config.EnvNatsUser, "")
			t.Setenv(config.EnvNatsPassword, "")

			ms := &Microservice{}
			require.NoError(t, ms.LoadInstanceConfigurationFrom(instanceDoc(t, areaLoginsDoc)))
		})
	}
}

// Without the flag nothing changes: an instance built before the logins keeps the shared one, even
// if a stray login is in the environment.
func TestWithoutTheFlagTheSharedLoginStands(t *testing.T) {
	t.Setenv(ENV_MS_FUNCTIONAL_AREA, "device-state")
	t.Setenv(config.EnvNatsUser, "dc-device-state-g1")
	t.Setenv(config.EnvNatsPassword, "its-own")

	ms := &Microservice{}
	require.NoError(t, ms.LoadInstanceConfigurationFrom(instanceDoc(t, minimalInstanceDoc)))
	assert.Empty(t, ms.InstanceConfiguration.Infrastructure.Nats.Auth.User)
}
