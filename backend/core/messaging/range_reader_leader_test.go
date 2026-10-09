// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	nats "github.com/nats-io/nats.go"
	"github.com/stretchr/testify/require"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/streams"
	dctest "github.com/devicechain-io/dc-microservice/test"
)

// These tests cover what the range reader believes. Every claim that a sequence is absent has
// to be one the stream leader would make, not a single "not found" and not a consumer's own
// "nothing pending", because neither says what the stream holds.

// fakeSource is a by-sequence source for the narrow reader. reply decides each answer from the
// sequence and how many times it has been asked for.
func fakeSource(first uint64, reply func(seq uint64, call int) (*nats.RawStreamMsg, error)) (*rangeSource, map[uint64]int) {
	calls := map[uint64]int{}
	return &rangeSource{firstSeq: first, stream: "fake", get: func(_ context.Context, seq uint64) (*nats.RawStreamMsg, error) {
		calls[seq]++
		return reply(seq, calls[seq])
	}}, calls
}

func rawMsg(seq uint64) *nats.RawStreamMsg {
	return &nats.RawStreamMsg{Sequence: seq, Subject: "s", Data: []byte(fmt.Sprint(seq))}
}

func readAll(rd ReplayReader) (got []uint64, err error) {
	for {
		m, err := rd.Read(context.Background())
		if err != nil {
			return got, err
		}
		got = append(got, m.StreamSeq)
	}
}

// Only a "message not found" answer is an absence. Any other error from the broker, however
// it arrives, ends the read with that error and leaves the stats incomplete.
func TestRangeReaderOnlyNotFoundIsAbsent(t *testing.T) {
	boom := errors.New("boom")
	src, _ := fakeSource(1, func(seq uint64, _ int) (*nats.RawStreamMsg, error) {
		switch seq {
		case 11:
			return nil, nats.ErrMsgNotFound
		case 14:
			return nil, boom
		}
		return rawMsg(seq), nil
	})
	rd := &seqRangeReader{src: src, from: 10, to: 20, next: 10}
	got, err := readAll(rd)
	require.ErrorIs(t, err, boom)
	require.Equal(t, []uint64{10, 12, 13}, got)
	require.Equal(t, RangeStats{Present: 3, Absent: 1, Incomplete: true}, rd.RangeStats(),
		"after a failure the stats count only what was established")
}

// The server reports several read failures as "no message found". At or above the first
// retained sequence a not-found is asked again, and a message that turns up on the second ask
// is delivered rather than counted absent; below it the answer is believed at once.
func TestRangeReaderAsksAgainBeforeCountingAnAbsence(t *testing.T) {
	src, calls := fakeSource(11, func(seq uint64, call int) (*nats.RawStreamMsg, error) {
		switch {
		case seq == 10: // evicted: gone for good
			return nil, nats.ErrMsgNotFound
		case seq == 12 && call == 1: // a transient failure reported as not found
			return nil, nats.ErrMsgNotFound
		case seq == 13: // purged
			return nil, nats.ErrMsgNotFound
		}
		return rawMsg(seq), nil
	})
	rd := &seqRangeReader{src: src, from: 10, to: 14, next: 10}
	got, err := readAll(rd)
	require.Equal(t, io.EOF, err)
	require.Equal(t, []uint64{11, 12, 14}, got, "the sequence that turned up on the second ask is delivered")
	require.Equal(t, RangeStats{Present: 3, Absent: 2}, rd.RangeStats())
	require.Equal(t, 1, calls[10], "below the first retained sequence a not-found is believed at once")
	require.Equal(t, 2, calls[13], "a purged sequence stays absent on the second ask")
}

// A sequence past the stream's last one is not absent, it does not exist yet: refused, on both
// the narrow and the wide path.
func TestRangeReaderRefusesARangePastTheLastSequence(t *testing.T) {
	rig := newRangeRig(t, 40)
	for _, c := range [][2]uint64{{30, 41}, {1, 300}, {41, 45}} {
		rd, err := rig.mgr.NewRangeReader(streams.InboundEvents, c[0], c[1])
		require.Error(t, err, "range %v", c)
		require.Nil(t, rd)
	}
	rd := rig.open(t, 30, 40)
	require.Equal(t, seqRange(30, 40), drainRange(t, rd), "the last sequence itself is in range")
}

func TestRangeReaderRefusesAnAdvisoryStreamOnTheWidePath(t *testing.T) {
	rig := newRangeRig(t, 5)
	rd, err := rig.mgr.NewRangeReader(streams.MaxDeliveries, 1, 1000)
	require.Error(t, err)
	require.Nil(t, rd)
}

// The consumer delivers nothing past `to`: with `to` and the sequence before it deleted, the
// next message it hands out is beyond the range and must end it, not be returned.
func TestRangeReaderWideRangeDoesNotReturnPastTo(t *testing.T) {
	rig := newRangeRig(t, 320)
	require.NoError(t, rig.mgr.js.DeleteMsg(rig.stream, 299))
	require.NoError(t, rig.mgr.js.DeleteMsg(rig.stream, 300))
	rd := rig.open(t, 2, 300)
	require.Equal(t, seqRange(2, 298), drainRange(t, rd))
	require.Equal(t, RangeStats{Present: 297, Absent: 2}, rangeStats(t, rd))
}

// A range whose end was purged and is the stream's end: the consumer sees nothing pending and
// the leader confirms the remainder is absent, so the read ends cleanly and counts it.
func TestRangeReaderWideRangeEndingInPurgedTail(t *testing.T) {
	rig := newRangeRig(t, 320)
	require.NoError(t, rig.mgr.js.DeleteMsg(rig.stream, 319))
	require.NoError(t, rig.mgr.js.DeleteMsg(rig.stream, 320))
	rd := rig.open(t, 2, 320)
	rd.(*consumerRangeReader).fetchWait = 100 * time.Millisecond
	require.Equal(t, seqRange(2, 318), drainRange(t, rd))
	require.Equal(t, RangeStats{Present: 317, Absent: 2}, rangeStats(t, rd))
}

// laggingConsumer makes the reader's consumer behave as one placed on a follower that has not
// applied the stream's tail: its fetches time out and its info says nothing is pending and
// nothing is outstanding, for the next `lies` rounds (forever when lies < 0).
func laggingConsumer(r *consumerRangeReader, lies int) {
	r.fetchWait = 50 * time.Millisecond
	r.fetch = func() ([]*nats.Msg, error) {
		if lies != 0 {
			return nil, nats.ErrTimeout
		}
		return r.sub.Fetch(fetchBatch, nats.MaxWait(r.fetchWait))
	}
	r.info = func() (*nats.ConsumerInfo, error) {
		if lies != 0 {
			if lies > 0 {
				lies--
			}
			return &nats.ConsumerInfo{}, nil // Delivered.Consumer 0, NumPending 0
		}
		return r.sub.ConsumerInfo()
	}
}

// The consumer's "nothing pending" is not the stream's. Here it says so while the leader holds
// the whole range; the read must not end, must not count those messages absent, and must
// deliver them once the consumer catches up.
func TestRangeReaderDoesNotTrustAConsumerThatSaysNothingIsPending(t *testing.T) {
	rig := newRangeRig(t, 320)
	rd := rig.open(t, 2, 300)
	cr := rd.(*consumerRangeReader)
	laggingConsumer(cr, 2)
	got := drainRange(t, rd)
	require.Equal(t, seqRange(2, 300), got, "a lagging consumer's empty answer must not turn present messages into absences")
	require.Equal(t, RangeStats{Present: 299}, rangeStats(t, rd))
	require.Equal(t, 2, cr.recreates)
}

// A consumer that never recovers fails the read loudly after a bounded number of
// re-creations; it is never an io.EOF with the range counted absent.
func TestRangeReaderGivesUpOnAConsumerThatKeepsLying(t *testing.T) {
	rig := newRangeRig(t, 320)
	rd := rig.open(t, 2, 300)
	cr := rd.(*consumerRangeReader)
	laggingConsumer(cr, -1)
	_, err := rd.Read(context.Background())
	require.Error(t, err)
	require.NotEqual(t, io.EOF, err)
	require.ErrorContains(t, err, "giving up")
	require.Equal(t, maxRangeRecreates+1, cr.recreates)
	require.Equal(t, RangeStats{Incomplete: true}, rd.(RangeStatser).RangeStats(),
		"after the failure nothing past the point reached is claimed absent")
}

// A consumer that reports messages pending but delivers none fails after a bounded number of
// fetches, and says it stalled.
func TestRangeReaderStallIsBounded(t *testing.T) {
	rig := newRangeRig(t, 320)
	rd := rig.open(t, 2, 300)
	cr := rd.(*consumerRangeReader)
	cr.fetchWait = 10 * time.Millisecond
	cr.maxStuck = 3
	cr.fetch = func() ([]*nats.Msg, error) { return nil, nats.ErrTimeout }
	cr.info = func() (*nats.ConsumerInfo, error) { return &nats.ConsumerInfo{NumPending: 7}, nil }
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, err := rd.Read(ctx)
	require.ErrorContains(t, err, "stalled")
	require.Equal(t, 4, cr.timeouts)
}

// rangeCluster starts a plain three-node JetStream cluster on loopback and returns a client
// URL once a three-replica stream can be placed. A clustered server holds back its listeners
// until it can reach a meta quorum, so every server is given the route ports of the other two
// up front and all three are started before any is waited on.
func rangeCluster(t *testing.T) string {
	t.Helper()
	name := fmt.Sprintf("rangecl-%d", os.Getpid())
	ports := make([]int, 3)
	for i := range ports {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		ports[i] = l.Addr().(*net.TCPAddr).Port
		l.Close()
	}
	var servers []*natsserver.Server
	for i := 0; i < 3; i++ {
		var routes []*url.URL
		for j, port := range ports {
			if j != i {
				routes = append(routes, &url.URL{Scheme: "nats-route", Host: fmt.Sprintf("127.0.0.1:%d", port)})
			}
		}
		srv, err := natsserver.NewServer(&natsserver.Options{
			ServerName: fmt.Sprintf("rangecl-%d", i),
			Host:       "127.0.0.1",
			Port:       -1,
			JetStream:  true,
			StoreDir:   dctest.JetStreamStoreDir(t),
			NoLog:      true,
			NoSigs:     true,
			Cluster:    natsserver.ClusterOpts{Name: name, Host: "127.0.0.1", Port: ports[i]},
			Routes:     routes,
		})
		require.NoError(t, err)
		go srv.Start()
		t.Cleanup(srv.Shutdown)
		servers = append(servers, srv)
	}
	for i, srv := range servers {
		require.True(t, srv.ReadyForConnections(60*time.Second), "cluster server %d not ready", i)
	}
	return servers[0].ClientURL()
}

// On a real three-node cluster, with a three-replica stream, the reader's answers are the
// stream's: purged sequences are absent, the rest arrive in order, on both paths, and a range
// past the last sequence is refused.
func TestRangeReaderOnACluster(t *testing.T) {
	const suffix = streams.InboundEvents
	nc, err := nats.Connect(rangeCluster(t))
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := nc.JetStream()
	require.NoError(t, err)

	mgr := &NatsManager{Microservice: &core.Microservice{InstanceId: "test"}, js: js}
	stream := StreamName("test", suffix)
	subject := StreamSubject("test", suffix)
	deadline := time.Now().Add(60 * time.Second)
	for {
		_, err = js.AddStream(&nats.StreamConfig{Name: stream, Subjects: []string{subject}, Replicas: 3})
		if err == nil {
			break
		}
		require.True(t, time.Now().Before(deadline), "cluster never placed a three-replica stream: %v", err)
		time.Sleep(250 * time.Millisecond)
	}
	concrete := ConcreteSubjectFor("test", "acme", suffix, "dev")
	for i := 1; i <= 320; i++ {
		_, err := js.Publish(concrete, []byte(fmt.Sprint(i)))
		require.NoError(t, err)
	}
	for _, s := range []uint64{12, 13, 150, 299, 300} {
		require.NoError(t, js.DeleteMsg(stream, s))
	}

	narrow, err := mgr.NewRangeReader(suffix, 10, 20)
	require.NoError(t, err)
	t.Cleanup(func() { _ = narrow.Close() })
	require.Equal(t, seqRange(10, 20, 12, 13), drainRange(t, narrow))
	require.Equal(t, RangeStats{Present: 9, Absent: 2}, rangeStats(t, narrow))

	wide, err := mgr.NewRangeReader(suffix, 2, 310)
	require.NoError(t, err)
	t.Cleanup(func() { _ = wide.Close() })
	require.Equal(t, seqRange(2, 310, 12, 13, 150, 299, 300), drainRange(t, wide))
	require.Equal(t, RangeStats{Present: 304, Absent: 5}, rangeStats(t, wide))

	_, err = mgr.NewRangeReader(suffix, 300, 321)
	require.Error(t, err)
}
