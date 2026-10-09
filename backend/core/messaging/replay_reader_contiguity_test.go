// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"io"
	"testing"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/streams"
)

func openReplay(t *testing.T, rig *rangeRig, startSeq uint64) (*natsReplayReader, uint64) {
	t.Helper()
	rd, head, err := rig.mgr.NewReplayReader(streams.InboundEvents, startSeq)
	require.NoError(t, err)
	t.Cleanup(func() { _ = rd.Close() })
	return rd.(*natsReplayReader), head
}

// A delivery the broker counted but the replay never received must be read again, not skipped.
// The test steals deliveries from the reader's own consumer without acking, which is what a
// connection that drops mid-response does.
func TestReplayReaderRecoversLostDeliveries(t *testing.T) {
	t.Run("middle of a batch", func(t *testing.T) {
		rig := newRangeRig(t, 300)
		rd, head := openReplay(t, rig, 1)
		require.Equal(t, uint64(300), head)
		var got []uint64
		m, err := rd.Read(context.Background())
		require.NoError(t, err)
		got = append(got, m.StreamSeq)
		rd.pending = nil // the rest of this batch was sent and lost
		got = append(got, drainRange(t, rd)...)
		require.Equal(t, seqRange(1, 300), got)
		require.Equal(t, 1, rd.recreates)
	})
	t.Run("skipped inside a batch", func(t *testing.T) {
		rig := newRangeRig(t, 300)
		rd, _ := openReplay(t, rig, 1)
		var got []uint64
		m, err := rd.Read(context.Background())
		require.NoError(t, err)
		got = append(got, m.StreamSeq)
		rd.pending = append([]*nats.Msg{rd.pending[0]}, rd.pending[2:]...) // one delivery lost mid-batch
		got = append(got, drainRange(t, rd)...)
		require.Equal(t, seqRange(1, 300), got)
		require.Equal(t, 1, rd.recreates)
	})
	t.Run("last batch", func(t *testing.T) {
		rig := newRangeRig(t, 300)
		rd, _ := openReplay(t, rig, 1)
		var got []uint64
		for len(got) < 256 {
			m, err := rd.Read(context.Background())
			require.NoError(t, err)
			got = append(got, m.StreamSeq)
		}
		require.Empty(t, rd.pending, "the test needs to stand between batches")
		lost, err := rd.sub.Fetch(44, nats.MaxWait(5*time.Second)) // sent, never received, never acked
		require.NoError(t, err)
		require.Len(t, lost, 44)
		got = append(got, drainRange(t, rd)...)
		require.Equal(t, seqRange(1, 300), got, "a lost final batch does not end the replay short of the head")
		require.Equal(t, 1, rd.recreates, "found at once, not after the broker's ack wait redelivered it")
	})
	t.Run("before the first message", func(t *testing.T) {
		rig := newRangeRig(t, 20)
		rd, _ := openReplay(t, rig, 5)
		lost, err := rd.sub.Fetch(3, nats.MaxWait(5*time.Second))
		require.NoError(t, err)
		require.Len(t, lost, 3)
		require.Equal(t, seqRange(5, 20), drainRange(t, rd))
	})
	t.Run("time-started", func(t *testing.T) {
		rig := newRangeRig(t, 50)
		rd, _, err := rig.mgr.NewReplayReaderFromTime(streams.InboundEvents, time.Now().Add(-time.Hour))
		require.NoError(t, err)
		t.Cleanup(func() { _ = rd.Close() })
		nr := rd.(*natsReplayReader)
		lost, err := nr.sub.Fetch(10, nats.MaxWait(5*time.Second))
		require.NoError(t, err)
		require.Len(t, lost, 10)
		require.Equal(t, seqRange(1, 50), drainRange(t, rd))
		require.Equal(t, 1, nr.recreates)
	})
}

// A reader whose consumer keeps losing deliveries stops with an error. It never ends as
// success short of the head.
func TestReplayReaderGivesUpWhenDeliveriesKeepGettingLost(t *testing.T) {
	rig := newRangeRig(t, 100)
	rd, _ := openReplay(t, rig, 1)
	rd.fetch = func() ([]*nats.Msg, error) {
		msgs, err := rd.sub.Fetch(fetchBatch, nats.MaxWait(5*time.Second))
		if err != nil || len(msgs) < 2 {
			return msgs, err
		}
		return msgs[1:], nil // the first delivery of every batch is lost
	}
	for {
		_, err := rd.Read(context.Background())
		if err == nil {
			continue
		}
		require.NotEqual(t, io.EOF, err, "a replay that lost deliveries must not end as success")
		require.ErrorContains(t, err, "giving up")
		break
	}
	require.Equal(t, maxRangeRecreates+1, rd.recreates)
}
