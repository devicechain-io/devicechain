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
)

// The replay and preview readers read a stream's past by sequence. These hold them to what the
// ephemeral consumers they replaced delivered: the same messages, in the same order, with the same
// fields, up to the head captured at open and not a message past it; and to the properties the
// consumer gave for free, which a read by sequence has to provide itself.

func openReplayAt(t *testing.T, rig *rangeRig, startSeq uint64) (ReplayReader, uint64) {
	t.Helper()
	rd, head, err := rig.mgr.NewReplayReader(rangeSuffix, startSeq)
	require.NoError(t, err)
	t.Cleanup(func() { _ = rd.Close() })
	return rd, head
}

func TestReplayReadsToTheHeadInOrderAndCreatesNothing(t *testing.T) {
	rig := newRangeRig(t, 300)
	rd, head := openReplayAt(t, rig, 1)
	require.EqualValues(t, 300, head)
	require.Zero(t, rig.consumers(t), "a replay leaves no consumer on the broker")

	// Messages published after the open belong to the live reader.
	for i := 301; i <= 310; i++ {
		_, err := rig.mgr.js.Publish(ConcreteSubjectFor("test", "acme", rangeSuffix, "dev"), []byte(fmt.Sprint(i)))
		require.NoError(t, err)
	}
	require.Equal(t, seqRange(1, 300), drainRange(t, rd))
	require.Zero(t, rig.consumers(t))
	_, err := rd.Read(context.Background())
	require.Equal(t, io.EOF, err, "the end is final")
}

func TestReplayFromAStartSequenceAndTheEdges(t *testing.T) {
	rig := newRangeRig(t, 300)
	rd, head := openReplayAt(t, rig, 150)
	require.EqualValues(t, 300, head)
	require.Equal(t, seqRange(150, 300), drainRange(t, rd))

	// A snapshot ahead of the stream (it was recreated or truncated) has nothing to replay.
	rd, head = openReplayAt(t, rig, 301)
	require.EqualValues(t, 300, head)
	require.Empty(t, drainRange(t, rd))

	// A start of zero means the beginning.
	rd, _ = openReplayAt(t, rig, 0)
	require.Equal(t, seqRange(1, 300), drainRange(t, rd))

	// A stream that does not exist has nothing to replay, and says its head is 0.
	empty := newRangeRig(t, 0)
	require.NoError(t, empty.mgr.js.DeleteStream(empty.stream))
	rd, head = openReplayAt(t, empty, 1)
	require.Zero(t, head)
	require.Empty(t, drainRange(t, rd))
}

// Evicted below the head while the replay runs: the range it was opened for is gone, and it ends
// cleanly, as the consumer-backed replay did.
func TestReplayOfARangeThatAgesOutEndsCleanly(t *testing.T) {
	rig := newRangeRig(t, 200)
	rd, _ := openReplayAt(t, rig, 1)
	require.NoError(t, rig.mgr.js.PurgeStream(rig.stream, &nats.StreamPurgeRequest{Sequence: 201}))
	require.Empty(t, drainRange(t, rd))
	require.Equal(t, RangeStats{Absent: 200}, rangeStats(t, rd))
}

// A cancelled context ends a replay with io.EOF, as it always has (the caller's own context says
// why); a failure of the read itself never does.
func TestReplayEndsWithEOFWhenItsContextEnds(t *testing.T) {
	rig := newRangeRig(t, 300)
	rd, _ := openReplayAt(t, rig, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := rd.Read(ctx)
	require.Equal(t, io.EOF, err)
}

// A replica that answers with messages already read (a batch that starts earlier than asked) does
// not make the replay hand any out twice.
func TestReplaySkipsCopiesItAlreadyReturned(t *testing.T) {
	rig := newRangeRig(t, 300)
	rd, _ := openReplayAt(t, rig, 1)
	sr := rd.(*sequenceReader)
	sr.get = func(ctx context.Context, req directBatchRequest) (directBatch, error) {
		if req.Seq > 5 {
			req.Seq -= 5 // five messages already read come back first
		}
		return directGetBatch(ctx, rig.mgr.nc, rig.stream, req)
	}
	require.Equal(t, seqRange(1, 300), drainRange(t, rd), "strictly ascending, exactly the range, no copies")
}

// A read that fails stays failed: no later Read may answer io.EOF, which would end the replay as if
// it were complete.
func TestReplayFailureIsFinal(t *testing.T) {
	rig := newRangeRig(t, 300)
	rd, _ := openReplayAt(t, rig, 1)
	sr := rd.(*sequenceReader)
	boom := fmt.Errorf("the broker went away")
	sr.get = func(context.Context, directBatchRequest) (directBatch, error) { return directBatch{}, boom }
	_, first := rd.Read(context.Background())
	require.ErrorIs(t, first, boom)
	require.NotEqual(t, io.EOF, first)
	for i := 0; i < 3; i++ {
		_, again := rd.Read(context.Background())
		require.Equal(t, first, again)
	}
}

// From a time: the first message at or after it, then the rest to the head.
func TestReplayFromATimeStartsAtTheFirstMessageAtOrAfterIt(t *testing.T) {
	rig := newRangeRig(t, 5)
	time.Sleep(1100 * time.Millisecond)
	mid := time.Now()
	time.Sleep(50 * time.Millisecond)
	for i := 6; i <= 10; i++ {
		_, err := rig.mgr.js.Publish(ConcreteSubjectFor("test", "acme", rangeSuffix, "dev"), []byte(fmt.Sprint(i)))
		require.NoError(t, err)
	}

	rd, first, err := rig.mgr.NewReplayReaderFromTime(rangeSuffix, mid)
	require.NoError(t, err)
	t.Cleanup(func() { _ = rd.Close() })
	require.False(t, first.IsZero())
	require.True(t, first.Before(mid), "the first retained message predates the start, so the caller can tell nothing was evicted")
	require.Equal(t, seqRange(6, 10), drainRange(t, rd))

	// Predating everything: all of it, and the stream's first time to compare against.
	rd, first, err = rig.mgr.NewReplayReaderFromTime(rangeSuffix, time.Now().Add(-time.Hour))
	require.NoError(t, err)
	t.Cleanup(func() { _ = rd.Close() })
	require.Equal(t, seqRange(1, 10), drainRange(t, rd))
	require.False(t, first.IsZero())

	// After the last message: an empty window, no request made.
	rd, _, err = rig.mgr.NewReplayReaderFromTime(rangeSuffix, time.Now().Add(time.Hour))
	require.NoError(t, err)
	require.Empty(t, drainRange(t, rd))
	require.Zero(t, rig.consumers(t))
}

// 🔴 THE GOLDEN COMPARISON. The same stream, with deletions, a purge below the start, headers and
// a dedup id on the messages, is read once through the ephemeral consumer the replay used to be
// and once through the read by sequence. Every message must be the same in every field a caller
// sees: subject, payload, headers, stream sequence, delivery count and append time.
func TestTheReplayReturnsWhatTheEphemeralConsumerReturned(t *testing.T) {
	rig := newRangeRig(t, 0)
	subject := ConcreteSubjectFor("test", "acme", rangeSuffix, "dev")
	for i := 1; i <= 700; i++ {
		h := nats.Header{}
		h.Set("Dc-Kind", fmt.Sprintf("kind-%d", i%7))
		h.Set("Nats-Msg-Id", fmt.Sprintf("id-%d", i))
		sub := subject
		if i%11 == 0 {
			sub = ConcreteSubjectFor("test", "globex", rangeSuffix, fmt.Sprintf("dev-%d", i%5))
		}
		_, err := rig.mgr.js.PublishMsg(&nats.Msg{Subject: sub, Header: h, Data: []byte(fmt.Sprint(i))})
		require.NoError(t, err)
	}
	for _, s := range []uint64{3, 4, 5, 250, 251, 699} {
		require.NoError(t, rig.mgr.js.DeleteMsg(rig.stream, s))
	}
	require.NoError(t, rig.mgr.js.PurgeStream(rig.stream, &nats.StreamPurgeRequest{Sequence: 101})) // 1..100 gone

	const start = 50 // below the purge: both readers start at the first retained message
	type seen struct {
		Subject string
		Value   string
		Headers map[string]string
		Seq     uint64
		Deliv   int
		At      time.Time
	}
	view := func(m Message) seen {
		return seen{m.Subject, string(m.Value), m.Headers, m.StreamSeq, m.NumDelivered, m.AppendTime}
	}

	// The reference: an ephemeral pull consumer, as the replay used to build one.
	sub, err := rig.mgr.js.PullSubscribe(StreamSubject("test", rangeSuffix), "",
		nats.BindStream(rig.stream), nats.StartSequence(start), nats.AckExplicit())
	require.NoError(t, err)
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	info, err := rig.mgr.js.StreamInfo(rig.stream)
	require.NoError(t, err)
	var want []seen
	for len(want) == 0 || want[len(want)-1].Seq < info.State.LastSeq {
		msgs, err := sub.Fetch(64, nats.MaxWait(5*time.Second))
		require.NoError(t, err)
		for _, nm := range msgs {
			seq, deliv, at := msgMeta(nm)
			m := NewConsumedMessage(nm.Subject, nm.Data, deliv, natsHeaders(nm), nil)
			m.StreamSeq, m.AppendTime = seq, at
			want = append(want, view(m))
			require.NoError(t, nm.Ack())
		}
	}

	rd, head := openReplayAt(t, rig, start)
	require.Equal(t, info.State.LastSeq, head)
	var got []seen
	for {
		m, err := rd.Read(context.Background())
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		got = append(got, view(m))
	}
	require.Equal(t, want, got, "the read by sequence differs from the ephemeral consumer's")
	require.Len(t, got, 597, "the corpus is what the test says it is (101..700 less three deleted)")
}

// 🔴 A STORED MESSAGE'S OWN HEADERS ARE NOT THE STREAM'S WORD. A direct get returns the message's
// own headers first and appends the stream's (subject, sequence, append time) after them, so a
// message whose writer set a header of the same name carries two values. The reader takes the
// stream's: the subject a tenant is read from, and the sequence the order and completeness checks
// rest on, can never come from what was published. And a message that carries a header named
// Status is a message, not the end of a batch.
func TestTheReplayTakesSubjectAndSequenceFromTheStreamNotTheMessage(t *testing.T) {
	rig := newRangeRig(t, 0)
	acme := ConcreteSubjectFor("test", "acme", rangeSuffix, "dev")
	globex := ConcreteSubjectFor("test", "globex", rangeSuffix, "dev")
	for i := 1; i <= 300; i++ {
		h := nats.Header{}
		h.Set("Nats-Subject", globex)
		h.Set("Nats-Sequence", "999999")
		h.Set("Nats-Time-Stamp", "2001-01-01T00:00:00Z")
		if i%3 == 0 {
			h.Set("Status", "204")
		}
		_, err := rig.mgr.js.PublishMsg(&nats.Msg{Subject: acme, Header: h, Data: []byte(fmt.Sprint(i))})
		require.NoError(t, err)
	}
	rd, _ := openReplayAt(t, rig, 1)
	var seqs []uint64
	for {
		m, err := rd.Read(context.Background())
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		require.Equal(t, acme, m.Subject, "the subject is the one the message was stored under")
		require.True(t, m.AppendTime.After(time.Now().Add(-time.Hour)), "the append time is the stream's")
		seqs = append(seqs, m.StreamSeq)
	}
	require.Equal(t, seqRange(1, 300), seqs)
}

// The leader's state is asked for when the holes cannot be accounted for from what it said last,
// not once per hole: a purged tenant's messages interleaved with the rest leave a hole every few
// sequences, and each state request lists every deleted sequence in the stream.
func TestTheReplayAsksTheLeaderOnceForHolesItAlreadyKnows(t *testing.T) {
	rig := newRangeRig(t, 1500)
	for s := uint64(5); s < 1500; s += 5 { // 299 holes, the last message kept
		require.NoError(t, rig.mgr.js.DeleteMsg(rig.stream, s))
	}
	rd, _ := openReplayAt(t, rig, 1)
	sr := rd.(*sequenceReader)
	asked := 0
	sr.src.info = func(ctx context.Context) (*nats.StreamInfo, error) {
		asked++
		return rig.mgr.js.StreamInfo(rig.stream, &nats.StreamInfoRequest{DeletedDetails: true}, nats.Context(ctx))
	}
	got := drainRange(t, rd)
	require.Len(t, got, 1201)
	require.Equal(t, RangeStats{Present: 1201, Absent: 299}, rangeStats(t, rd))
	require.Equal(t, 1, asked, "one state request accounts for every hole that was there before the read")
}
