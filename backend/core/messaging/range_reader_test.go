// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"fmt"
	"io"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/streams"
)

// rangeRig is a broker with one stream holding n messages whose payload is their own
// sequence number, plus a manager to read ranges from it.
type rangeRig struct {
	srv    *natsserver.Server
	mgr    *NatsManager
	stream string
}

func newRangeRig(t *testing.T, n int) *rangeRig {
	t.Helper()
	const suffix = streams.InboundEvents
	srv := startEmbeddedServer(t)
	ctx := core.WithTenant(context.Background(), "acme")

	var writer MessageWriter
	mgr := NewNatsManager(testMicroservice(t, srv, uniqueArea("range-rig")),
		core.NewNoOpLifecycleCallbacks(), func(m *NatsManager) error {
			w, err := m.NewWriter(suffix)
			writer = w
			return err
		})
	require.NoError(t, mgr.Initialize(ctx))
	require.NoError(t, mgr.Start(ctx))
	t.Cleanup(func() { _ = mgr.Stop(ctx) })

	for i := 1; i <= n; i++ {
		require.NoError(t, writer.WriteMessages(ctx, Message{Value: []byte(fmt.Sprint(i))}))
	}
	return &rangeRig{srv: srv, mgr: mgr, stream: StreamName("test", suffix)}
}

func (r *rangeRig) open(t *testing.T, from, to uint64) ReplayReader {
	t.Helper()
	rd, err := r.mgr.NewRangeReader(streams.InboundEvents, from, to)
	require.NoError(t, err)
	t.Cleanup(func() { _ = rd.Close() })
	return rd
}

func (r *rangeRig) consumers(t *testing.T) int {
	t.Helper()
	info, err := r.mgr.js.StreamInfo(r.stream)
	require.NoError(t, err)
	return info.State.Consumers
}

// drainRange reads to io.EOF and returns the stream sequences in the order they came, checking
// that each message's payload is its own sequence (so the sequence is the right message's).
func drainRange(t *testing.T, rd ReplayReader) []uint64 {
	t.Helper()
	var got []uint64
	for {
		m, err := rd.Read(context.Background())
		if err == io.EOF {
			return got
		}
		require.NoError(t, err)
		require.Equal(t, fmt.Sprint(m.StreamSeq), string(m.Value), "message at seq %d carries another message's payload", m.StreamSeq)
		got = append(got, m.StreamSeq)
	}
}

func seqRange(from, to uint64, except ...uint64) []uint64 {
	skip := map[uint64]bool{}
	for _, e := range except {
		skip[e] = true
	}
	var out []uint64
	for s := from; s <= to; s++ {
		if !skip[s] {
			out = append(out, s)
		}
	}
	return out
}

func rangeStats(t *testing.T, rd ReplayReader) RangeStats {
	t.Helper()
	s, ok := rd.(RangeStatser)
	require.True(t, ok, "the range reader reports its stats")
	return s.RangeStats()
}

func TestRangeReaderContiguous(t *testing.T) {
	rig := newRangeRig(t, 40)
	rd := rig.open(t, 10, 30)
	require.Equal(t, seqRange(10, 30), drainRange(t, rd))
	require.Equal(t, RangeStats{Present: 21}, rangeStats(t, rd))
	require.Zero(t, rig.consumers(t), "a narrow range creates no consumer")
}

func TestRangeReaderReportsPurgedSequencesAbsent(t *testing.T) {
	rig := newRangeRig(t, 40)
	for _, s := range []uint64{12, 13, 20, 30} {
		require.NoError(t, rig.mgr.js.DeleteMsg(rig.stream, s))
	}
	rd := rig.open(t, 10, 30)
	require.Equal(t, seqRange(10, 30, 12, 13, 20, 30), drainRange(t, rd), "the survivors, in order")
	require.Equal(t, RangeStats{Present: 17, Absent: 4}, rangeStats(t, rd), "every removed sequence is reported, none dropped silently")
}

func TestRangeReaderBelowFirstSeqIsAbsent(t *testing.T) {
	rig := newRangeRig(t, 40)
	require.NoError(t, rig.mgr.js.PurgeStream(rig.stream, &nats.StreamPurgeRequest{Sequence: 21})) // evicts 1..20
	rd := rig.open(t, 15, 25)
	require.Equal(t, seqRange(21, 25), drainRange(t, rd))
	require.Equal(t, RangeStats{Present: 5, Absent: 6}, rangeStats(t, rd))
}

func TestRangeReaderWideRangeUsesOneBoundedConsumer(t *testing.T) {
	rig := newRangeRig(t, 320)
	for _, s := range []uint64{5, 6, 150, 299} {
		require.NoError(t, rig.mgr.js.DeleteMsg(rig.stream, s))
	}
	const from, to = 2, 310 // 309 sequences: past the direct limit
	rd := rig.open(t, from, to)
	require.Equal(t, 1, rig.consumers(t), "a wide range reads through exactly one consumer")
	got := drainRange(t, rd)
	require.Equal(t, seqRange(from, to, 5, 6, 150, 299), got, "all of it, in order, and nothing past `to`")
	require.Equal(t, RangeStats{Present: uint64(len(got)), Absent: 4}, rangeStats(t, rd))
	require.NoError(t, rd.Close())
	require.Eventually(t, func() bool { return rig.consumers(t) == 0 }, 5*time.Second, 50*time.Millisecond,
		"the ephemeral consumer is released on Close")
}

func TestRangeReaderWideRangeEntirelyEvictedCreatesNoConsumer(t *testing.T) {
	rig := newRangeRig(t, 320)
	require.NoError(t, rig.mgr.js.PurgeStream(rig.stream, &nats.StreamPurgeRequest{Sequence: 311}))
	rd := rig.open(t, 1, 300)
	require.Zero(t, rig.consumers(t))
	require.Empty(t, drainRange(t, rd))
	require.Equal(t, RangeStats{Absent: 300}, rangeStats(t, rd))
	require.Zero(t, rig.consumers(t))
}

func TestRangeReaderWideRangePartlyEvictedStartsAtFirstSeq(t *testing.T) {
	rig := newRangeRig(t, 320)
	require.NoError(t, rig.mgr.js.PurgeStream(rig.stream, &nats.StreamPurgeRequest{Sequence: 101}))
	rd := rig.open(t, 1, 300)
	got := drainRange(t, rd)
	require.Equal(t, seqRange(101, 300), got)
	require.Equal(t, RangeStats{Present: 200, Absent: 100}, rangeStats(t, rd))
}

// A delivery the broker counted but the client never received must not read as a purged
// sequence. The test steals deliveries from the reader's own consumer without acking, so
// the broker has sent them and the reader never saw them, which is what a connection that
// drops mid-response does.
func TestRangeReaderWideRangeRecoversLostDeliveries(t *testing.T) {
	t.Run("middle of a batch", func(t *testing.T) {
		rig := newRangeRig(t, 300)
		rd := rig.open(t, 1, 300).(*consumerRangeReader)
		var got []uint64
		m, err := rd.Read(context.Background())
		require.NoError(t, err)
		got = append(got, m.StreamSeq)
		rd.pending = nil // the rest of this batch was sent and lost
		got = append(got, drainRange(t, rd)...)
		require.Equal(t, seqRange(1, 300), got, "the lost tail of the batch was re-read")
		require.Equal(t, 1, rd.recreates)
	})
	t.Run("last batch", func(t *testing.T) {
		rig := newRangeRig(t, 300)
		rd := rig.open(t, 1, 300).(*consumerRangeReader)
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
		require.Equal(t, seqRange(1, 300), got, "a lost final batch is not mistaken for purged sequences")
		require.Equal(t, RangeStats{Present: 300}, rangeStats(t, rd))
	})
}

func TestRangeReaderRefusesAnInvalidRange(t *testing.T) {
	rig := newRangeRig(t, 5)
	for _, c := range [][2]uint64{{5, 4}, {0, 3}} {
		rd, err := rig.mgr.NewRangeReader(streams.InboundEvents, c[0], c[1])
		require.Error(t, err, "range [%d, %d]", c[0], c[1])
		require.Nil(t, rd)
	}
}

// A broker that goes away mid-range is an error from Read, never a clean end of a short
// result: a caller that took io.EOF for "the range is done" would advance past a hole.
func TestRangeReaderBrokerErrorMidRangeIsAnError(t *testing.T) {
	rig := newRangeRig(t, 40)
	rd := rig.open(t, 10, 30)
	for i := 0; i < 3; i++ {
		_, err := rd.Read(context.Background())
		require.NoError(t, err)
	}
	rig.srv.Shutdown()
	rig.srv.WaitForShutdown()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for {
		_, err := rd.Read(ctx)
		if err == nil {
			continue // answered from before the shutdown took hold
		}
		require.NotEqual(t, io.EOF, err, "a broker failure must not read as the end of the range")
		return
	}
}

func TestRangeReaderCancelledContextIsAnError(t *testing.T) {
	rig := newRangeRig(t, 40)
	rd := rig.open(t, 10, 30)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := rd.Read(ctx)
	require.ErrorIs(t, err, context.Canceled)
}
