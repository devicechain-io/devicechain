// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestAlarmLedgerRefusesCommandKindFilter(t *testing.T) {
	kind := "COMMAND_SEND"
	_, err := newProposalRig(t).api.AgentProposals(tenantCtx(rigTenant), AgentProposalSearchCriteria{Kind: &kind}, nil)
	require.ErrorContains(t, err, "unknown proposal kind")
}
