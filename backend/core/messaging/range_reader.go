// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/devicechain-io/dc-microservice/streams"
	nats "github.com/nats-io/nats.go"
	"github.com/rs/zerolog/log"
)

const (
	// rangeDirectMax is the widest range read with one request/reply per sequence. At or
	// below it nothing is created on the broker, so a gap cannot cost a consumer and a
	// reply that never arrives is a timeout the caller sees, not part of a batch that
	// silently went missing. Above it a single bounded consumer is cheaper than that
	// many round trips.
	rangeDirectMax = 256

	// rangeVerifyMax is the most sequences the consumer-backed read will check one by one
	// against the stream leader before it believes "nothing more to deliver" (see
	// consumerRangeReader.restIsAbsent). A longer unproven remainder is not believed; the
	// read re-creates its consumer instead and eventually fails.
	rangeVerifyMax = 4 * rangeDirectMax

	// rangeGetTimeout bounds one by-sequence request.
	rangeGetTimeout = 5 * time.Second

	// maxRangeRecreates bounds how often the consumer-backed read re-creates its consumer
	// (after losing a delivery, or after its answer could not be confirmed against the
	// stream leader), so a broker that keeps doing either fails the read loudly instead of
	// looping.
	maxRangeRecreates = 8
)

// RangeStats is what a range read found. It is complete once Read has returned io.EOF.
// Absent counts sequences in [from, to] the stream does not hold: removed by a purge, or
// evicted by retention. They are reported, never silently skipped, so a caller can tell a
// legitimate hole from a range it simply failed to read (which is an error, not an Absent).
//
// After a Read error the stats are Incomplete: Present and Absent then count only what the
// read had established before it stopped, and say nothing about the sequences after that.
type RangeStats struct {
	Present    uint64
	Absent     uint64
	Incomplete bool
}

// RangeStatser is implemented by the reader NewRangeReader returns.
type RangeStatser interface {
	RangeStats() RangeStats
}

// NewRangeReader reads exactly the stream sequences [from, to] of the suffix's stream,
// in ascending order, each message carrying its StreamSeq. A sequence the stream no
// longer holds produces no message and is counted in RangeStats().Absent.
//
// It exists for a consumer that has found it is missing part of the stream (a hole in the
// sequences its durable handed it) and must read the missing part by position. It never
// returns a plausible short result: any read error other than "that sequence is not in the
// stream" is returned from Read, and io.EOF means every sequence in the range was either
// returned or established absent. A range reaching past the stream's last sequence is
// refused: those sequences are not absent, they do not exist yet.
//
// Every claim that a sequence is absent is checked against the stream LEADER. The broker
// answers "no message found" for several failures that are not an absence (a store being
// reset, a block that could not be read), and a consumer created without replicas is placed
// on one peer, which may be a follower still catching up, so neither its answers nor a
// single "not found" are taken as the stream's.
//
// Ranges of at most 256 sequences are read one by one by sequence, creating no consumer. A
// sequence reported not found that the leader says should exist (at or above its first
// retained sequence) is asked for once more before it is counted absent. Wider ranges skip
// the part below the stream's first retained sequence (and create no consumer at all when
// the whole range is below it) and use one bounded ephemeral pull consumer. That reader
// verifies that the consumer's delivery sequence advances by one per message and
// re-creates the consumer after the last message received when it does not, because a
// delivery the connection lost looks, in stream sequences alone, exactly like a purged
// message. It ends early only when the leader confirms that the rest of the range is
// absent.
func (nmgr *NatsManager) NewRangeReader(suffix string, from, to uint64) (ReplayReader, error) {
	if from == 0 || from > to {
		return nil, fmt.Errorf("invalid range [%d, %d]: need 1 <= from <= to", from, to)
	}
	name := StreamName(nmgr.Microservice.InstanceId, suffix)
	span := to - from + 1
	wide := span > rangeDirectMax
	if wide && streams.ShapeOf(suffix) == streams.ShapeAdvisory {
		return nil, fmt.Errorf("range read of %q: an advisory capture stream has no single subject to read through", suffix)
	}

	info, err := nmgr.js.StreamInfo(name)
	if err != nil {
		return nil, fmt.Errorf("range read of stream %s: %w", name, err)
	}
	if to > info.State.LastSeq {
		return nil, fmt.Errorf("range read of stream %s: range %d..%d reaches past the stream's last sequence %d",
			name, from, to, info.State.LastSeq)
	}
	src := &rangeSource{js: nmgr.js, stream: name, firstSeq: info.State.FirstSeq}
	if !wide {
		return &seqRangeReader{src: src, from: from, to: to, next: from}, nil
	}

	lo := from
	if info.State.FirstSeq > lo {
		lo = info.State.FirstSeq
	}
	if lo > to {
		// Everything asked for is below the first retained sequence.
		return &seqRangeReader{src: src, from: from, to: to, next: to + 1, absent: span}, nil
	}
	r := &consumerRangeReader{
		src: src, subject: StreamSubject(nmgr.Microservice.InstanceId, suffix),
		from: from, to: to, start: lo, fetchWait: fetchTimeout, maxStuck: maxReplayStuckFetches,
	}
	if err := r.open(lo); err != nil {
		return nil, err
	}
	return r, nil
}

// rangeSource is the by-sequence access to the stream leader both readers share.
type rangeSource struct {
	js     nats.JetStreamContext
	stream string
	// firstSeq is the stream's first retained sequence when the read was opened. It only
	// ever rises, so a sequence below it is certainly gone.
	firstSeq uint64
	// get is the request itself; nil means js.GetMsg. A test replaces it.
	get func(ctx context.Context, seq uint64) (*nats.RawStreamMsg, error)
}

func (s *rangeSource) rawGet(ctx context.Context, seq uint64) (*nats.RawStreamMsg, error) {
	cctx, cancel := context.WithTimeout(ctx, rangeGetTimeout)
	defer cancel()
	var raw *nats.RawStreamMsg
	var err error
	if s.get != nil {
		raw, err = s.get(cctx, seq)
	} else {
		raw, err = s.js.GetMsg(s.stream, seq, nats.Context(cctx))
	}
	if err != nil {
		return nil, err
	}
	if raw.Sequence != seq {
		return nil, fmt.Errorf("range read of stream %s: asked for seq %d, broker returned %d", s.stream, seq, raw.Sequence)
	}
	return raw, nil
}

// getOrAbsent returns the message at seq, or (nil, nil) when the stream does not hold it. A
// not-found answer is believed outright only below the first retained sequence; at or above
// it the broker is asked once more, because the server reports several read failures as "no
// message found" and a purged sequence stays purged on the second ask.
func (s *rangeSource) getOrAbsent(ctx context.Context, seq uint64) (*nats.RawStreamMsg, error) {
	for attempt := 0; ; attempt++ {
		raw, err := s.rawGet(ctx, seq)
		switch {
		case err == nil:
			return raw, nil
		case !errors.Is(err, nats.ErrMsgNotFound):
			return nil, fmt.Errorf("range read of stream %s seq %d: %w", s.stream, seq, err)
		case seq < s.firstSeq || attempt > 0:
			return nil, nil
		}
	}
}

// rawToMessage converts a by-sequence read into the consumed-message shape.
func rawToMessage(raw *nats.RawStreamMsg) Message {
	var headers map[string]string
	if len(raw.Header) > 0 {
		headers = make(map[string]string, len(raw.Header))
		for k := range raw.Header {
			headers[k] = raw.Header.Get(k)
		}
	}
	msg := NewConsumedMessage(raw.Subject, raw.Data, 1, headers, nil)
	msg.StreamSeq = raw.Sequence
	msg.AppendTime = raw.Time
	return msg
}

// seqRangeReader reads a narrow range one sequence at a time.
type seqRangeReader struct {
	src      *rangeSource
	from, to uint64
	next     uint64 // the next sequence to ask for
	present  uint64
	absent   uint64
	failed   bool
}

func (r *seqRangeReader) Read(ctx context.Context) (Message, error) {
	m, err := r.read(ctx)
	if err != nil && err != io.EOF {
		r.failed = true
	}
	return m, err
}

func (r *seqRangeReader) read(ctx context.Context) (Message, error) {
	for r.next <= r.to {
		if err := ctx.Err(); err != nil {
			return Message{}, err
		}
		raw, err := r.src.getOrAbsent(ctx, r.next)
		if err != nil {
			return Message{}, err
		}
		r.next++
		if raw == nil {
			r.absent++
			continue
		}
		r.present++
		return rawToMessage(raw), nil
	}
	return Message{}, io.EOF
}

func (r *seqRangeReader) RangeStats() RangeStats {
	return RangeStats{Present: r.present, Absent: r.absent, Incomplete: r.failed}
}

func (r *seqRangeReader) Close() error { return nil }

// consumerRangeReader reads a wide range through one ephemeral pull consumer.
type consumerRangeReader struct {
	src      *rangeSource
	subject  string
	from, to uint64
	start    uint64 // the first sequence the consumer was first opened at

	sub       *nats.Subscription
	pending   []*nats.Msg
	done      uint64 // the last stream sequence returned; 0 until the first
	lastDseq  uint64 // the consumer sequence of the last message received from this consumer
	present   uint64
	timeouts  int
	recreates int
	finished  bool
	failed    bool

	fetchWait time.Duration
	maxStuck  int
	// fetch and info are the consumer round trips; nil means the real ones. A test replaces
	// them to stand in for a consumer on a lagging replica.
	fetch func() ([]*nats.Msg, error)
	info  func() (*nats.ConsumerInfo, error)
}

func (r *consumerRangeReader) open(startSeq uint64) error {
	if r.sub != nil {
		_ = r.sub.Unsubscribe()
		r.sub = nil
	}
	sub, err := r.src.js.PullSubscribe(r.subject, "", // empty durable => ephemeral consumer
		nats.BindStream(r.src.stream),
		nats.StartSequence(startSeq),
		// Nothing is acknowledged: the consumer is throwaway, its delivery sequence still
		// advances per delivery (which is all the contiguity check reads), and an ack on a
		// work-queue stream would remove messages other readers need.
		nats.AckNone(),
		nats.InactiveThreshold(replayInactiveThreshold))
	if err != nil {
		return fmt.Errorf("range read of stream %s: open consumer at %d: %w", r.src.stream, startSeq, err)
	}
	r.sub = sub
	r.pending = nil
	r.lastDseq = 0
	return nil
}

// recreate reopens the consumer just after the last message returned.
func (r *consumerRangeReader) recreate(why string) error {
	r.recreates++
	if r.recreates > maxRangeRecreates {
		return fmt.Errorf("range read of stream %s %d..%d: giving up after %d consumer re-creations (%s)",
			r.src.stream, r.from, r.to, r.recreates-1, why)
	}
	startSeq := r.start
	if r.done >= startSeq {
		startSeq = r.done + 1
	}
	log.Warn().Str("stream", r.src.stream).Str("why", why).Uint64("from", startSeq).
		Msg("Range read cannot rely on its consumer; re-creating it")
	return r.open(startSeq)
}

func (r *consumerRangeReader) doFetch() ([]*nats.Msg, error) {
	if r.fetch != nil {
		return r.fetch()
	}
	return r.sub.Fetch(fetchBatch, nats.MaxWait(r.fetchWait))
}

func (r *consumerRangeReader) doInfo() (*nats.ConsumerInfo, error) {
	if r.info != nil {
		return r.info()
	}
	return r.sub.ConsumerInfo()
}

// restIsAbsent asks the stream leader whether the sequences after the last one returned,
// up to the end of the range, are all absent. The consumer saying it has nothing more is
// not enough: it lives on one peer, and a follower that has not applied the tail of the
// range answers "nothing pending" while the leader holds messages there.
//
// It reports false, without error, when the leader holds a message the consumer has not
// delivered or when the remainder is too long to check; either way the consumer's answer is
// not to be believed.
func (r *consumerRangeReader) restIsAbsent(ctx context.Context) (bool, error) {
	next := r.start
	if r.done >= next {
		next = r.done + 1
	}
	if next > r.to {
		return true, nil
	}
	if r.to-next+1 > rangeVerifyMax {
		return false, nil
	}
	for seq := next; seq <= r.to; seq++ {
		raw, err := r.src.getOrAbsent(ctx, seq)
		if err != nil {
			return false, err
		}
		if raw != nil {
			return false, nil
		}
	}
	return true, nil
}

func (r *consumerRangeReader) Read(ctx context.Context) (Message, error) {
	m, err := r.read(ctx)
	if err != nil && err != io.EOF {
		r.failed = true
	}
	return m, err
}

func (r *consumerRangeReader) read(ctx context.Context) (Message, error) {
	for {
		if r.finished || r.done >= r.to {
			r.finished = true
			return Message{}, io.EOF
		}
		if err := ctx.Err(); err != nil {
			return Message{}, err
		}
		if len(r.pending) == 0 {
			msgs, err := r.doFetch()
			if err != nil {
				if !errors.Is(err, nats.ErrTimeout) {
					return Message{}, fmt.Errorf("range read of stream %s: %w", r.src.stream, err)
				}
				info, ierr := r.doInfo()
				if ierr != nil {
					return Message{}, fmt.Errorf("range read of stream %s: consumer info: %w", r.src.stream, ierr)
				}
				if info.Delivered.Consumer != r.lastDseq {
					// The broker sent something this client never received.
					if err := r.recreate("tail of a batch lost"); err != nil {
						return Message{}, err
					}
					continue
				}
				if info.NumPending == 0 {
					ok, err := r.restIsAbsent(ctx)
					if err != nil {
						return Message{}, err
					}
					if ok {
						// The consumer has nothing more and the stream leader agrees: the
						// rest of the range is absent.
						r.finished = true
						return Message{}, io.EOF
					}
					if err := r.recreate("consumer reports nothing pending but the stream holds more of the range"); err != nil {
						return Message{}, err
					}
					continue
				}
				if r.timeouts++; r.timeouts > r.maxStuck {
					return Message{}, fmt.Errorf("range read stalled: stream %s has %d messages pending but delivered none in %d fetches",
						r.src.stream, info.NumPending, r.timeouts)
				}
				continue
			}
			r.timeouts = 0
			r.pending = msgs
		}
		nm := r.pending[0]
		md, err := nm.Metadata()
		if err != nil {
			return Message{}, fmt.Errorf("range read of stream %s: message without metadata: %w", r.src.stream, err)
		}
		if md.Sequence.Consumer != r.lastDseq+1 {
			if err := r.recreate(fmt.Sprintf("consumer sequence %d after %d", md.Sequence.Consumer, r.lastDseq)); err != nil {
				return Message{}, err
			}
			continue
		}
		r.pending = r.pending[1:]
		r.lastDseq = md.Sequence.Consumer
		seq := md.Sequence.Stream
		if seq > r.to {
			// Past the range: everything after belongs to the caller's live read.
			r.finished = true
			return Message{}, io.EOF
		}
		if seq <= r.done {
			continue
		}
		r.done = seq
		r.present++
		msg := NewConsumedMessage(nm.Subject, nm.Data, int(md.NumDelivered), natsHeaders(nm), nil)
		msg.StreamSeq = seq
		msg.AppendTime = md.Timestamp
		return msg, nil
	}
}

func (r *consumerRangeReader) RangeStats() RangeStats {
	if !r.failed {
		return RangeStats{Present: r.present, Absent: (r.to - r.from + 1) - r.present}
	}
	// Stopped early: only what lies at or before the last sequence returned, or below the
	// point the consumer started at, has been established.
	upto := r.start - 1
	if r.done > upto {
		upto = r.done
	}
	var absent uint64
	if upto >= r.from {
		absent = (upto - r.from + 1) - r.present
	}
	return RangeStats{Present: r.present, Absent: absent, Incomplete: true}
}

func (r *consumerRangeReader) Close() error {
	if r.sub != nil {
		return r.sub.Unsubscribe()
	}
	return nil
}
