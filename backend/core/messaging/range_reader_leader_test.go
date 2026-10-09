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
	src.info = (&leaderState{first: 1, last: 40, deleted: []uint64{11}}).info
	rd := &seqRangeReader{src: src, from: 10, to: 20, next: 10}
	got, err := readAll(rd)
	require.ErrorIs(t, err, boom)
	require.Equal(t, []uint64{10, 12, 13}, got)
	require.Equal(t, RangeStats{Present: 3, Absent: 1, Incomplete: true}, rd.RangeStats(),
		"after a failure the stats count only what was established")
}

// leaderState stands in for the stream leader's state request, and counts how often it was
// asked.
type leaderState struct {
	first, last uint64
	deleted     []uint64
	asked       int
}

func (l *leaderState) info(context.Context) (*nats.StreamInfo, error) {
	l.asked++
	return &nats.StreamInfo{State: nats.StreamState{FirstSeq: l.first, LastSeq: l.last, Deleted: l.deleted}}, nil
}

func notFoundFor(missing ...uint64) func(uint64, int) (*nats.RawStreamMsg, error) {
	gone := map[uint64]bool{}
	for _, m := range missing {
		gone[m] = true
	}
	return func(seq uint64, _ int) (*nats.RawStreamMsg, error) {
		if gone[seq] {
			return nil, nats.ErrMsgNotFound
		}
		return rawMsg(seq), nil
	}
}

// "No message found" is what the server answers for a sequence it holds but cannot read, as
// well as for a purged one. A sequence counts as absent only if the leader lists it as deleted
// or below its first retained sequence; one it does not account for fails the read, and is
// never skipped.
func TestRangeReaderAnUnaccountedNotFoundIsAnErrorNotAnAbsence(t *testing.T) {
	src, _ := fakeSource(1, notFoundFor(12))
	leader := &leaderState{first: 1, last: 40, deleted: []uint64{13, 14}} // 12 is not among them
	src.info = leader.info
	rd := &seqRangeReader{src: src, from: 10, to: 20, next: 10}
	got, err := readAll(rd)
	require.Error(t, err)
	require.NotEqual(t, io.EOF, err)
	require.ErrorContains(t, err, "refusing to count it absent")
	require.Equal(t, []uint64{10, 11}, got)
	require.Equal(t, RangeStats{Present: 2, Incomplete: true}, rd.RangeStats())
}

// Purged sequences are in the leader's deleted set and are absent; the set is asked for once
// for the whole range, not once per sequence.
func TestRangeReaderAPurgedSequenceIsAbsentWhenTheLeaderListsIt(t *testing.T) {
	src, _ := fakeSource(1, notFoundFor(12, 13, 17))
	leader := &leaderState{first: 1, last: 40, deleted: []uint64{12, 13, 17}}
	src.info = leader.info
	rd := &seqRangeReader{src: src, from: 10, to: 20, next: 10}
	got, err := readAll(rd)
	require.Equal(t, io.EOF, err)
	require.Equal(t, []uint64{10, 11, 14, 15, 16, 18, 19, 20}, got)
	require.Equal(t, RangeStats{Present: 8, Absent: 3}, rd.RangeStats())
	require.Equal(t, 1, leader.asked, "one state request answers every absence in the range")
}

// A message evicted after the read began is not in the deleted set, but the leader's first
// retained sequence, read after the not-found, is past it.
func TestRangeReaderASequenceEvictedDuringTheReadIsAbsent(t *testing.T) {
	src, _ := fakeSource(1, notFoundFor(10, 11))
	leader := &leaderState{first: 12, last: 40}
	src.info = leader.info
	rd := &seqRangeReader{src: src, from: 10, to: 13, next: 10}
	got, err := readAll(rd)
	require.Equal(t, io.EOF, err)
	require.Equal(t, []uint64{12, 13}, got)
	require.Equal(t, RangeStats{Present: 2, Absent: 2}, rd.RangeStats())
}

// The boundary: a not-found at exactly the first retained sequence that was current when the
// read began is not an eviction (that sequence is retained), so the leader has to account for
// it; one below is certainly gone and needs no question.
func TestRangeReaderTheFirstRetainedSequenceIsNotPresumedGone(t *testing.T) {
	src, _ := fakeSource(11, notFoundFor(11))
	leader := &leaderState{first: 11, last: 40}
	src.info = leader.info
	rd := &seqRangeReader{src: src, from: 11, to: 13, next: 11}
	_, err := readAll(rd)
	require.ErrorContains(t, err, "refusing to count it absent")

	src, _ = fakeSource(11, notFoundFor(10))
	leader = &leaderState{first: 11, last: 40}
	src.info = leader.info
	rd = &seqRangeReader{src: src, from: 10, to: 12, next: 10}
	got, err := readAll(rd)
	require.Equal(t, io.EOF, err)
	require.Equal(t, []uint64{11, 12}, got)
	require.Zero(t, leader.asked, "below the first retained sequence nothing needs asking")
}

// A remainder too long for the leader to be asked about sequence by sequence is not believed
// either: a lagging consumer's "nothing pending" over more than the verify limit must end in
// an error, never in an end-of-range with the whole remainder counted absent.
func TestRangeReaderALongRemainderIsNotBelievedFromALaggingConsumer(t *testing.T) {
	rig := newRangeRig(t, 1500)
	rd := rig.open(t, 2, 1400)
	cr := rd.(*consumerRangeReader)
	laggingConsumer(cr, -1)
	_, err := rd.Read(context.Background())
	require.Error(t, err)
	require.NotEqual(t, io.EOF, err)
	require.ErrorContains(t, err, "giving up")
}

// A range longer than the default pending-ack limit reads through, on a consumer that takes no
// acknowledgements (one that never acknowledged would stall at that limit).
func TestRangeReaderAWideRangeBeyondThePendingAckLimitReadsThrough(t *testing.T) {
	rig := newRangeRig(t, 1500)
	rd := rig.open(t, 2, 1400)
	cr := rd.(*consumerRangeReader)
	cr.fetchWait = 300 * time.Millisecond
	cr.maxStuck = 3
	ci, err := cr.sub.ConsumerInfo()
	require.NoError(t, err)
	require.Equal(t, nats.AckNonePolicy, ci.Config.AckPolicy, "the throwaway consumer takes no acknowledgements")
	require.Equal(t, seqRange(2, 1400), drainRange(t, rd))
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
