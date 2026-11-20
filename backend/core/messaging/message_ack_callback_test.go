// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func TestAckCallbackPreservesMessageAndRunsOnce(t *testing.T) {
	ack := &progressAck{}
	c := newCapacity(1, time.Minute, nil)
	if n, ok := c.acquire(context.Background(), 1); !ok || n != 1 {
		t.Fatal("no capacity")
	}
	original := NewConsumedMessage("i.acme.events", []byte("payload"), MaxDeliver, map[string]string{"h": "v"}, ack)
	original.Key = []byte("key")
	original.StreamSeq, original.NumPending = 12, 23
	original.AppendTime = time.Now()
	original.DedupID, original.BypassBackpressure = "dedup", true
	original.slot = c.hold(time.Now())
	defer original.Release()
	original.origin = Origin{Stream: "stream", Consumer: "consumer"}
	original.member, original.split = &memberInfo{}, &splitOrigin{}
	original.traceContext = context.Background()
	callbacks := 0
	copied := original.WithAckCallback(func() { callbacks++ })
	comparison := copied
	comparison.onAck = nil
	if !reflect.DeepEqual(original, comparison) {
		t.Fatal("the callback must preserve every message field")
	}
	if original.onAck != nil {
		t.Fatal("the original was changed")
	}
	if err := copied.InProgress(); err != nil || ack.progress != 1 {
		t.Fatalf("InProgress: %v, count %d", err, ack.progress)
	}
	if copied.AckDeadline().IsZero() {
		t.Fatal("the capacity slot was lost")
	}
	for range 2 {
		if err := copied.Ack(); err != nil {
			t.Fatal(err)
		}
	}
	if callbacks != 1 || ack.acks != 2 {
		t.Fatalf("callbacks=%d acks=%d", callbacks, ack.acks)
	}
	if len(c.tokens) != 1 {
		t.Fatal("Ack did not release the original capacity slot")
	}
	if err := original.Ack(); err != nil || callbacks != 1 {
		t.Fatal("the original inherited the callback")
	}
}
