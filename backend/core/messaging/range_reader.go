// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

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

	// rangeGetTimeout bounds one by-sequence request.
	rangeGetTimeout = 5 * time.Second

	// maxRangeRecreates bounds how often the consumer-backed read re-creates its consumer
	// after losing a delivery, so a broker that keeps dropping them fails the read loudly
	// instead of looping.
	maxRangeRecreates = 8
)

// RangeStats is what a range read found. It is complete once Read has returned io.EOF.
// Absent counts sequences in [from, to] the stream does not hold: removed by a purge, or
// evicted by retention. They are reported, never silently skipped, so a caller can tell a
// legitimate hole from a range it simply failed to read (which is an error, not an Absent).
type RangeStats struct {
	Present uint64
	Absent  uint64
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
// returned or counted absent.
//
// Ranges of at most 256 sequences are read one by one by sequence, creating no consumer.
// Wider ranges use one bounded ephemeral pull consumer, after a stream-info check that
// skips the part below the stream's first retained sequence (and creates no consumer at
// all when the whole range is below it). The consumer-backed read verifies that the
// consumer's delivery sequence advances by one per message, and re-creates the consumer
// after the last message received when it does not: a delivery the connection lost looks
// exactly like a purged sequence in the stream sequences, and only the delivery count tells
// them apart.
func (nmgr *NatsManager) NewRangeReader(suffix string, from, to uint64) (ReplayReader, error) {
	if from == 0 || from > to {
		return nil, fmt.Errorf("invalid range [%d, %d]: need 1 <= from <= to", from, to)
	}
	name := StreamName(nmgr.Microservice.InstanceId, suffix)
	span := to - from + 1
	if span <= rangeDirectMax {
		return &seqRangeReader{js: nmgr.js, stream: name, from: from, to: to, next: from}, nil
	}

	info, err := nmgr.js.StreamInfo(name)
	if err != nil {
		return nil, fmt.Errorf("range read of stream %s: %w", name, err)
	}
	lo := from
	if info.State.FirstSeq > lo {
		lo = info.State.FirstSeq
	}
	if lo > to {
		// Everything asked for is below the first retained sequence.
		return &seqRangeReader{js: nmgr.js, stream: name, from: from, to: to, next: to + 1, absent: span}, nil
	}
	r := &consumerRangeReader{
		js: nmgr.js, stream: name, subject: StreamSubject(nmgr.Microservice.InstanceId, suffix),
		from: from, to: to, start: lo,
	}
	if err := r.open(lo); err != nil {
		return nil, err
	}
	return r, nil
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
	js       nats.JetStreamContext
	stream   string
	from, to uint64
	next     uint64 // the next sequence to ask for
	present  uint64
	absent   uint64
}

func (r *seqRangeReader) Read(ctx context.Context) (Message, error) {
	for r.next <= r.to {
		if err := ctx.Err(); err != nil {
			return Message{}, err
		}
		cctx, cancel := context.WithTimeout(ctx, rangeGetTimeout)
		raw, err := r.js.GetMsg(r.stream, r.next, nats.Context(cctx))
		cancel()
		switch {
		case err == nil:
		case errors.Is(err, nats.ErrMsgNotFound):
			r.absent++
			r.next++
			continue
		default:
			return Message{}, fmt.Errorf("range read of stream %s seq %d (range %d..%d): %w",
				r.stream, r.next, r.from, r.to, err)
		}
		if raw.Sequence != r.next {
			return Message{}, fmt.Errorf("range read of stream %s: asked for seq %d, broker returned %d",
				r.stream, r.next, raw.Sequence)
		}
		r.next++
		r.present++
		return rawToMessage(raw), nil
	}
	return Message{}, io.EOF
}

func (r *seqRangeReader) RangeStats() RangeStats {
	return RangeStats{Present: r.present, Absent: r.absent}
}

func (r *seqRangeReader) Close() error { return nil }

// consumerRangeReader reads a wide range through one ephemeral pull consumer.
type consumerRangeReader struct {
	js       nats.JetStreamContext
	stream   string
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
}

func (r *consumerRangeReader) open(startSeq uint64) error {
	if r.sub != nil {
		_ = r.sub.Unsubscribe()
		r.sub = nil
	}
	sub, err := r.js.PullSubscribe(r.subject, "", // empty durable => ephemeral consumer
		nats.BindStream(r.stream),
		nats.StartSequence(startSeq),
		nats.AckExplicit(),
		nats.InactiveThreshold(replayInactiveThreshold))
	if err != nil {
		return fmt.Errorf("range read of stream %s: open consumer at %d: %w", r.stream, startSeq, err)
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
		return fmt.Errorf("range read of stream %s %d..%d: consumer lost deliveries %d times; giving up",
			r.stream, r.from, r.to, r.recreates)
	}
	startSeq := r.start
	if r.done >= startSeq {
		startSeq = r.done + 1
	}
	log.Warn().Str("stream", r.stream).Str("why", why).Uint64("from", startSeq).
		Msg("Range read lost a delivery; re-creating its consumer")
	return r.open(startSeq)
}

func (r *consumerRangeReader) Read(ctx context.Context) (Message, error) {
	for {
		if r.finished || r.done >= r.to {
			r.finished = true
			return Message{}, io.EOF
		}
		if err := ctx.Err(); err != nil {
			return Message{}, err
		}
		if len(r.pending) == 0 {
			msgs, err := r.sub.Fetch(fetchBatch, nats.MaxWait(fetchTimeout))
			if err != nil {
				if !errors.Is(err, nats.ErrTimeout) {
					return Message{}, fmt.Errorf("range read of stream %s: %w", r.stream, err)
				}
				info, ierr := r.sub.ConsumerInfo()
				if ierr != nil {
					return Message{}, fmt.Errorf("range read of stream %s: consumer info: %w", r.stream, ierr)
				}
				if info.Delivered.Consumer != r.lastDseq {
					// The broker sent something this client never received.
					if err := r.recreate("tail of a batch lost"); err != nil {
						return Message{}, err
					}
					continue
				}
				if info.NumPending == 0 {
					// Nothing more exists and nothing was lost: the rest of the range is absent.
					r.finished = true
					return Message{}, io.EOF
				}
				if r.timeouts++; r.timeouts > maxReplayStuckFetches {
					return Message{}, fmt.Errorf("range read stalled: stream %s has %d messages pending but delivered none in %d fetches",
						r.stream, info.NumPending, r.timeouts)
				}
				continue
			}
			r.timeouts = 0
			r.pending = msgs
		}
		nm := r.pending[0]
		md, err := nm.Metadata()
		if err != nil {
			return Message{}, fmt.Errorf("range read of stream %s: message without metadata: %w", r.stream, err)
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
		_ = nm.Ack() // advances the throwaway consumer
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
	span := r.to - r.from + 1
	return RangeStats{Present: r.present, Absent: span - r.present}
}

func (r *consumerRangeReader) Close() error {
	if r.sub != nil {
		return r.sub.Unsubscribe()
	}
	return nil
}
