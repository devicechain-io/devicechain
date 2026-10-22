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
)

const (
	// rangeDirectMax is the widest range read with one request/reply per sequence. Above it
	// the range is read in batched direct gets (sequence_reader.go), which are cheaper than
	// that many round trips. Neither creates anything on the broker.
	rangeDirectMax = 256

	// rangeGetTimeout bounds one by-sequence request.
	rangeGetTimeout = 5 * time.Second
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
// "not found" are taken as the stream's.
//
// Ranges of at most 256 sequences are read one by one by sequence. Wider ranges are read in
// batched direct gets (sequenceReader), which need the stream to allow direct gets. Neither creates
// anything on the broker, so the grant to read a range is a read of one stream and the reader
// needs no consumer name. Both account for every sequence in the range: a hole the answer leaves is
// an absence only if the stream leader says so, and a reader that is shown an end it cannot
// confirm retries and then fails rather than ending short.
func (nmgr *NatsManager) NewRangeReader(suffix string, from, to uint64) (ReplayReader, error) {
	if err := nmgr.requireReader(suffix, streams.ReadEphemeral); err != nil {
		return nil, err
	}
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
	src := &rangeSource{js: nmgr.js, stream: name, firstSeq: info.State.FirstSeq, lastSeq: info.State.LastSeq}
	if !wide {
		return &seqRangeReader{src: src, from: from, to: to, next: from}, nil
	}

	return &sequenceReader{src: src, nc: nmgr.nc, from: from, to: to, next: from}, nil
}

// rangeSource is the by-sequence access to the stream leader both readers share.
type rangeSource struct {
	js     nats.JetStreamContext
	stream string
	// firstSeq is the stream's first retained sequence when the read was opened. It only
	// ever rises, so a sequence below it is certainly gone.
	firstSeq uint64
	// lastSeq and deleted are the leader's state as of the last time it was asked (see
	// leaderConfirmsAbsent); deleted is the set of purged sequences between the two.
	lastSeq uint64
	deleted map[uint64]struct{}
	// noDirect is set when the leader's configuration (as of the last refresh) says the stream
	// does not answer direct gets. A state read without a configuration (a test's) leaves it unset.
	noDirect bool
	// get and info are the requests themselves; nil means the real ones. A test replaces them.
	get  func(ctx context.Context, seq uint64) (*nats.RawStreamMsg, error)
	info func(ctx context.Context) (*nats.StreamInfo, error)
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

// getOrAbsent returns the message at seq, or (nil, nil) when the stream does not hold it.
//
// A "no message found" answer is NOT taken as an absence by itself: the server gives that
// answer for every failed read of a sequence it holds (a block that cannot be read or
// decompressed, a store being reset), and asking again gets the same answer. It counts as an
// absence only when the stream leader says so: the sequence is below its first retained
// sequence (evicted), or is in the deleted set its state reports (purged). Anything else is
// an error, so a stream that holds an event it cannot read fails the read instead of
// having the event skipped.
func (s *rangeSource) getOrAbsent(ctx context.Context, seq uint64) (*nats.RawStreamMsg, error) {
	raw, err := s.rawGet(ctx, seq)
	switch {
	case err == nil:
		return raw, nil
	case !errors.Is(err, nats.ErrMsgNotFound):
		return nil, fmt.Errorf("range read of stream %s seq %d: %w", s.stream, seq, err)
	case seq < s.firstSeq:
		return nil, nil // evicted before this read began
	}
	absent, err := s.leaderConfirmsAbsent(ctx, seq)
	if err != nil {
		return nil, err
	}
	if !absent {
		return nil, fmt.Errorf("range read of stream %s: seq %d was reported not found, but the stream leader does not list it as deleted or evicted (first %d, last %d); refusing to count it absent",
			s.stream, seq, s.firstSeq, s.lastSeq)
	}
	return nil, nil
}

// leaderConfirmsAbsent reports whether the stream leader's own state accounts for seq not
// being readable. The deleted set from one state request answers every absence in a range;
// it is asked for again only when a sequence is not in the set already held, which covers a
// purge or an eviction that happened after the last ask.
func (s *rangeSource) leaderConfirmsAbsent(ctx context.Context, seq uint64) (bool, error) {
	if _, ok := s.deleted[seq]; ok {
		return true, nil
	}
	if err := s.refresh(ctx); err != nil {
		return false, fmt.Errorf("range read of stream %s: confirming seq %d with the stream leader: %w", s.stream, seq, err)
	}
	return s.knownAbsent(seq), nil
}

// refresh asks the stream leader for its state (first and last sequence and the deleted set) so
// that knownAbsent answers from it.
func (s *rangeSource) refresh(ctx context.Context) error {
	cctx, cancel := context.WithTimeout(ctx, rangeGetTimeout)
	defer cancel()
	var info *nats.StreamInfo
	var err error
	if s.info != nil {
		info, err = s.info(cctx)
	} else {
		info, err = s.js.StreamInfo(s.stream, &nats.StreamInfoRequest{DeletedDetails: true}, nats.Context(cctx))
	}
	if err != nil {
		return fmt.Errorf("range read of stream %s: asking the stream leader for its state: %w", s.stream, err)
	}
	if info.State.FirstSeq > s.firstSeq {
		s.firstSeq = info.State.FirstSeq
	}
	s.lastSeq = info.State.LastSeq
	s.noDirect = info.Config.Name != "" && !info.Config.AllowDirect
	s.deleted = make(map[uint64]struct{}, len(info.State.Deleted))
	for _, d := range info.State.Deleted {
		s.deleted[d] = struct{}{}
	}
	return nil
}

// allKnownAbsent reports whether the leader state last read accounts for every sequence in
// [lo, hi].
func (s *rangeSource) allKnownAbsent(lo, hi uint64) bool {
	for seq := lo; seq <= hi; seq++ {
		if !s.knownAbsent(seq) {
			return false
		}
	}
	return true
}

// knownAbsent reports whether the leader state last read accounts for seq not being held: below
// the first retained sequence, or in the deleted set.
func (s *rangeSource) knownAbsent(seq uint64) bool {
	if seq < s.firstSeq {
		return true
	}
	_, ok := s.deleted[seq]
	return ok
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
