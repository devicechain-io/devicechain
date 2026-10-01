// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/prometheus/client_golang/prometheus"
)

// What a QUEUE of events costs the projection, by value. On a replicated database every
// commit waits for the standby, so the number that matters is transactions: merged one at a
// time, an event costs two (its device state, then its latest values), and a queue of 32
// costs 64 — and 64 fence reads, one per transaction. Merged as a batch, the queue is ONE
// transaction with one fence read.
//
// Every test here drives the worker loop SYNCHRONOUSLY over a channel filled and closed
// before it runs, so what forms a batch is decided by maxBatch alone and nothing races.

// queueOf fills a closed channel with msgs, each consumed with its own ack recorder.
func queueOf(msgs []messaging.Message) (chan messaging.Message, []*recordingAck) {
	ch := make(chan messaging.Message, len(msgs))
	acks := make([]*recordingAck, len(msgs))
	for i, m := range msgs {
		acks[i] = &recordingAck{}
		ch <- consumed(m, acks[i])
	}
	close(ch)
	return ch, acks
}

// drainQueue runs the worker loop over msgs to completion, exactly as a writer does.
func drainQueue(sp *StateProcessor, msgs []messaging.Message) []*recordingAck {
	ch, acks := queueOf(msgs)
	sp.messages = ch
	sp.processMessages(context.Background())
	return acks
}

// ackCounts is how many times each message was acknowledged.
func ackCounts(acks []*recordingAck) []int32 {
	out := make([]int32, len(acks))
	for i, a := range acks {
		out[i] = a.n.Load()
	}
	return out
}

// stateRegistry gives sp instruments of its own, on a registry the test reads, so counts
// from other tests sharing the package's microservice cannot leak in.
func stateRegistry(sp *StateProcessor) *prometheus.Registry {
	ms := &core.Microservice{InstanceId: "test", FunctionalArea: "device-state"}
	reg := prometheus.NewRegistry()
	ms.UseMetricsRegistry(reg)
	sp.metrics = NewStateMetrics(ms)
	return reg
}

// gathered reads one series from reg: a counter's value, a histogram's sample count and sum,
// or the messages counter for one result. found=false is absence.
func gathered(t *testing.T, reg *prometheus.Registry, name, result string) (value, count, sum float64, found bool) {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, f := range families {
		if f.GetName() != "devicechain_devicestate_"+name {
			continue
		}
		for _, m := range f.GetMetric() {
			match := result == ""
			for _, lp := range m.GetLabel() {
				if lp.GetName() == "result" && lp.GetValue() == result {
					match = true
				}
			}
			if !match {
				continue
			}
			if h := m.GetHistogram(); h != nil {
				return 0, float64(h.GetSampleCount()), h.GetSampleSum(), true
			}
			return m.GetCounter().GetValue(), 0, 0, true
		}
	}
	return 0, 0, 0, false
}

// A queue of 32 measurement events for 32 devices of one tenant is merged in ONE
// transaction: one lock read, one fence read, the devices' states, one upsert for all 96
// latest values — where merging them one at a time made 320 statements and 64 fence reads in
// 64 transactions. A device seen for the first time is inserted on its own (that is how a
// lost first-sight race is told apart); the states of devices that already have one are
// written back together, one statement for the tenant.
func TestAQueueOfEventsIsMergedInOneTransaction(t *testing.T) {
	t0 := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Minute)
	devices := make([]string, 32)
	for i := range devices {
		devices[i] = fmt.Sprintf("bq-%02d", i)
	}
	for _, tc := range []struct {
		name string
		// seeded devices already have a row (merged at t0, one at a time) before the queue.
		seeded bool
		at     time.Time
		// The devices' states: one INSERT per device at first sight, one write-back for all of
		// them once they exist — plus the lock read, the fence read and the latest-value upsert.
		wantAll, wantFence int64
	}{
		{"first sight", false, t0, 1 + 1 + 32 + 1, 1},
		{"every device seen before", true, t1, 1 + 1 + 1 + 1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sp, _, counter := newFencedStateProcessor(t)
			reg := stateRegistry(sp)
			if tc.seeded {
				for _, d := range devices {
					seed(t, sp, threeMetrics(t, d, t0))
				}
			}
			msgs := make([]messaging.Message, len(devices))
			for i, d := range devices {
				msgs[i] = threeMetrics(t, d, tc.at)
			}
			counter.Reset()
			acks := drainQueue(sp, msgs)
			all, fence := counter.Counts()
			if all != tc.wantAll || fence != tc.wantFence {
				t.Errorf("a queue of 32 made %d statements with %d fence reads; want %d with %d",
					all, fence, tc.wantAll, tc.wantFence)
			}
			for i, n := range ackCounts(acks) {
				if n != 1 {
					t.Errorf("message %d acknowledged %d times; want 1", i, n)
				}
			}
			for _, d := range devices {
				assertProjectedAt(t, sp, d, tc.at)
			}
			// One committed transaction, holding all 32. Seeding went through the per-message
			// path, which records its own commits of one — hence the offset.
			seeded := 0.0
			if tc.seeded {
				seeded = 32
			}
			if _, n, sum, _ := gathered(t, reg, "state_batch_size", ""); n != 1+seeded || sum != 32+seeded {
				t.Errorf("batch-size histogram holds %v observations summing to %v; want %v summing to %v",
					n, sum, 1+seeded, 32+seeded)
			}
			if v, _, _, found := gathered(t, reg, "state_messages_total", core.ResultOK); !found || v != 32+seeded {
				t.Errorf("state_messages_total{result=ok} = %v (found=%v); want %v", v, found, 32+seeded)
			}
		})
	}
}

// A batch is bounded by maxBatch: 100 events at 32 are four transactions (32+32+32+4).
func TestABatchHoldsAtMostMaxBatchEvents(t *testing.T) {
	sp, _, _ := newFencedStateProcessor(t)
	reg := stateRegistry(sp)
	t0 := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	msgs := make([]messaging.Message, 100)
	for i := range msgs {
		msgs[i] = threeMetrics(t, fmt.Sprintf("bm-%03d", i), t0)
	}
	acks := drainQueue(sp, msgs)
	for i, n := range ackCounts(acks) {
		if n != 1 {
			t.Errorf("message %d acknowledged %d times; want 1", i, n)
		}
	}
	if _, n, sum, _ := gathered(t, reg, "state_batch_size", ""); n != 4 || sum != 100 {
		t.Errorf("batch-size histogram holds %v observations summing to %v; want 4 summing to 100", n, sum)
	}
}

// Several events for ONE device in a batch fold in arrival order, over one row: the queue
// t2, t0, t1 leaves the device's activity and every reading at t2, and costs the same one
// transaction as three devices would.
func TestEventsForOneDeviceInABatchFoldInArrivalOrder(t *testing.T) {
	sp, _, counter := newFencedStateProcessor(t)
	t0 := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	t1, t2 := t0.Add(time.Minute), t0.Add(2*time.Minute)
	counter.Reset()
	acks := drainQueue(sp, []messaging.Message{
		threeMetrics(t, "bf-01", t2), threeMetrics(t, "bf-01", t0), threeMetrics(t, "bf-01", t1),
	})
	if all, fence := counter.Counts(); all != 4 || fence != 1 {
		t.Errorf("three events for one new device made %d statements with %d fence reads; want 4 with 1 "+
			"(lock read, fence, insert, upsert)", all, fence)
	}
	for i, n := range ackCounts(acks) {
		if n != 1 {
			t.Errorf("message %d acknowledged %d times; want 1", i, n)
		}
	}
	assertProjectedAt(t, sp, "bf-01", t2)
}

// A processor assembled by literal — no constructor, so no metrics and a zero projection
// configuration — still merges: zero instruments are no-ops and a zero maxBatch is a batch of
// one. Several tests in this package build processors that way.
func TestALiteralProcessorMergesOneAtATime(t *testing.T) {
	built, _, counter := newFencedStateProcessor(t)
	sp := &StateProcessor{Api: built.Api}
	t0 := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	counter.Reset()
	acks := drainQueue(sp, []messaging.Message{threeMetrics(t, "bl-01", t0), threeMetrics(t, "bl-02", t0)})
	for i, n := range ackCounts(acks) {
		if n != 1 {
			t.Errorf("message %d acknowledged %d times; want 1", i, n)
		}
	}
	// Two events on the per-message path: two transactions each.
	if _, fence := counter.Counts(); fence != 4 {
		t.Errorf("two events made %d fence reads; want 4 (two transactions each, one at a time)", fence)
	}
	assertProjectedAt(t, built, "bl-01", t0)
	assertProjectedAt(t, built, "bl-02", t0)
}
