// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"testing"
)

// DeliveredThrough separates what a durable has handed out from what it has not: before any read
// it reports the sequence before its start, after a read it covers what was read and never runs
// past the stream, and a fresh publish nobody has fetched does not move it.
func TestDeliveredThroughReportsWhatTheDurableHandedOut(t *testing.T) {
	f := newFloorFixture(t, 0)
	f.publish(5)
	r, err := f.reader(fixedStart(3))
	if err != nil {
		t.Fatalf("reader: %v", err)
	}
	ctx := context.Background()
	got, err := r.DeliveredThrough(ctx)
	if err != nil || got != 2 {
		t.Fatalf("before any read: delivered through %d (%v), want 2, the sequence before the start", got, err)
	}
	f.readN(r, 3)
	if got, err = r.DeliveredThrough(ctx); err != nil || got != 5 {
		t.Fatalf("after reading 3..5: delivered through %d (%v), want 5", got, err)
	}
	f.publish(2)
	if got, err = r.DeliveredThrough(ctx); err != nil || got != 5 {
		t.Fatalf("an unfetched publish moved the delivered sequence to %d (%v), want 5", got, err)
	}
}

// A durable that is gone has delivered nothing.
func TestDeliveredThroughOfAMissingDurableIsZero(t *testing.T) {
	f := newFloorFixture(t, 0)
	f.publish(2)
	r, err := f.reader(fixedStart(1))
	if err != nil {
		t.Fatalf("reader: %v", err)
	}
	f.readN(r, 2)
	if err := f.nmgr.js.DeleteConsumer(f.stream, f.durable); err != nil {
		t.Fatalf("delete durable: %v", err)
	}
	if got, err := r.DeliveredThrough(context.Background()); err != nil || got != 0 {
		t.Fatalf("missing durable: delivered through %d (%v), want 0 and no error", got, err)
	}
}
