// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"testing"
)

// StreamFirstSequence names the oldest sequence a stream still holds, against a real embedded
// JetStream server: 1 while nothing has been discarded, and the first survivor once the limit has
// rolled the oldest off. A record located at a lower sequence is gone, which is what the
// failed-events list reports as expired.
func TestStreamFirstSequenceMovesWhenTheLimitDiscardsTheOldest(t *testing.T) {
	g := newUnreadRig(t, 100) // alarm-events, MaxMsgs 100
	ctx := context.Background()

	g.publish("t1", 10)
	first, err := g.nmgr.StreamFirstSequence(ctx, unreadSuffix)
	if err != nil {
		t.Fatalf("StreamFirstSequence: %v", err)
	}
	if first != 1 {
		t.Fatalf("first = %d with nothing discarded; want 1", first)
	}

	g.publish("t1", 140) // 150 in all against a limit of 100: the oldest 50 are gone
	first, err = g.nmgr.StreamFirstSequence(ctx, unreadSuffix)
	if err != nil {
		t.Fatalf("StreamFirstSequence: %v", err)
	}
	if first != 51 {
		t.Fatalf("first = %d after 150 messages into a limit of 100; want 51", first)
	}
}

// A stream the broker does not have is an error, never a plausible number: the caller reports it
// as unknown rather than as held or gone.
func TestStreamFirstSequenceOfAMissingStreamIsAnError(t *testing.T) {
	g := newUnreadRig(t, 100)
	if _, err := g.nmgr.StreamFirstSequence(context.Background(), "no-such-stream"); err == nil {
		t.Fatal("a stream that does not exist answered a sequence")
	}
	var none *NatsManager
	if _, err := none.StreamFirstSequence(context.Background(), unreadSuffix); err == nil {
		t.Fatal("a nil manager answered a sequence")
	}
}
