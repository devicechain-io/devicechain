// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"strconv"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/messaging"
)

// 🔴 A $MQTT_msgs sequence and a capture sequence must never make the same dedup id. Both
// forward into the one inbound-events stream, and while both paths run (the cutover overlaps
// them on purpose) an equal id would have the broker answer the second event as a duplicate
// and drop it with a successful publish. The id built from the gateway's message store
// carries a source tag and the store's epoch; this pins that no capture id, of this build or
// the previous one, can equal it, for the same tenant and the same sequence numbers.
//
// GUARD: kills a store id without its source tag or epoch (it would equal DedupID for the
// same sequence), and a separator change that let the two shapes meet.
func TestAMessageStoreDedupIDNeverEqualsACaptureDedupID(t *testing.T) {
	epochs := []time.Time{time.Unix(1, 0), time.Unix(1_800_000_000, 123), time.Unix(4_000_000_000, 0)}
	for _, tenant := range []string{"acme", "a", "m1", "t-1_x"} {
		capture := map[string]bool{}
		for seq := uint64(1); seq <= 50_000; seq++ {
			capture[DedupID(tenant, seq)] = true
			capture[tenant+":"+strconv.FormatUint(seq, 10)] = true // the previous build's shape
		}
		for _, epoch := range epochs {
			for seq := uint64(1); seq <= 50_000; seq++ {
				id := messaging.MqttMessageStoreDedupID(tenant, epoch, seq)
				if id == "" {
					t.Fatalf("no id for tenant %q, sequence %d", tenant, seq)
				}
				if capture[id] {
					t.Fatalf("the message store's id %q for sequence %d equals a capture id", id, seq)
				}
			}
		}
	}
}
