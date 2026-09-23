// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-user-management/config"
	"github.com/devicechain-io/dc-user-management/identity"
)

// The seed password reaches the identity manager through bootstrapConfig, which is the
// function afterMicroserviceInitialized calls — so this drives the real path: the
// configuration document is loaded the way parseConfiguration loads it, and the
// environment variable the chart projects is set or unset around it.
//
// 🔴 IT ASSERTS THE VALUE IN BOTH BRANCHES. An "unset" branch that only checked for no
// error would pass over a default re-introduced anywhere on this path, which is the
// exact defect this replaced: the literal `devicechain` was filled in by the config
// defaults, so the load never failed and the superuser was always seeded.
func TestTheSeedPasswordComesFromTheEnvironmentAndNothingElse(t *testing.T) {
	load := func(t *testing.T, doc string) *config.UserManagementConfiguration {
		t.Helper()
		cfg := &config.UserManagementConfiguration{}
		require.NoError(t, core.LoadConfiguration([]byte(doc), cfg))
		return cfg
	}

	t.Run("set", func(t *testing.T) {
		t.Setenv(config.SuperuserPasswordEnv, "minted-by-dcctl")
		got := bootstrapConfig(load(t, ``))
		assert.Equal(t, "minted-by-dcctl", got.SuperuserPassword)
		assert.Equal(t, "superuser@devicechain.local", got.SuperuserEmail)
	})

	t.Run("unset", func(t *testing.T) {
		t.Setenv(config.SuperuserPasswordEnv, "")
		for _, doc := range []string{``, `{}`, `{"auth":{}}`} {
			got := bootstrapConfig(load(t, doc))
			assert.Equal(t, "", got.SuperuserPassword,
				"with nothing supplied the seed password must be blank, never a default (document %q)", doc)
		}
	})

	t.Run("the variable is the one the chart projects", func(t *testing.T) {
		// A literal on purpose: a rename of the constant would otherwise move both
		// sides of this comparison, and the chart's template spells it independently.
		assert.Equal(t, "DC_SUPERUSER_PASSWORD", config.SuperuserPasswordEnv)
	})
}

// The refusal an operator reads names the variable and where it normally comes from,
// and anything else passes through untouched.
func TestTheSeedRefusalSaysWhatToSet(t *testing.T) {
	err := explainSeedRefusal(identity.ErrNoSuperuserSeedPassword)
	require.ErrorIs(t, err, identity.ErrNoSuperuserSeedPassword)
	for _, want := range []string{"DC_SUPERUSER_PASSWORD", "dci-<instance id>-superuser", "instance.superuserSecret"} {
		assert.True(t, strings.Contains(err.Error(), want), "the refusal does not mention %q: %v", want, err)
	}

	other := errors.New("something else")
	assert.Same(t, other, explainSeedRefusal(other))
}
