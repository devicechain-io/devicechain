// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/devicechain-io/dc-microservice/ledger"
	"github.com/stretchr/testify/require"
)

func TestSettlementTasksAreNotRegisteredWhileUnserved(t *testing.T) {
	require.False(t, ledger.Served)
	require.NoError(t, initFleetSettlement())
	require.Nil(t, FleetSettlement)
	require.Nil(t, FleetSettlementDeadline)
}
