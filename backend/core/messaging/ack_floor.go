// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"errors"
	"fmt"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/rs/zerolog/log"
)

// ackFloorStartTimeout bounds the start callback of a reader that is creating its durable.
const ackFloorStartTimeout = 30 * time.Second

// ErrAckFloorReader is what Message.Ack returns for a message read from an ack-floor reader
// (ReaderWithAckFloor). On such a durable a single ack acknowledges EVERY message at or below
// it, so a bare Ack would silently confirm messages the caller never handled. The only
// permitted acknowledgement is AckThrough, which names the ceiling explicitly.
var ErrAckFloorReader = errors.New("messaging: a message from an ack-floor reader cannot be acked on its own; " +
	"an ack acknowledges every message at or below it, use AckThrough")

// ErrNotAckFloorMessage is returned by AckThrough when a message did not come from an
// ack-floor reader. Acking it through the floor path would not mean what the caller thinks.
var ErrNotAckFloorMessage = errors.New("messaging: AckThrough was given a message that did not come from an ack-floor reader")

// ErrAckFloorPolicyMismatch is returned when a reader that asked for ReaderWithAckFloor finds
// its durable already exists with a different ack policy. The server refuses to change the
// ack policy of an existing consumer, and quietly falling back to explicit acks would turn
// AckThrough into something other than what was asked for, so the bind fails instead.
var ErrAckFloorPolicyMismatch = errors.New("messaging: durable exists with an ack policy other than AckAll")

// ReaderWithAckFloor makes the reader's durable an AckAll consumer positioned at a committed
// floor, for a consumer that checkpoints: it applies messages in order, periodically
// persists a position, and only then acknowledges everything at or below it with a single
// ack (AckThrough) instead of one ack per message.
//
// start returns the first stream sequence the durable should deliver (the committed floor
// plus one). It is called only when the durable has to be CREATED: the first bind, and a
// self-heal after the durable was deleted. A durable that already exists is bound as it is,
// at wherever its own ack floor stands.
//
// The server refuses to change an existing consumer's ack policy or start sequence
// (CreateConsumer reports "consumer already exists"; UpdateConsumer reports "ack policy can
// not be updated" / "start sequence can not be updated"). So this bind path never calls
// AddConsumer on an existing durable, and when the filter subject has moved it updates the
// existing configuration with only the filter changed. If the existing durable's ack policy
// is not AckAll the bind fails with ErrAckFloorPolicyMismatch; use a new durable name to
// move a reader onto this option.
//
// Messages from such a reader refuse Message.Ack (ErrAckFloorReader). It cannot be combined
// with ReaderWithDeliverNew or ReaderWithCapacity; NewReader rejects the combination.
func ReaderWithAckFloor(start func(ctx context.Context) (uint64, error)) ReaderOption {
	return func(r *natsReader) { r.ackFloorStart = start }
}

// ackFloor reports whether this reader was built with ReaderWithAckFloor.
func (r *natsReader) ackFloor() bool { return r.ackFloorStart != nil }

// validateAckFloor rejects option combinations that cannot mean what they say.
func (r *natsReader) validateAckFloor() error {
	if !r.ackFloor() {
		return nil
	}
	if r.deliverNew {
		return errors.New("messaging: ReaderWithAckFloor cannot be combined with ReaderWithDeliverNew; " +
			"the start position comes from the floor callback")
	}
	if r.slots > 0 {
		return errors.New("messaging: ReaderWithAckFloor cannot be combined with ReaderWithCapacity; " +
			"capacity slots are released per message ack")
	}
	return nil
}

// floorAck is the Acknowledger of a message from an ack-floor reader. Ack refuses; the
// transport handle is reachable only through AckThrough.
type floorAck struct{ nm *nats.Msg }

func (floorAck) Ack() error { return ErrAckFloorReader }

// ackFloorConsumerConfig is the configuration an ack-floor durable is CREATED with.
func (r *natsReader) ackFloorConsumerConfig(startSeq uint64) *nats.ConsumerConfig {
	cfg := r.consumerConfig()
	cfg.AckPolicy = nats.AckAllPolicy
	cfg.DeliverPolicy = nats.DeliverByStartSequencePolicy
	cfg.OptStartSeq = startSeq
	return cfg
}

// bindAckFloorLocked ensures the ack-floor durable exists and reconciles nothing but its
// filter, without calling AddConsumer on an existing durable. bindMu is held.
func (r *natsReader) bindAckFloorLocked() error {
	js := r.nmgr.js
	info, err := js.ConsumerInfo(r.stream, r.durable)
	if err != nil {
		if !errors.Is(err, nats.ErrConsumerNotFound) {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), ackFloorStartTimeout)
		startSeq, serr := r.ackFloorStart(ctx)
		cancel()
		if serr != nil {
			return fmt.Errorf("messaging: ack-floor start position for durable %q: %w", r.durable, serr)
		}
		if startSeq == 0 {
			return fmt.Errorf("messaging: ack-floor start position for durable %q is sequence 0; "+
				"stream sequences begin at 1", r.durable)
		}
		if _, aerr := js.AddConsumer(r.stream, r.ackFloorConsumerConfig(startSeq)); aerr != nil {
			// Another replica may have created it between our look and our create, with its
			// own start position. Anything that exists now is bound as it is.
			if _, ierr := js.ConsumerInfo(r.stream, r.durable); ierr != nil {
				return aerr
			}
		}
		if info, err = js.ConsumerInfo(r.stream, r.durable); err != nil {
			return err
		}
	}
	if info.Config.AckPolicy != nats.AckAllPolicy {
		return fmt.Errorf("%w: durable %q on stream %q has ack policy %s", ErrAckFloorPolicyMismatch,
			r.durable, r.stream, info.Config.AckPolicy)
	}
	if err := r.reconcileAckFloorFilter(info); err != nil {
		return err
	}
	sub, err := js.PullSubscribe(r.subject, r.durable, nats.Bind(r.stream, r.durable))
	if err != nil {
		return err
	}
	r.sub.Store(sub)
	r.answered.Store(true)
	return nil
}

// reconcileAckFloorFilter moves an existing ack-floor durable onto this build's filter,
// carrying its existing configuration (ack policy and start fields included) unchanged.
func (r *natsReader) reconcileAckFloorFilter(info *nats.ConsumerInfo) error {
	want := r.consumerConfig()
	if info.Config.FilterSubject == want.FilterSubject && sameStrings(info.Config.FilterSubjects, want.FilterSubjects) {
		return nil
	}
	cfg := info.Config
	cfg.FilterSubject = want.FilterSubject
	cfg.FilterSubjects = want.FilterSubjects
	log.Info().Str("stream", r.stream).Str("durable", r.durable).
		Str("from", info.Config.FilterSubject).Str("to", want.FilterSubject).
		Msg("Moving an existing ack-floor durable onto this build's filter subject")
	if _, err := r.nmgr.js.UpdateConsumer(r.stream, &cfg); err != nil {
		return fmt.Errorf("moving ack-floor durable %q onto filter %q: %w", r.durable, want.FilterSubject, err)
	}
	return nil
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := make(map[string]int, len(a))
	for _, s := range a {
		m[s]++
	}
	for _, s := range b {
		if m[s]--; m[s] < 0 {
			return false
		}
	}
	return true
}

// AckThroughResult says what AckThrough did.
type AckThroughResult struct {
	// AckedSeq is the stream sequence of the message that was acked, which acknowledged
	// every message at or below it. Zero when nothing was acked.
	AckedSeq uint64
	// Above counts messages with a sequence above the ceiling; they are left unacked.
	Above int
	// Unsequenced counts messages with no stream sequence (0); they are never acked, and
	// the next floor that passes them covers them.
	Unsequenced int
}

// AckThrough acknowledges, with a single ack, every message the durable has delivered at or
// below ceiling: it acks the message in msgs with the HIGHEST StreamSeq not above ceiling
// (not the last one in the slice, which may be a redelivery of a lower sequence). It does
// nothing, and sends nothing, when ceiling is 0 or no message qualifies; a message with
// StreamSeq 0 (unreadable metadata) is never the one acked, because 0 would name no floor.
//
// msgs must all come from ack-floor readers; if any does not, nothing is acked and
// ErrNotAckFloorMessage is returned.
func AckThrough(msgs []Message, ceiling uint64) (AckThroughResult, error) {
	var res AckThroughResult
	var best *nats.Msg
	for i := range msgs {
		fa, ok := msgs[i].ack.(floorAck)
		if !ok {
			return AckThroughResult{}, ErrNotAckFloorMessage
		}
		switch seq := msgs[i].StreamSeq; {
		case seq == 0:
			res.Unsequenced++
		case seq > ceiling:
			res.Above++
		case seq > res.AckedSeq:
			res.AckedSeq, best = seq, fa.nm
		}
	}
	if best == nil {
		return AckThroughResult{Above: res.Above, Unsequenced: res.Unsequenced}, nil
	}
	if err := best.Ack(); err != nil {
		return AckThroughResult{Above: res.Above, Unsequenced: res.Unsequenced},
			fmt.Errorf("messaging: acking through sequence %d: %w", res.AckedSeq, err)
	}
	return res, nil
}
