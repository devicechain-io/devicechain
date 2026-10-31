// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"testing"
	"time"

	detectcore "github.com/devicechain-io/dc-event-processing/internal/detect/core"
	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-microservice/entity"
)

// TestEntityDeletedConsumerDropsDetectionState: a deleted device's raised latch is torn down
// through the loop, so a device recreated under the same token raises on its first matching reading
// instead of being absorbed by the old device's latch.
func TestEntityDeletedConsumerDropsDetectionState(t *testing.T) {
	id := runtime.ComposeRuleID("acme", "p1@v1/r1")
	rp, eng := armedProcessor(t, id)
	rp.seriesDrops = make(chan seriesDrop, 8)
	// Swap the dead-man rule for a threshold rule under the same id.
	eng.UpsertRule(detectcore.Rule{ID: id, Kind: detectcore.Threshold, Op: detectcore.LT, Thresh: 20})
	feed := func(seq uint64, sec int) []detectcore.Detection {
		eng.ProcessEvent(detectcore.Event{Seq: seq, Key: detectcore.SeriesKey{Rule: id, Series: "dev1"},
			Time: testBase.Add(time.Duration(sec) * time.Second), Value: 12, HasValue: true, Match: true})
		return eng.Drain()
	}
	if got := feed(1, 0); len(got) != 1 {
		t.Fatalf("first reading must raise: %+v", got)
	}

	ack := &fakeAck{acked: make(chan struct{}, 1)}
	rp.EntityDeletedReader = &fakeReader{results: []readResult{{msg: entityDeletedMsg(t, "acme", entity.TypeDevice, "dev1", ack)}}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rp.procCtx = ctx
	rp.readerWG.Add(1)
	go rp.runEntityDeletedConsumer()
	waitForAck(t, ack)
	select {
	case sd := <-rp.seriesDrops:
		rp.applySeriesDrop(sd)
	case <-time.After(2 * time.Second):
		t.Fatal("no series drop was marshaled onto the loop")
	}
	cancel()
	rp.readerWG.Wait()

	if !rp.dirty {
		t.Fatal("dropping serialized state must dirty the loop so the next checkpoint captures it")
	}
	if got := feed(2, 1); len(got) != 1 || got[0].Edge != detectcore.EdgeRaised {
		t.Fatalf("recreated device must raise on its first matching reading: %+v", got)
	}
}
