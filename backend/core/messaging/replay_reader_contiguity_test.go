// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"fmt"
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
		require.Equal(t, 1, rig.consumers(t), "a re-create deletes the consumer it replaces")
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
		began := time.Now()
		got = append(got, drainRange(t, rd)...)
		require.Equal(t, seqRange(1, 300), got, "a lost final batch does not end the replay short of the head")
		require.Equal(t, 1, rd.recreates, "found at once, not after the broker's ack wait redelivered it")
		require.Less(t, time.Since(began), 10*time.Second, "recovered by the re-create, not by waiting out the broker's ack wait (30s)")
	})
	t.Run("before the first message", func(t *testing.T) {
		rig := newRangeRig(t, 20)
		rd, _ := openReplay(t, rig, 5)
		lost, err := rd.sub.Fetch(3, nats.MaxWait(5*time.Second))
		require.NoError(t, err)
		require.Len(t, lost, 3)
		require.Equal(t, seqRange(5, 20), drainRange(t, rd))
		require.Equal(t, 1, rd.recreates)
	})
	t.Run("backwards consumer sequence", func(t *testing.T) {
		rig := newRangeRig(t, 100)
		rd, _ := openReplay(t, rig, 1)
		m, err := rd.Read(context.Background())
		require.NoError(t, err)
		got := []uint64{m.StreamSeq}
		rd.lastDseq += 10 // the next delivery now looks like it went backwards
		got = append(got, drainRange(t, rd)...)
		require.Equal(t, seqRange(1, 100), got)
		require.Equal(t, 1, rd.recreates)
	})
	t.Run("lost tail then a live message past the head", func(t *testing.T) {
		rig := newRangeRig(t, 100)
		rd, head := openReplay(t, rig, 1)
		require.Equal(t, uint64(100), head)
		var got []uint64
		for i := 0; i < fetchBatch; i++ {
			m, err := rd.Read(context.Background())
			require.NoError(t, err)
			got = append(got, m.StreamSeq)
		}
		lost, err := rd.sub.Fetch(36, nats.MaxWait(5*time.Second))
		require.NoError(t, err)
		require.Len(t, lost, 36)
		for i := 101; i <= 110; i++ {
			_, err := rig.mgr.js.Publish(ScopedSubject("test", "acme", "inbound-events"), []byte(fmt.Sprint(i)))
			require.NoError(t, err)
		}
		got = append(got, drainRange(t, rd)...)
		require.Equal(t, seqRange(1, 100), got, "the live message past the head must not end the replay before the lost range is read")
		require.Equal(t, 1, rd.recreates)
	})
	t.Run("time-started mid-stream", func(t *testing.T) {
		rig := newRangeRig(t, 50)
		raw, err := rig.mgr.js.GetMsg(rig.stream, 20)
		require.NoError(t, err)
		rd, _, err := rig.mgr.NewReplayReaderFromTime(streams.InboundEvents, raw.Time)
		require.NoError(t, err)
		t.Cleanup(func() { _ = rd.Close() })
		nr := rd.(*natsReplayReader)
		lost, err := nr.sub.Fetch(5, nats.MaxWait(5*time.Second))
		require.NoError(t, err)
		require.Len(t, lost, 5)
		// Messages published in the same clock tick share a timestamp, so the first one at
		// or after the start time can be earlier than 20.
		first := uint64(20)
		for first > 1 {
			prev, err := rig.mgr.js.GetMsg(rig.stream, first-1)
			require.NoError(t, err)
			if prev.Time.Before(raw.Time) {
				break
			}
			first--
		}
		require.Greater(t, first, uint64(1), "the start time must fall inside the stream for this test to mean anything")
		got := drainRange(t, rd)
		require.Equal(t, seqRange(first, 50), got, "a re-create keeps the requested start time")
		require.Equal(t, 1, nr.recreates)
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
	// A re-create loop with no bound would spin until the go-test timeout; the context
	// turns that into a failure of this test (Read answers io.EOF once it is done).
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for {
		_, err := rd.Read(ctx)
		if err == nil {
			continue
		}
		require.NotEqual(t, io.EOF, err, "a replay that lost deliveries must not end as success")
		require.ErrorContains(t, err, "giving up")
		break
	}
	require.Equal(t, maxRangeRecreates+1, rd.recreates)
}

// A broker that never saw an ack sends the message again, with a fresh consumer sequence. The
// replay must not hand the copy out: it has already returned that message, and a second
// apply would break both the ascending order and the engine's state.
func TestReplayReaderSkipsRedeliveredCopies(t *testing.T) {
	rig := newRangeRig(t, 200)
	rd, _ := openReplay(t, rig, 1)
	rd.ackWait = time.Second // a test-only shortcut to the broker's 30s default
	require.NoError(t, rd.recreate("short ack wait for the test"))
	// A subscription on a connection that is already closed: acking through it fails, as it
	// does when the connection drops, while the message still answers its metadata.
	nc, err := nats.Connect(rig.srv.ClientURL())
	require.NoError(t, err)
	js, err := nc.JetStream()
	require.NoError(t, err)
	dead, err := js.PullSubscribe(rd.subject, "", nats.BindStream(rig.stream), nats.AckExplicit())
	require.NoError(t, err)
	nc.Close()
	first := true
	rd.fetch = func() ([]*nats.Msg, error) {
		msgs, err := rd.sub.Fetch(fetchBatch, nats.MaxWait(fetchTimeout))
		if first && err == nil {
			first = false
			for _, m := range msgs {
				m.Sub = dead
			}
		}
		return msgs, err
	}
	var got []uint64
	for i := 0; i < fetchBatch; i++ {
		m, err := rd.Read(context.Background())
		require.NoError(t, err)
		got = append(got, m.StreamSeq)
	}
	time.Sleep(2500 * time.Millisecond) // past the ack wait: the first batch is sent again
	got = append(got, drainRange(t, rd)...)
	require.Equal(t, seqRange(1, 200), got, "strictly ascending, exactly the range, no copies")
	require.Positive(t, rd.redelivered, "the broker must have redelivered for this test to mean anything")
}

// A re-create that fails leaves the reader unusable. Every later Read says so; none of them
// may answer io.EOF, which would end the replay as if it were complete.
func TestReplayReaderFailedRecreateIsSticky(t *testing.T) {
	rig := newRangeRig(t, 100)
	rd, _ := openReplay(t, rig, 1)
	_, err := rd.Read(context.Background())
	require.NoError(t, err)
	require.NoError(t, rig.mgr.js.DeleteStream(rig.stream))
	first := rd.recreate("stream deleted for the test")
	require.Error(t, first)
	require.NotEqual(t, io.EOF, first)
	for i := 0; i < 3; i++ {
		_, again := rd.Read(context.Background())
		require.Equal(t, first, again, "a failed re-create is final")
	}
}
