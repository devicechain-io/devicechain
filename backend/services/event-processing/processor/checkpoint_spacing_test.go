// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"context"
	"errors"
	"testing"
	"time"

	detectcore "github.com/devicechain-io/dc-event-processing/internal/detect/core"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func spacingRig(t *testing.T, cost time.Duration) (*gapRig, *detectcore.ManualClock) {
	t.Helper()
	g := newGapRig(t, &fakeReplayOpener{})
	clock := detectcore.NewManualClock(testBase)
	g.rp.clock, g.rp.cfg.Clock = clock, clock
	g.rp.cfg.CheckpointInterval = 10 * time.Second
	g.rp.lastCheckpoint = clock.Now()
	g.rp.snapshotFn = func() ([]byte, error) {
		clock.Set(clock.Now().Add(cost))
		return g.rp.engine.Snapshot()
	}
	return g, clock
}

func spacingMessage(t *testing.T, seq uint64) messaging.Message {
	t.Helper()
	return measuredMsg(t, seq, "acme", "d1", "p@1", "temperature", "20", &fakeAck{})
}

func TestCheckpointSpacingLive(t *testing.T) {
	for _, cost := range []time.Duration{500 * time.Millisecond, time.Millisecond} {
		t.Run(cost.String(), func(t *testing.T) {
			g, clock := spacingRig(t, cost)
			equal(t, "cold cost", g.rp.lastCheckpointCost, time.Duration(0))
			// Drive the live loop's actual handle/checkpointDue sequence synchronously
			// so the ManualClock cannot race a ticker or reader goroutine.
			for seq := uint64(1); seq <= 20000; seq++ {
				clock.Set(clock.Now().Add(50 * time.Microsecond))
				g.rp.handle(spacingMessage(t, seq))
				if g.rp.checkpointDue() && !g.rp.checkpoint(context.Background()) {
					t.Fatal("checkpoint failed")
				}
			}
			count := testutil.ToFloat64(g.metrics.checkpointsTotal)
			if cost == time.Millisecond {
				equal(t, "small snapshot commits", count, float64(20))
			} else if count < 1 || count > 3 {
				t.Fatalf("slow snapshot commits = %v, want 1..3", count)
			}
			equal(t, "committed cost", g.rp.lastCheckpointCost, cost)
			equal(t, "spacing gauge", testutil.ToFloat64(g.metrics.checkpointSpacing), (4 * cost).Seconds())
		})
	}
}

func TestCheckpointSpacingGapFill(t *testing.T) {
	for _, cost := range []time.Duration{500 * time.Millisecond, 0} {
		t.Run(cost.String(), func(t *testing.T) {
			g, _ := spacingRig(t, cost)
			opener := &fakeReplayOpener{msgs: stream(t, span(1, 200)...)}
			g.rp.Replay = opener // Range reads do not advance the ManualClock.
			g.rp.cfg.CheckpointEvents = 10
			g.rp.handle(hot(t, 201, &fakeAck{})) // Fill 1..200 before the held message.

			equal(t, "range reads", opener.rangeReads, [][2]uint64{{1, 200}})
			equal(t, "gap events applied", g.seqs(gapSeqApplied), float64(200))
			equal(t, "final position", g.rp.engine.LastSeq(), uint64(201))
			count := testutil.ToFloat64(g.metrics.checkpointsTotal)
			if cost == 0 {
				// Negative control: zero snapshot cost leaves count checkpoints unspaced.
				equal(t, "zero-cost snapshot commits", count, float64(20))
			} else if count < 1 || count > 2 {
				t.Fatalf("slow gap-fill commits = %v, want 1..2", count)
			}
		})
	}
}

func TestCheckpointSpacingAckOverride(t *testing.T) {
	g, _ := spacingRig(t, 500*time.Millisecond)
	g.rp.handle(spacingMessage(t, 1))
	if !g.rp.checkpoint(context.Background()) {
		t.Fatal("initial checkpoint failed")
	}
	for seq := uint64(2); seq <= messaging.ReaderMaxAckPending/2; seq++ {
		g.rp.handle(spacingMessage(t, seq))
	}
	if g.rp.checkpointDue() {
		t.Fatal("checkpoint due below override inside spacing window")
	}
	g.rp.handle(spacingMessage(t, messaging.ReaderMaxAckPending/2+1))
	if !g.rp.checkpointDue() {
		t.Fatal("ack override did not bypass spacing")
	}
	if !g.rp.checkpoint(context.Background()) {
		t.Fatal("override checkpoint failed")
	}
	equal(t, "commits", testutil.ToFloat64(g.metrics.checkpointsTotal), float64(2))
	equal(t, "buffered acks", len(g.rp.pendingAcks), 0)
}

func TestCheckpointSpacingIntervalCap(t *testing.T) {
	g, clock := spacingRig(t, 4*time.Second)
	g.rp.handle(spacingMessage(t, 1))
	if !g.rp.checkpoint(context.Background()) {
		t.Fatal("checkpoint failed")
	}
	equal(t, "capped spacing", g.rp.checkpointMinSpacing(), 10*time.Second)
	equal(t, "capped gauge", testutil.ToFloat64(g.metrics.checkpointSpacing), float64(10))
	g.rp.pendingEvents = g.rp.cfg.CheckpointEvents
	clock.Set(clock.Now().Add(10*time.Second - time.Nanosecond))
	if g.rp.checkpointDue() {
		t.Fatal("checkpoint due before capped spacing")
	}
	clock.Set(clock.Now().Add(time.Nanosecond))
	if !g.rp.checkpointDue() {
		t.Fatal("checkpoint not due at interval bound")
	}
}

func TestCheckpointSpacingCostOnlyOnCommit(t *testing.T) {
	for _, failure := range []string{"serialize", "save"} {
		t.Run(failure, func(t *testing.T) {
			g, clock := spacingRig(t, 500*time.Millisecond)
			g.rp.handle(spacingMessage(t, 1))
			if !g.rp.checkpoint(context.Background()) {
				t.Fatal("initial checkpoint failed")
			}
			clock.Set(clock.Now().Add(time.Second))
			g.rp.handle(spacingMessage(t, 1))
			if !g.rp.checkpoint(context.Background()) {
				t.Fatal("ack-only checkpoint failed")
			}
			equal(t, "ack-only cost", g.rp.lastCheckpointCost, 500*time.Millisecond)
			g.rp.handle(spacingMessage(t, 2))
			g.rp.snapshotFn = func() ([]byte, error) {
				clock.Set(clock.Now().Add(3 * time.Second))
				if failure == "serialize" {
					return nil, errors.New("cannot encode")
				}
				return g.rp.engine.Snapshot()
			}
			if failure == "save" {
				store, _, _ := newSlowStore(t, hang)
				g.rp.Store = store
				g.rp.cfg.CheckpointTimeout = 10 * time.Millisecond
			}
			if g.rp.checkpoint(context.Background()) {
				t.Fatal("failed snapshot reported success")
			}
			equal(t, "failure stage", g.failures(failure), float64(1))
			equal(t, "cost after failure", g.rp.lastCheckpointCost, 500*time.Millisecond)
			equal(t, "gauge after failure", testutil.ToFloat64(g.metrics.checkpointSpacing), float64(2))
		})
	}
}

type spacingReplayOpener struct {
	*fakeReplayOpener
	clock *detectcore.ManualClock
}

func (o *spacingReplayOpener) NewReplayReader(suffix string, start uint64) (messaging.ReplayReader, uint64, error) {
	reader, head, err := o.fakeReplayOpener.NewReplayReader(suffix, start)
	return &spacingReplayReader{ReplayReader: reader, clock: o.clock}, head, err
}

type spacingReplayReader struct {
	messaging.ReplayReader
	clock *detectcore.ManualClock
}

func (r *spacingReplayReader) Read(ctx context.Context) (messaging.Message, error) {
	msg, err := r.ReplayReader.Read(ctx)
	if err == nil {
		r.clock.Set(r.clock.Now().Add(50 * time.Microsecond))
	}
	return msg, err
}

func TestCheckpointSpacingReplay(t *testing.T) {
	for _, cost := range []time.Duration{500 * time.Millisecond, time.Millisecond} {
		t.Run(cost.String(), func(t *testing.T) {
			g, clock := spacingRig(t, cost)
			opener := &fakeReplayOpener{head: 10000}
			for seq := uint64(1); seq <= opener.head; seq++ {
				opener.msgs = append(opener.msgs, spacingMessage(t, seq))
			}
			g.rp.Replay = &spacingReplayOpener{fakeReplayOpener: opener, clock: clock}
			if err := g.rp.replayToHead(); err != nil {
				t.Fatal(err)
			}
			count := testutil.ToFloat64(g.metrics.checkpointsTotal)
			if cost == time.Millisecond {
				// Ten count checkpoints plus the final checkpoint call: at exactly
				// 10,000 events the final call is non-dirty and saves no extra snapshot.
				equal(t, "small snapshot commits", count, float64(10))
			} else if count < 2 || count > 4 {
				t.Fatalf("slow replay commits including final = %v, want 2..4", count)
			}
			equal(t, "final committed position", g.rp.committedSeq, uint64(10000))
			equal(t, "replay buffers no acks", len(g.rp.pendingAcks), 0)
		})
	}
}
