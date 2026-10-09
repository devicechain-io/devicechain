// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// A generated correlation id is short, present, and unique across many messages; a propagated
// one is carried through untouched.
func TestNatsMsgGeneratedCorrelationID(t *testing.T) {
	seen := map[string]struct{}{}
	for i := 0; i < 10000; i++ {
		id := natsMsg("s", Message{}).Header.Get(HeaderCorrelationID)
		require.Len(t, id, 22)
		_, dup := seen[id]
		require.False(t, dup, "generated correlation id repeated")
		seen[id] = struct{}{}
	}
	require.Equal(t, "upstream", natsMsg("s", Message{}.WithCorrelationID("upstream")).Header.Get(HeaderCorrelationID))
}
