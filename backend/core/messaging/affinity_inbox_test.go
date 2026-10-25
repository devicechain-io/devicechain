// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/core"
)

// The leader connection the affinity loop dials carries the area's reply-inbox prefix, as the
// manager's own connection does. An area's broker login may subscribe to "_INBOX_<area>.>" and
// to no other inbox, so a leader connection on nats.go's default "_INBOX" would be refused
// every reply it asked for: each PubAck of a write routed to the stream leader would time out.
// No permission test reaches this connection (it is dialled only when the stream leader is
// another server, which an embedded single broker never is), so it is pinned here.
func TestTheLeaderConnectionUsesTheAreasInboxPrefix(t *testing.T) {
	url := evictRig(t)
	const area = "event-processing"
	a := &affinity{nmgr: &NatsManager{Microservice: &core.Microservice{FunctionalArea: area}}}

	nc, err := a.dial("server-1", url)
	require.NoError(t, err)
	t.Cleanup(nc.Close)

	inbox := nc.NewInbox()
	require.True(t, strings.HasPrefix(inbox, InboxPrefix(area)+"."),
		"the leader connection's replies arrive on %q, outside %s.>, the only inbox the area's login may read",
		inbox, InboxPrefix(area))
}
