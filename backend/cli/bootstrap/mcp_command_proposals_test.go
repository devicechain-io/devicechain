// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestCommandProposalsOverlayIsExplicitAndNarrow(t *testing.T) {
	for _, on := range []bool{false, true} {
		overlay := map[string]interface{}{"instance": map[string]interface{}{"config": map[string]interface{}{"infrastructure": map[string]interface{}{"mcp": map[string]interface{}{"commandProposals": on}}}}}
		require.NoError(t, refuseOverlayKeys(overlay, "test"))
	}
	typo := map[string]interface{}{"instance": map[string]interface{}{"config": map[string]interface{}{"infrastructure": map[string]interface{}{"mcp": map[string]interface{}{"commandProposal": true}}}}}
	require.Error(t, refuseOverlayKeys(typo, "test"))
}
