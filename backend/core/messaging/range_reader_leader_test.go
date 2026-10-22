// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

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

// A replica that has not caught up answers a direct get as though the tail were not there. The
// answer is not the stream's: with the leader holding the next sequence, the read asks again and,
// if no replica ever returns it, fails loudly after a bounded number of tries. It never ends the
// range and never counts what the leader holds as absent.
func TestRangeReaderALaggingReplicaIsNotBelieved(t *testing.T) {
	rig := newRangeRig(t, 1500)
	rd := rig.open(t, 2, 1400).(*sequenceReader)
	rd.wait = time.Millisecond
	rd.get = func(context.Context, directBatchRequest) (directBatch, error) { return directBatch{none: true}, nil }
	_, err := rd.Read(context.Background())
	require.Error(t, err)
	require.NotEqual(t, io.EOF, err)
	require.ErrorContains(t, err, "no replica returned it")
	require.Equal(t, RangeStats{Incomplete: true}, rd.RangeStats(), "after the failure nothing past the point reached is claimed absent")
	_, again := rd.Read(context.Background())
	require.Equal(t, err, again, "a failed read is final: no later Read answers io.EOF")
}

// ...and once the replica catches up the same read delivers every message.
func TestRangeReaderALaggingReplicaThatCatchesUpDeliversEverything(t *testing.T) {
	rig := newRangeRig(t, 320)
	rd := rig.open(t, 2, 300).(*sequenceReader)
	rd.wait = time.Millisecond
	lies := 3
	real := func(ctx context.Context, req directBatchRequest) (directBatch, error) {
		return directGetBatch(ctx, rig.mgr.nc, rig.stream, req)
	}
	rd.get = func(ctx context.Context, req directBatchRequest) (directBatch, error) {
		if lies > 0 {
			lies--
			return directBatch{none: true}, nil
		}
		return real(ctx, req)
	}
	require.Equal(t, seqRange(2, 300), drainRange(t, rd), "a lagging replica's empty answer must not turn present messages into absences")
	require.Equal(t, RangeStats{Present: 299}, rangeStats(t, rd))
}

// A replica that stops a batch early (a connection that drops mid-reply, a batch cut at its
// byte limit) loses nothing: the next request starts where the last message arrived.
func TestRangeReaderAShortBatchIsContinuedNotTakenForTheEnd(t *testing.T) {
	rig := newRangeRig(t, 320)
	rd := rig.open(t, 2, 300).(*sequenceReader)
	calls := 0
	rd.get = func(ctx context.Context, req directBatchRequest) (directBatch, error) {
		calls++
		b, err := directGetBatch(ctx, rig.mgr.nc, rig.stream, req)
		if len(b.msgs) > 10 {
			b.msgs = b.msgs[:10]
		}
		return b, err
	}
	require.Equal(t, seqRange(2, 300), drainRange(t, rd))
	require.Equal(t, RangeStats{Present: 299}, rangeStats(t, rd))
	require.Greater(t, calls, 25, "the batches were cut, so the read has to have asked many times")
}

// A message the answer skips that the leader still holds is an error, not an absence.
func TestRangeReaderASkippedMessageTheLeaderHoldsIsAnError(t *testing.T) {
	rig := newRangeRig(t, 320)
	rd := rig.open(t, 2, 300).(*sequenceReader)
	rd.get = func(ctx context.Context, req directBatchRequest) (directBatch, error) {
		b, err := directGetBatch(ctx, rig.mgr.nc, rig.stream, req)
		for i, m := range b.msgs {
			if m.Header.Get("Nats-Sequence") == "50" {
				b.msgs = append(b.msgs[:i:i], b.msgs[i+1:]...)
				break
			}
		}
		return b, err
	}
	var got []uint64
	for {
		m, err := rd.Read(context.Background())
		if err != nil {
			require.ErrorContains(t, err, "refusing to count it absent")
			break
		}
		got = append(got, m.StreamSeq)
	}
	require.Equal(t, seqRange(2, 49), got, "everything before the hole, and nothing after it")
	require.True(t, rd.RangeStats().Incomplete)
}

// A range longer than the default pending-ack limit reads through: nothing is acknowledged, because
// there is no consumer to acknowledge on.
func TestRangeReaderAWideRangeReadsThrough(t *testing.T) {
	rig := newRangeRig(t, 1500)
	rd := rig.open(t, 2, 1400)
	require.Equal(t, seqRange(2, 1400), drainRange(t, rd))
	require.Zero(t, rig.consumers(t))
}

// A stream that does not allow direct gets fails the wide read by saying so, instead of timing out
// or ending short.
func TestRangeReaderAStreamThatDoesNotAllowDirectGetsSaysSo(t *testing.T) {
	rig := newRangeRig(t, 320)
	info, err := rig.mgr.js.StreamInfo(rig.stream)
	require.NoError(t, err)
	cfg := info.Config
	cfg.AllowDirect = false
	_, err = rig.mgr.js.UpdateStream(&cfg)
	require.NoError(t, err)
	rd := rig.open(t, 2, 300)
	_, err = rd.Read(context.Background())
	require.ErrorContains(t, err, "does not allow")
}

// A sequence past the stream's last one is not absent, it does not exist yet: refused, on both
// the narrow and the wide path.
func TestRangeReaderRefusesARangePastTheLastSequence(t *testing.T) {
	rig := newRangeRig(t, 40)
	for _, c := range [][2]uint64{{30, 41}, {1, 300}, {41, 45}} {
		rd, err := rig.mgr.NewRangeReader(rangeSuffix, c[0], c[1])
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

// Nothing past `to` is returned: with `to` and the sequence before it deleted, the next message the
// stream hands out is beyond the range and must end it, not be returned.
func TestRangeReaderWideRangeDoesNotReturnPastTo(t *testing.T) {
	rig := newRangeRig(t, 320)
	require.NoError(t, rig.mgr.js.DeleteMsg(rig.stream, 299))
	require.NoError(t, rig.mgr.js.DeleteMsg(rig.stream, 300))
	rd := rig.open(t, 2, 300)
	require.Equal(t, seqRange(2, 298), drainRange(t, rd))
	require.Equal(t, RangeStats{Present: 297, Absent: 2}, rangeStats(t, rd))
}

// A range whose end was purged and is the stream's end: the stream returns nothing for the tail and
// the leader confirms it is absent, so the read ends cleanly and counts it.
func TestRangeReaderWideRangeEndingInPurgedTail(t *testing.T) {
	rig := newRangeRig(t, 320)
	require.NoError(t, rig.mgr.js.DeleteMsg(rig.stream, 319))
	require.NoError(t, rig.mgr.js.DeleteMsg(rig.stream, 320))
	rd := rig.open(t, 2, 320)
	require.Equal(t, seqRange(2, 318), drainRange(t, rd))
	require.Equal(t, RangeStats{Present: 317, Absent: 2}, rangeStats(t, rd))
}

// rangeCluster starts a three-node JetStream cluster on loopback and returns a client URL
// once it has formed. It is the shared fixture (dctest.StartJetStreamCluster), not a cluster
// built here: this test used to pick three route ports by binding and releasing them, then
// hand them to servers it started with logging off. A port another test binary's server took
// in between made a route listener fail with nothing reported anywhere, and the only symptom
// was ReadyForConnections running out its 60 seconds ("cluster server N not ready"). The
// fixture holds every address a server dials for the cluster's whole life, gives the cluster a
// name no other has, waits for a slow server rather than restarting it, and names what is
// missing when it gives up.
func rangeCluster(t *testing.T) string {
	t.Helper()
	return dctest.StartJetStreamCluster(t, 3)[0].ClientURL()
}

// On a real three-node cluster, with a three-replica stream, the reader's answers are the
// stream's: purged sequences are absent, the rest arrive in order, on both paths, and a range
// past the last sequence is refused.
func TestRangeReaderOnACluster(t *testing.T) {
	const suffix = rangeSuffix
	nc, err := nats.Connect(rangeCluster(t))
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	js, err := nc.JetStream()
	require.NoError(t, err)

	mgr := &NatsManager{Microservice: &core.Microservice{InstanceId: "test"}, nc: nc, js: js}
	stream := StreamName("test", suffix)
	subject := StreamSubject("test", suffix)
	deadline := time.Now().Add(60 * time.Second)
	for {
		_, err = js.AddStream(&nats.StreamConfig{Name: stream, Subjects: []string{subject}, Replicas: 3, AllowDirect: true})
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
