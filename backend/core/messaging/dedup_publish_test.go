// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/streams"
	"github.com/nats-io/nats.go"
)

// A declared window must be applied; an UNdeclared one must leave the broker's
// setting alone. The distinction is the whole reason this is not a plain assign:
// writing 0 for "not managed" would clear a window the broker already has, and
// would do it on every ensureStream — so two pods on different builds would issue
// an UpdateStream apiece, forever, fighting over the same stream.
func TestApplyStreamDuplicateWindow(t *testing.T) {
	t.Run("a declared window is applied to a stream that has none", func(t *testing.T) {
		cfg := &nats.StreamConfig{}
		if !applyStreamDuplicateWindow(cfg, 10*time.Minute) {
			t.Error("expected a change to be reported")
		}
		if cfg.Duplicates != 10*time.Minute {
			t.Errorf("Duplicates = %v, want 10m", cfg.Duplicates)
		}
	})

	t.Run("a declared window already in place is not rewritten", func(t *testing.T) {
		cfg := &nats.StreamConfig{Duplicates: 10 * time.Minute}
		if applyStreamDuplicateWindow(cfg, 10*time.Minute) {
			t.Error("reported a change for an unchanged window: ensureStream would issue " +
				"an UpdateStream on every pod start")
		}
	})

	t.Run("a declared window of zero is unmanaged, not forced to zero", func(t *testing.T) {
		cfg := &nats.StreamConfig{Duplicates: 5 * time.Minute}
		if applyStreamDuplicateWindow(cfg, 0) {
			t.Error("reported a change for an undeclared window")
		}
		if cfg.Duplicates != 5*time.Minute {
			t.Errorf("Duplicates = %v, want the broker's existing 5m left untouched", cfg.Duplicates)
		}
	})
}

// The ingest path's dedup is worthless unless the stream it publishes to actually
// remembers ids. Declaring the window and setting the header are done in
// different packages, so nothing but this ties them together.
func TestInboundEventsDeclaresADuplicateWindow(t *testing.T) {
	if got := streams.DuplicateWindowSecondsFor(streams.InboundEvents); got <= 0 {
		t.Errorf("inbound-events declares no duplicate window (%d): every Nats-Msg-Id the "+
			"ingest path sets would be ignored and dedup would silently do nothing", got)
	}
	// The floor tracks the RATIONALE the declaration carries, not merely "some
	// window": it is sized to a BAD rollout — an image-pull backoff or a failing
	// deploy, tens of minutes — because that is the restart during which a duplicate
	// actually arises. A floor of a couple of minutes would let a regression to 300s
	// pass here while contradicting the reason the number was chosen, which is the
	// kind of test that reports health it is not checking.
	if got := streams.DuplicateWindowSecondsFor(streams.InboundEvents); got < 900 {
		t.Errorf("duplicate window %ds is under 15 minutes, short of the bad-rollout restart "+
			"it is sized for; a redelivery after the window is not deduped at all for the "+
			"events that carry no alternate id", got)
	}
}
