// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/rs/zerolog/log"

	"github.com/devicechain-io/dc-microservice/streams"
)

// READING HISTORY BY SEQUENCE.
//
// A replay (the engine catching up from its snapshot), a live-gap fill and a rule preview all read
// a stream's past: a bounded run of messages in order. They used to do it through an EPHEMERAL
// pull consumer, whose name the client library chooses, so a login that needed one had to be
// granted the right to create, fetch from, acknowledge and delete a consumer under ANY name on the
// stream, including another area's durable. This reads the same messages with batched direct
// gets instead: the request names a sequence (or a time) and a count, the stream answers with up
// to that many messages and an end marker, and nothing is created on the broker. The grant is a
// read of one stream, and no area needs a wildcard consumer name on a stream that carries trust.
//
// What the consumer gave for free, this has to do itself:
//
//   - ORDER AND COMPLETENESS. A batch is ascending, so a hole between the sequence asked for and
//     the one returned is a message the stream no longer holds. It is never skipped on the
//     answer's word alone: it counts as absent only if the stream's LEADER says so (below its
//     first retained sequence, or in the deleted set its state reports). The broker answers a
//     direct get from any replica, and a replica that has not caught up answers as though the
//     tail were not there.
//   - NOTHING PAST THE HEAD. The range ends where it was opened to end; whatever arrived after is
//     the live reader's.
//   - NO PLAUSIBLE SHORT RESULT. A reply that never comes, or an end marker with the range not
//     reached and the leader holding the next sequence, is retried and then an error; it is never
//     taken for the end of the range.

const (
	// sequenceBatch is how many messages one request asks for, sequenceBatchBytes the most bytes
	// the stream answers with (it always answers with at least one message).
	sequenceBatch      = 256
	sequenceBatchBytes = 4 << 20

	// sequenceReplyWait bounds the wait for the next message of a batch.
	sequenceReplyWait = 5 * time.Second

	// maxReplayStuckFetches bounds how long a read waits on a stream whose leader holds the next
	// sequence while no replica returns it: a stuck or lagging broker, not a drained one. Reaching
	// it fails the read LOUDLY rather than ending it short of the head, which would hand the unread
	// tail to a live reader that is not ordered. With sequenceStuckWait between tries it is tens
	// of seconds.
	maxReplayStuckFetches = 30
	sequenceStuckWait     = 500 * time.Millisecond
)

// serverHeaders are the headers a direct get adds to the message it returns; the message's own
// headers are what is left.
var serverHeaders = []string{
	"Nats-Stream", "Nats-Subject", "Nats-Sequence", "Nats-Time-Stamp", "Nats-Num-Pending", "Nats-Last-Sequence",
}

// serverHeader is the value the stream gave a header it adds to a direct-get reply. The reply
// carries the stored message's own headers FIRST and the stream's appended after them, so a
// message whose writer set a header of the same name (Nats-Subject, Nats-Sequence) has two values
// and Header.Get would return the writer's. The subject a tenant is read from and the sequence the
// order and completeness checks rest on are the stream's word, never the publisher's: the LAST
// value is the one the stream wrote.
func serverHeader(h nats.Header, key string) string {
	if v := h.Values(key); len(v) > 0 {
		return v[len(v)-1]
	}
	return ""
}

// directBatchRequest is the JSON body of a batched direct get.
type directBatchRequest struct {
	Seq       uint64     `json:"seq,omitempty"`
	StartTime *time.Time `json:"start_time,omitempty"`
	Batch     int        `json:"batch"`
	MaxBytes  int        `json:"max_bytes,omitempty"`
}

// directBatch is what a batched direct get returned: the messages, and whether the stream said it
// had none to give (a 404) rather than ending the batch (a 204).
type directBatch struct {
	msgs []*nats.Msg
	none bool
}

// directGetBatch asks stream for a batch of messages and collects the answer.
//
// The reply is a run of messages ending in a status message: 204 EOB when the batch is complete,
// 404 when the stream has nothing at or after the start, 503 when no replica answers the subject
// (the stream does not allow direct gets), and anything else is an error the stream gave. A reply
// that stops without an end marker returns what arrived, and ErrTimeout when nothing did.
func directGetBatch(ctx context.Context, nc *nats.Conn, stream string, req directBatchRequest) (directBatch, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return directBatch{}, err
	}
	// The connection's own inbox prefix: the login may subscribe to nothing else.
	inbox := nc.NewInbox()
	sub, err := nc.SubscribeSync(inbox)
	if err != nil {
		return directBatch{}, err
	}
	defer func() { _ = sub.Unsubscribe() }()
	if err := nc.PublishRequest("$JS.API.DIRECT.GET."+stream, inbox, body); err != nil {
		return directBatch{}, err
	}
	var out directBatch
	for {
		wctx, cancel := context.WithTimeout(ctx, sequenceReplyWait)
		m, err := sub.NextMsgWithContext(wctx)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return out, ctx.Err()
			}
			if len(out.msgs) > 0 {
				return out, nil // a batch that stopped early: what arrived is good
			}
			return out, nats.ErrTimeout
		}
		// A message always carries the sequence the stream appends; an end marker or an error never
		// does. A stored message's own header named Status does not make it a status.
		if len(m.Header.Values("Nats-Sequence")) > 0 {
			out.msgs = append(out.msgs, m)
			continue
		}
		switch status := serverHeader(m.Header, "Status"); status {
		case "":
			return out, fmt.Errorf("stream %s answered a direct get with a reply that is neither a message nor a status", stream)
		case "204":
			return out, nil
		case "404":
			out.none = len(out.msgs) == 0
			return out, nil
		case "503":
			return out, fmt.Errorf("no replica of stream %s answers a direct get: the stream does not allow them "+
				"(the broker administrator sets AllowDirect on the streams that are read by sequence)", stream)
		default:
			return out, fmt.Errorf("stream %s answered a direct get with status %s %s", stream, status, m.Header.Get("Description"))
		}
	}
}

// sequenceReader reads the stream sequences [next, to] in ascending order through batched direct
// gets. It is the ReplayReader NewReplayReader and NewReplayReaderFromTime return, and the wide
// path of NewRangeReader.
type sequenceReader struct {
	src      *rangeSource
	nc       *nats.Conn
	from, to uint64
	// next is the next sequence to ask for; 0 means nothing has been read and the first request
	// starts at startTime.
	next      uint64
	startTime time.Time

	pending []Message
	// deferred is an error found part way through a batch, after the messages before it were
	// queued: they are returned first, then this, so a failure never hides what was read in order.
	deferred        error
	present, absent uint64
	finished        bool
	failed          error
	stuck           int
	// eofOnCancel ends the read with io.EOF when the context ends, as the replay always has; a
	// range read returns the context's error instead, because a short range is not a complete one.
	eofOnCancel bool

	// get is the batched request; nil means the real one. A test replaces it.
	get func(ctx context.Context, req directBatchRequest) (directBatch, error)
	// wait is the pause between tries on a stuck stream; a test shortens it.
	wait time.Duration
}

func (r *sequenceReader) Read(ctx context.Context) (Message, error) {
	if r.failed != nil {
		return Message{}, r.failed
	}
	m, err := r.read(ctx)
	if err != nil && !errors.Is(err, io.EOF) {
		r.failed = err
	}
	return m, err
}

func (r *sequenceReader) read(ctx context.Context) (Message, error) {
	for {
		if len(r.pending) > 0 {
			m := r.pending[0]
			r.pending = r.pending[1:]
			return m, nil
		}
		if r.deferred != nil {
			return Message{}, r.deferred
		}
		if r.finished || (r.next != 0 && r.next > r.to) {
			r.finished = true
			return Message{}, io.EOF
		}
		if err := ctx.Err(); err != nil {
			if r.eofOnCancel {
				return Message{}, io.EOF
			}
			return Message{}, err
		}
		if err := r.fill(ctx); err != nil {
			if r.eofOnCancel && ctx.Err() != nil {
				return Message{}, io.EOF
			}
			return Message{}, err
		}
	}
}

// fill asks for the next batch and queues what it can account for.
func (r *sequenceReader) fill(ctx context.Context) error {
	req := directBatchRequest{Batch: sequenceBatch, MaxBytes: sequenceBatchBytes}
	if r.next == 0 {
		t := r.startTime
		req.StartTime = &t
	} else {
		req.Seq = r.next
	}
	var b directBatch
	var err error
	if r.get != nil {
		b, err = r.get(ctx, req)
	} else {
		b, err = directGetBatch(ctx, r.nc, r.src.stream, req)
	}
	switch {
	case err == nil:
	case errors.Is(err, nats.ErrTimeout):
		return r.nothingReturned(ctx)
	default:
		return fmt.Errorf("read of stream %s: %w", r.src.stream, err)
	}
	if b.none || len(b.msgs) == 0 {
		return r.nothingReturned(ctx)
	}
	r.stuck = 0
	err = r.queue(ctx, b.msgs)
	if err != nil && len(r.pending) > 0 {
		r.deferred, err = err, nil
	}
	return err
}

// queue accounts for the messages of a batch and queues the ones in range, in order.
func (r *sequenceReader) queue(ctx context.Context, msgs []*nats.Msg) error {
	for _, m := range msgs {
		seq, err := strconv.ParseUint(serverHeader(m.Header, "Nats-Sequence"), 10, 64)
		if err != nil {
			return fmt.Errorf("read of stream %s: a reply carries no sequence (%q)", r.src.stream, serverHeader(m.Header, "Nats-Sequence"))
		}
		if r.next != 0 && seq < r.next {
			continue // a replica answered with what was already read
		}
		if seq > r.to {
			// Everything the range still wanted lies before this message and is gone.
			if r.next != 0 && r.next <= r.to {
				if err := r.accountForGap(ctx, r.next, r.to); err != nil {
					return err
				}
			}
			r.next = r.to + 1
			r.finished = true
			break
		}
		if r.next != 0 && seq > r.next {
			if err := r.accountForGap(ctx, r.next, seq-1); err != nil {
				return err
			}
		}
		msg, err := directToMessage(m)
		if err != nil {
			return fmt.Errorf("read of stream %s: %w", r.src.stream, err)
		}
		r.pending = append(r.pending, msg)
		r.present++
		r.next = seq + 1
	}
	return nil
}

// accountForGap establishes, from the stream's leader, that every sequence in [lo, hi] is gone.
// A sequence the leader still holds that the answer skipped is an error, never an absence.
//
// The leader is asked again only when what it said last cannot account for the gap, as the narrow
// path's leaderConfirmsAbsent does. A sequence once below the first retained one, or once deleted,
// never comes back, so a cached absence stays true; and the state request lists EVERY deleted
// sequence in the stream, which the leader builds by walking its store. A range with a purged
// tenant's messages interleaved has a hole in nearly every batch, and a state request per batch
// would make a long replay cost the leader a walk of its store per 256 messages.
func (r *sequenceReader) accountForGap(ctx context.Context, lo, hi uint64) error {
	if !r.src.allKnownAbsent(lo, hi) {
		if err := r.src.refresh(ctx); err != nil {
			return err
		}
	}
	for s := lo; s <= hi; s++ {
		if !r.src.knownAbsent(s) {
			return fmt.Errorf("read of stream %s: sequence %d was skipped by the answer, but the stream leader does not list it "+
				"as deleted or evicted (first %d, last %d); refusing to count it absent", r.src.stream, s, r.src.firstSeq, r.src.lastSeq)
		}
		r.absent++
	}
	return nil
}

// nothingReturned handles a request that returned no message at r.next: either the sequences from
// there are gone (and the leader says so), or a replica has not caught up (and it is asked again).
func (r *sequenceReader) nothingReturned(ctx context.Context) error {
	// A stream that does not answer direct gets never answers: say so now, not after the tries.
	if err := r.src.refresh(ctx); err != nil {
		return err
	}
	if r.src.noDirect {
		return fmt.Errorf("read of stream %s: the stream does not allow direct gets, which a read by sequence needs "+
			"(the broker administrator sets AllowDirect on the streams that are read by sequence; run `dcctl broker reconcile`)", r.src.stream)
	}
	if r.next == 0 {
		// A start time with nothing at or after it on the replica that answered.
		r.stuck++
		if r.stuck > maxReplayStuckFetches {
			return fmt.Errorf("read of stream %s from %s stalled: no replica returned a message at or after it in %d tries",
				r.src.stream, r.startTime.Format(time.RFC3339), r.stuck)
		}
		return r.pause(ctx)
	}
	// Past everything the stream retains below `to`: the first retained sequence moved beyond the
	// head (an eviction, as the replay has always handled it).
	n := r.next
	for n <= r.to && r.src.knownAbsent(n) {
		n++
	}
	if n > r.next {
		if n > r.to+1 {
			n = r.to + 1
		}
		log.Debug().Str("stream", r.src.stream).Uint64("from", r.next).Uint64("to", n-1).
			Msg("Sequences not held by the stream (evicted or deleted); the leader accounts for them")
		r.absent += n - r.next
		r.next = n
		return nil
	}
	if r.next > r.src.lastSeq {
		return fmt.Errorf("read of stream %s: sequence %d is past the stream's last sequence %d", r.src.stream, r.next, r.src.lastSeq)
	}
	r.stuck++
	if r.stuck > maxReplayStuckFetches {
		return fmt.Errorf("read stalled: stream %s holds sequence %d on its leader but no replica returned it in %d tries",
			r.src.stream, r.next, r.stuck)
	}
	return r.pause(ctx)
}

func (r *sequenceReader) pause(ctx context.Context) error {
	wait := r.wait
	if wait <= 0 {
		wait = sequenceStuckWait
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(wait):
		return nil
	}
}

// RangeStats is what the read established. It is complete once Read has returned io.EOF.
func (r *sequenceReader) RangeStats() RangeStats {
	return RangeStats{Present: r.present, Absent: r.absent, Incomplete: r.failed != nil}
}

// Close releases nothing: a read by sequence leaves nothing on the broker.
func (r *sequenceReader) Close() error { return nil }

// directToMessage turns a direct-get reply into the consumed-message shape the readers return:
// the message's own headers without the ones the broker added, its stream sequence and append time.
func directToMessage(m *nats.Msg) (Message, error) {
	seq, err := strconv.ParseUint(serverHeader(m.Header, "Nats-Sequence"), 10, 64)
	if err != nil {
		return Message{}, fmt.Errorf("a reply carries no sequence (%q)", serverHeader(m.Header, "Nats-Sequence"))
	}
	appended, err := time.Parse(time.RFC3339Nano, serverHeader(m.Header, "Nats-Time-Stamp"))
	if err != nil {
		return Message{}, fmt.Errorf("sequence %d carries no append time (%q)", seq, serverHeader(m.Header, "Nats-Time-Stamp"))
	}
	var headers map[string]string
	for k := range m.Header {
		if slices.Contains(serverHeaders, k) {
			continue
		}
		if headers == nil {
			headers = make(map[string]string, len(m.Header))
		}
		headers[k] = m.Header.Get(k)
	}
	msg := NewConsumedMessage(serverHeader(m.Header, "Nats-Subject"), m.Data, 1, headers, nil)
	msg.StreamSeq = seq
	// As the consumer metadata builds it (time.Unix), so a time compares equal in every way.
	msg.AppendTime = time.Unix(0, appended.UnixNano())
	return msg, nil
}

// NewReplayReader opens an in-order read of the suffix's stream from startSeq up to the current
// head, returning the reader and that head sequence. When startSeq is already past the head
// (nothing to replay, or a snapshot ahead of a re-created or truncated stream) it returns a reader
// that immediately reports io.EOF. The head is captured once, at open, so live messages
// published during the replay are left for the durable reader.
//
// It reads by sequence (see the top of this file): it creates no consumer, acknowledges nothing and
// leaves nothing behind, and a read that dies leaves no orphan to be reaped.
func (nmgr *NatsManager) NewReplayReader(suffix string, startSeq uint64) (ReplayReader, uint64, error) {
	if err := nmgr.requireReader(suffix, streams.ReadEphemeral); err != nil {
		return nil, 0, err
	}
	name := StreamName(nmgr.Microservice.InstanceId, suffix)
	info, err := nmgr.js.StreamInfo(name)
	if err != nil {
		if errors.Is(err, nats.ErrStreamNotFound) {
			return &sequenceReader{finished: true}, 0, nil // empty/absent stream: nothing to replay
		}
		return nil, 0, err
	}
	head := info.State.LastSeq
	if startSeq == 0 {
		startSeq = 1
	}
	if startSeq > head {
		return &sequenceReader{finished: true}, head, nil
	}
	src := &rangeSource{js: nmgr.js, stream: name, firstSeq: info.State.FirstSeq, lastSeq: head}
	log.Info().Str("stream", name).Uint64("startSeq", startSeq).Uint64("head", head).
		Msg("Opened ordered replay reader")
	return &sequenceReader{src: src, nc: nmgr.nc, from: startSeq, to: head, next: startSeq, eofOnCancel: true}, head, nil
}

// NewReplayReaderFromTime opens an in-order read of the suffix's stream from the first message
// published at or after startTime up to the current head, for the replay-preview harness. It
// starts by publish TIME rather than a sequence, since a preview names a time window.
//
// It returns the stream's earliest retained message time (FirstTime) so the caller can DETECT the
// aged-out case: when the requested startTime predates FirstTime the read starts at the first
// retained message, so the caller compares startTime to the returned FirstTime and reports a
// degraded (history-evicted) preview rather than a misleadingly empty one. When the stream is
// absent or empty, or startTime is past the last retained message (an empty window), it returns a
// reader that immediately reports io.EOF, so a future-dated or empty window degrades to zero
// firings cleanly.
//
// NB: the start keys on the message's PUBLISH time (about the event's processed or arrival time),
// not its logical occurred time, so the caller must still filter delivered events to its
// occurred-time window; this only bounds the scan's lower edge.
func (nmgr *NatsManager) NewReplayReaderFromTime(suffix string, startTime time.Time) (ReplayReader, time.Time, error) {
	if err := nmgr.requireReader(suffix, streams.ReadEphemeral); err != nil {
		return nil, time.Time{}, err
	}
	name := StreamName(nmgr.Microservice.InstanceId, suffix)
	info, err := nmgr.js.StreamInfo(name)
	if err != nil {
		if errors.Is(err, nats.ErrStreamNotFound) {
			return &sequenceReader{finished: true}, time.Time{}, nil // empty/absent stream: nothing to replay
		}
		return nil, time.Time{}, err
	}
	head := info.State.LastSeq
	firstTime := info.State.FirstTime
	if head == 0 || startTime.After(info.State.LastTime) {
		return &sequenceReader{finished: true}, firstTime, nil
	}
	src := &rangeSource{js: nmgr.js, stream: name, firstSeq: info.State.FirstSeq, lastSeq: head}
	log.Info().Str("stream", name).Time("startTime", startTime).Uint64("head", head).
		Msg("Opened time-started replay reader (preview)")
	return &sequenceReader{src: src, nc: nmgr.nc, from: info.State.FirstSeq, to: head, startTime: startTime, eofOnCancel: true}, firstTime, nil
}
