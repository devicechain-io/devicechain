// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package bootstrap

import (
	"strings"
	"testing"

	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/replication"
	"github.com/devicechain-io/dc-microservice/streams"
)

func mqttLayout(capture, inbound, mqtt, resolved string) replication.Snapshot {
	var snap replication.Snapshot
	add := func(name, leader string) {
		snap.Objects = append(snap.Objects, replication.Object{Name: name, Replicas: 3, Leader: leader, Peers: shapePeers(leader)})
	}
	add(messaging.StreamName(shapeInstance, streams.DeviceEventsCapture), capture)
	add(messaging.StreamName(shapeInstance, streams.InboundEvents), inbound)
	add(messaging.MqttMessageStore, mqtt)
	add(messaging.StreamName(shapeInstance, streams.ResolvedEvents), resolved)
	return snap
}

// The rebalance's own options keep resolved-events off the server leading the MQTT message store:
// capture and inbound share n0, the store is on n1 with resolved, so resolved goes to n2. Before the
// store was an anchor the same broker read as separated and nothing moved.
func TestTheRebalanceMovesResolvedOffTheMqttMessageStoreLeader(t *testing.T) {
	resolved := messaging.StreamName(shapeInstance, streams.ResolvedEvents)
	plan := replication.PlanRebalance(mqttLayout("n0", "n0", "n1", "n1"), rebalancePlanOptions(shapeInstance, brokerRebalanceMaxMoves))
	if plan.PerEventLeaders[resolved] != "n2" {
		t.Fatalf("resolved should be led by n2, the server leading no per-event stream: %v (moves %+v)", plan.PerEventLeaders, plan.Moves)
	}
	for _, m := range plan.Moves {
		if m.Stream == messaging.MqttMessageStore {
			t.Fatalf("moved the MQTT message store: %+v", m)
		}
	}
}

// And it is never moved ONTO that server: with capture on n0, inbound on n1 and the store on n2, no
// server is free, so resolved stays where it is rather than trading capture for the store.
func TestTheRebalanceDoesNotTradeCaptureForTheMqttMessageStore(t *testing.T) {
	plan := replication.PlanRebalance(mqttLayout("n0", "n1", "n2", "n0"), rebalancePlanOptions(shapeInstance, brokerRebalanceMaxMoves))
	for _, m := range plan.Moves {
		if m.Stream == messaging.StreamName(shapeInstance, streams.ResolvedEvents) {
			t.Fatalf("resolved moved with no free server to go to: %+v", m)
		}
	}
}

// The summary names the MQTT store by its own name: it has no instance prefix to strip.
func TestTheRebalanceSummaryNamesTheMqttMessageStore(t *testing.T) {
	plan := replication.PlanRebalance(mqttLayout("n0", "n1", "n0", "n1"), rebalancePlanOptions(shapeInstance, brokerRebalanceMaxMoves))
	line := perEventLine(plan, nil)
	if !strings.Contains(line, "$MQTT_msgs=n0") {
		t.Errorf("summary does not name the MQTT store: %s", line)
	}
	if !strings.Contains(line, "$MQTT_msgs/device-events-capture share n0") && !strings.Contains(line, "device-events-capture/$MQTT_msgs share n0") {
		t.Errorf("fixed pair not named in full: %s", line)
	}
}
