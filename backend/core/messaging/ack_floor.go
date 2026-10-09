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
// A variable only so a test can shorten it; nothing else assigns it.
var ackFloorStartTimeout = 30 * time.Second

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
// start must be a fast local read. It runs with the reader's bind lock held, under a 30 s
// context, and UnbindTerm (a lost leadership term) takes the same lock, so a slow start
// delays term teardown by as long as it takes.
//
// Messages handed out before a re-bind that recreated the durable belong to the dead
// consumer. Their ack reply subjects name the consumer by name, so the server would apply
// them to the new one, and AckThrough therefore skips them (AckThroughResult.Stale).
//
// The server refuses to change an existing consumer's ack policy or start sequence
// (CreateConsumer reports "consumer already exists"; UpdateConsumer reports "ack policy can
// not be updated" / "start sequence can not be updated"). So this bind path never calls
// AddConsumer on an existing durable, and when the filter subject has moved it updates the
// existing configuration with only the filter changed. If the existing durable's ack policy
// is not AckAll the bind fails with ErrAckFloorPolicyMismatch; use a new durable name to
// move a reader onto this option.
//
// The durable is named AckFloorDurableName, not DurableName: see there. The ordinary-named
// durable the reader replaced is deleted at the start of each leadership term (BindTerm).
//
// Messages from such a reader refuse Message.Ack (ErrAckFloorReader). It cannot be combined
// with ReaderWithDeliverNew or ReaderWithCapacity; NewReader rejects the combination.
func ReaderWithAckFloor(start func(ctx context.Context) (uint64, error)) ReaderOption {
	return func(r *natsReader) { r.ackFloorStart = start }
}

// AckFloorRecreator is implemented by an ack-floor reader that can have its durable recreated at
// the start callback's current position. A consumer whose committed position moved BACKWARD
// (the stream was recreated under a snapshot that is ahead of it) needs this: the durable was
// created at the old position plus one, past everything the new stream holds.
type AckFloorRecreator interface {
	// RecreateAtFloor deletes the durable and creates it again at the start callback's current
	// answer, then binds to it. Anything handed out before it belongs to the dead consumer and
	// is skipped by AckThrough.
	RecreateAtFloor() error
}

// RecreateAtFloor implements AckFloorRecreator.
func (r *natsReader) RecreateAtFloor() error {
	if !r.ackFloor() {
		return errors.New("messaging: RecreateAtFloor on a reader that was not built with ReaderWithAckFloor")
	}
	r.bindMu.Lock()
	defer r.bindMu.Unlock()
	if !r.reading.CompareAndSwap(false, true) {
		return fmt.Errorf("%w: RecreateAtFloor on durable %q while a read is in flight", ErrConcurrentRead, r.durable)
	}
	r.dropPending()
	r.reading.Store(false)
	if err := r.nmgr.js.DeleteConsumer(r.stream, r.durable); err != nil && !errors.Is(err, nats.ErrConsumerNotFound) {
		return fmt.Errorf("messaging: deleting ack-floor durable %q to recreate it: %w", r.durable, err)
	}
	return r.bindLocked()
}

// AckFloorDurableName is the durable an ack-floor reader of suffix uses: the ordinary name
// (DurableName) with an "_ackfloor" tail. The tail is the point, not decoration. A durable's
// ack policy cannot be changed in place, so a reader moving to the floor policy cannot keep
// its old name: it takes a new one, created at its committed position, and retires the old
// one (see retireLegacyDurable). And a name that says which policy a durable has is one a
// reader of the other policy can never collide with. Dashboards and alert selectors match on
// the name, so anything that names a durable by its ordinary form must know this one too.
func AckFloorDurableName(instanceId, functionalArea, suffix string) string {
	return DurableName(instanceId, functionalArea, suffix) + ackFloorDurableTail
}

// ackFloorDurableTail ends the name of every ack-floor durable; see AckFloorDurableName.
const ackFloorDurableTail = "_ackfloor"

// retireLegacyDurable deletes the explicit-ack durable this reader replaced, if it is still
// there. It is idempotent, and a durable that cannot be deleted is logged and left: it is
// harmless (resolved-events retains by limits, so an orphaned consumer pins nothing, and
// nothing samples or pulls from it), and the next term tries again.
//
// It runs at term start, not at construction, so that only the replica that leads deletes
// anything. A replica running the previous build (which binds the legacy name with
// AddConsumer on every re-bind) would otherwise recreate it behind a deleter that ran at
// every process start.
func (r *natsReader) retireLegacyDurable() {
	if !r.ackFloor() {
		return
	}
	legacy := DurableName(r.nmgr.Microservice.InstanceId, r.nmgr.Microservice.FunctionalArea, r.suffix)
	if legacy == r.durable {
		return
	}
	err := r.nmgr.js.DeleteConsumer(r.stream, legacy)
	switch {
	case err == nil:
		log.Info().Str("stream", r.stream).Str("durable", legacy).Str("replacedBy", r.durable).
			Msg("Deleted the explicit-ack durable this ack-floor reader replaced")
	case errors.Is(err, nats.ErrConsumerNotFound):
	default:
		log.Warn().Err(err).Str("stream", r.stream).Str("durable", legacy).
			Msg("Could not delete the explicit-ack durable this ack-floor reader replaced; it is unused and harmless, and the next term retries")
	}
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
	if gatesItsStream(r.suffix, r.nmgr.Microservice.FunctionalArea) {
		// The stream's writers measure this area's backlog on the durable's ORDINARY name
		// (gatingDurables). An ack-floor durable has another, so the gate would read a
		// durable that does not exist and never close.
		return fmt.Errorf("messaging: ReaderWithAckFloor cannot be used by %q on %q: its backlog gates the stream's writers, "+
			"and the gate reads the ordinary durable name", r.nmgr.Microservice.FunctionalArea, r.suffix)
	}
	return nil
}

// FloorAcknowledger is what AckThrough needs of a message's Acknowledger. The transport's
// is the only production implementation; the interface exists so a consumer's unit tests can
// stand a fake in for the broker and still go through AckThrough (the code under test) rather
// than around it.
type FloorAcknowledger interface {
	Acknowledger
	// AckFloor acknowledges this message and, with it, every message at or below its stream
	// sequence. It is the only acknowledgement an ack-floor message permits.
	AckFloor() error
	// Current reports whether the message was delivered by the reader's current bind. One
	// delivered before the durable was recreated names a consumer that no longer exists, so
	// acking it would apply its delivery sequence to the new one.
	Current() bool
}

// floorAck is the Acknowledger of a message from an ack-floor reader. Ack refuses; the
// transport handle is reachable only through AckFloor.
type floorAck struct {
	nm *nats.Msg
	// r and gen identify the bind that delivered the message; nil r means no check (tests).
	r   *natsReader
	gen uint64
}

func (floorAck) Ack() error { return ErrAckFloorReader }

func (a floorAck) AckFloor() error { return a.nm.Ack() }

// Current reports whether the message was delivered by the reader's current bind.
func (a floorAck) Current() bool { return a.r == nil || a.r.ackGen.Load() == a.gen }

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
	// A new generation: anything delivered by an earlier bind is stale from here on.
	r.ackGen.Add(1)
	r.sub.Store(sub)
	r.answered.Store(true)
	return nil
}

// reconcileAckFloorFilter brings an existing ack-floor durable's server-updatable fields
// (filter, AckWait, MaxDeliver, MaxAckPending) onto this build's values, carrying the rest
// of its configuration (ack policy and start fields included) unchanged.
func (r *natsReader) reconcileAckFloorFilter(info *nats.ConsumerInfo) error {
	want := r.consumerConfig()
	if info.Config.FilterSubject == want.FilterSubject && sameStrings(info.Config.FilterSubjects, want.FilterSubjects) &&
		info.Config.AckWait == want.AckWait && info.Config.MaxDeliver == want.MaxDeliver &&
		info.Config.MaxAckPending == want.MaxAckPending {
		return nil
	}
	cfg := info.Config
	cfg.FilterSubject = want.FilterSubject
	cfg.FilterSubjects = want.FilterSubjects
	cfg.AckWait = want.AckWait
	cfg.MaxDeliver = want.MaxDeliver
	cfg.MaxAckPending = want.MaxAckPending
	log.Info().Str("stream", r.stream).Str("durable", r.durable).
		Str("from", info.Config.FilterSubject).Str("to", want.FilterSubject).
		Msg("Reconciling an existing ack-floor durable with this build's configuration")
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
	// SentSeq is the stream sequence of the message an ack was SENT for, which the server
	// applies to every message at or below it. Zero when nothing was sent. The ack is
	// fire-and-forget: SentSeq says it left this process, not that the broker applied it.
	// A caller that needs to know can poll the durable's ConsumerInfo until
	// AckFloor.Stream >= SentSeq.
	SentSeq uint64
	// Stale counts messages delivered by an earlier bind of the reader (before the durable
	// was recreated); they are never acked, because the server would apply their delivery
	// sequence to the new consumer.
	Stale int
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
	var best FloorAcknowledger
	for i := range msgs {
		fa, ok := msgs[i].ack.(FloorAcknowledger)
		if !ok {
			return AckThroughResult{}, ErrNotAckFloorMessage
		}
		switch seq := msgs[i].StreamSeq; {
		case !fa.Current():
			res.Stale++
		case seq == 0:
			res.Unsequenced++
		case seq > ceiling:
			res.Above++
		case seq > res.SentSeq:
			res.SentSeq, best = seq, fa
		}
	}
	if best == nil {
		return AckThroughResult{Above: res.Above, Unsequenced: res.Unsequenced, Stale: res.Stale}, nil
	}
	if err := best.AckFloor(); err != nil {
		return AckThroughResult{Above: res.Above, Unsequenced: res.Unsequenced, Stale: res.Stale},
			fmt.Errorf("messaging: acking through sequence %d: %w", res.SentSeq, err)
	}
	return res, nil
}
