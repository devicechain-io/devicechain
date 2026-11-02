// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"testing"

	"github.com/nats-io/nats.go"
)

// TestOnlinePeersSeparatesLagFromLoss pins the distinction the replication alerts
// rely on: a follower that is merely behind is still ONLINE (so the offline alert
// stays quiet), while an offline or unlisted peer is not.
func TestOnlinePeersSeparatesLagFromLoss(t *testing.T) {
	info := func(peers ...*nats.PeerInfo) *nats.StreamInfo {
		return &nats.StreamInfo{Cluster: &nats.ClusterInfo{Leader: "n1", Replicas: peers}}
	}
	cases := []struct {
		name            string
		si              *nats.StreamInfo
		online, current int
	}{
		{"all current", info(&nats.PeerInfo{Name: "n2", Current: true}, &nats.PeerInfo{Name: "n3", Current: true}), 3, 3},
		{"one lagging", info(&nats.PeerInfo{Name: "n2", Current: true}, &nats.PeerInfo{Name: "n3"}), 3, 2},
		{"one offline", info(&nats.PeerInfo{Name: "n2", Current: true}, &nats.PeerInfo{Name: "n3", Offline: true}), 2, 2},
		{"one unlisted", info(&nats.PeerInfo{Name: "n2", Current: true}), 2, 2},
		{"unclustered", &nats.StreamInfo{}, 1, 1},
	}
	for _, c := range cases {
		if got := onlinePeers(c.si); got != c.online {
			t.Errorf("%s: onlinePeers = %d, want %d", c.name, got, c.online)
		}
		if got := currentPeers(c.si); got != c.current {
			t.Errorf("%s: currentPeers = %d, want %d", c.name, got, c.current)
		}
	}
}
