// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/ledger"
	"github.com/stretchr/testify/require"
)

// The registry, not a test override, makes config.set executable: create accepts it on a served build.
func TestConfigSetIsRegisteredByThisArea(t *testing.T) {
	require.True(t, ledger.Served)
	require.True(t, ledger.Executable(FleetKindConfigSet))
	require.True(t, fleetKindExecutable(FleetKindConfigSet))
	require.False(t, fleetKindExecutable("custody.transfer"))
	require.Error(t, ledger.Register(fleetKind(FleetKindConfigSet)), "a second registration is refused")
}
