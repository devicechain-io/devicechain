// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package deadletter is the platform's one shape for "a consumer gave up on this"
// (ADR-024).
//
// # Why one stream and one envelope rather than a dead-letter twin per stream
//
// The convention already in the tree is `<base>.dead` — outbound-connectors publishes
// to `connector-dispatch.dead`, built by streams.DeadLetter. Following it for the three
// consumers that lacked an arm would have meant three more streams, and 🔴 a stream is
// not free here: every ceiling in the stream budget is RESERVED UP FRONT, so three more
// cold streams is three more MaxBytes the JetStream volume has to be sized for on every
// deployment, forever, to hold something that is empty in steady state.
//
// One stream costs one reservation. What it gives up is the ability to tell producers
// apart by subject, and that is bought back for nothing by putting the source in the
// envelope — which the read side wanted anyway, because an operator asking "what has the
// platform given up on" wants one list, not four.
//
// # What it is NOT
//
// It is not a retry queue. Nothing in the platform re-injects a dead letter today, and
// the published documentation has been corrected to stop implying otherwise: operator-
// driven replay carries a re-poisoning risk that deserves its own review, and shipping
// the word before the mechanism is how a promise becomes a claim.
//
// It is not for every failure either. A message dropped because it is unattributable —
// no parseable tenant, a forged tenant, an undecodable body — is NOT dead-lettered. Those
// are dropped where they are found, because writing them onto a tenant's dead-letter
// subject would be attributing to that tenant something that was never demonstrably
// theirs. A dead letter is work the platform ACCEPTED and then could not finish.
package deadletter

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/devicechain-io/dc-microservice/messaging"
)

// Kind names what the platform gave up on, from the point of view of someone reading a
// list of them. It is a closed vocabulary because it is a metric label and a query
// filter, and both are cardinality-bounded surfaces.
type Kind string

const (
	// KindDetectionAction is one detection's authored REACT actions — raise-alarm,
	// send-command, or a connector dispatch — that could not be dispatched.
	KindDetectionAction Kind = "detection-action"
	// KindNotification is an alarm that could not be delivered to a tenant's people
	// over any configured channel.
	KindNotification Kind = "notification"
	// KindCommandResponse is a device's reply to a command that could not be recorded
	// against that command.
	KindCommandResponse Kind = "command-response"
	// KindConnectorDispatch is an outbound send — a webhook call or a broker publish to
	// a tenant's registered connector — that the connectors service gave up on.
	//
	// 🔑 IT IS THE ONE KIND WHOSE ORIGINAL LIVES ELSEWHERE. That service keeps its own
	// terminal sink (connector-dispatch.dead) holding the request VERBATIM, because a
	// replay of an outbound send has to be byte-identical and an envelope is a summary.
	// This letter is the INDEX entry for it: it puts the give-up in the one list an
	// operator reads. So it carries no Payload — the body is already durable one stream
	// over, and storing it twice would double the retention cost of the platform's
	// noisiest error path to say the same thing.
	//
	// 🔴 SUBJECT AND SEQUENCE LOCATE THE ORIGINAL ON THE SOURCE STREAM, NOT THE COPY ON
	// THE DEAD SUBJECT, and reading them the other way sends an operator to a sequence
	// holding a different message. The copy's own sequence is not knowable to record:
	// messaging.WriteMessages returns an error and no PubAck, so nothing that writes a
	// dead letter ever learns where it landed. What joins the two is CORRELATION, which
	// the verbatim copy carries through the writer — so the pointer is "this dispatch, at
	// this position on connector-dispatch", and the copy is found by scanning the dead
	// subject for that correlation id. Both streams share the platform's 7-day age
	// limit, so the pointer is usually still good — but connector-dispatch is a HOT
	// stream carrying every dispatch and the dead sink is a COLD one carrying only the
	// give-ups, so on a busy instance the original can be discarded on the byte ceiling
	// first. That is why the verbatim copy, not the pointer, is the durable record.
	KindConnectorDispatch Kind = "connector-dispatch"
)

// allKinds is the vocabulary in one place, so a reader that has to OFFER the set — the
// dcctl `--kind` filter's help, and anything else that lists it — derives it instead of
// typing it out again.
//
// 🔴 IT IS DECLARED BESIDE THE CONSTANTS FOR THE REASON EVERY LIST-OF-A-LIST IN THIS TREE
// IS. dcctl's help already carried a hand-written copy of three of these, and a
// hand-written copy of a closed set is wrong the first time the set grows: an operator is
// then told a filter value does not exist, on the one surface whose job is to help them
// find what failed. Adding a Kind above without adding it here leaves it unlistable, which
// is a visible gap rather than a silent one — but keep them together anyway.
var allKinds = []Kind{
	KindDetectionAction, KindNotification, KindCommandResponse, KindConnectorDispatch,
}

// Kinds returns the Kind vocabulary as strings, in declaration order.
func Kinds() []string {
	out := make([]string, 0, len(allKinds))
	for _, k := range allKinds {
		out = append(out, string(k))
	}
	return out
}

// Valid reports whether k is one of the declared kinds.
//
// 🔑 MEMBERSHIP OF allKinds, NOT A THIRD HAND-COPY OF THE SAME FOUR NAMES. It is positive
// in the sense that matters — an unlisted value is false, never "assumed fine" — while
// leaving the vocabulary written down once for both the readers that OFFER it and the
// writers that are checked against it. A switch here would have been a second list to keep
// in step with the first, and a list-of-a-list is wrong the first time the set grows.
//
// Why it is checked at all: Kind is closed because it is a metric label and a query filter,
// and a value outside it is not a harmless typo. It marshals, it lands in an indexed
// column, and it is then invisible to every reader that offers the vocabulary — dcctl's
// `--kind` help among them. A record of a failure that the one surface built to find
// failures cannot show is worse than no record, because the operator sees a
// complete-looking list.
func (k Kind) Valid() bool {
	for _, declared := range allKinds {
		if k == declared {
			return true
		}
	}
	return false
}

// Reason is why it was given up on, bounded for the same reasons Kind is.
type Reason string

const (
	// ReasonExhausted means the work was retried to the redelivery cap and never
	// succeeded. It is the ordinary reason and says nothing about the cause.
	ReasonExhausted Reason = "exhausted"
	// ReasonUnprocessable means the work was accepted and then found to be something
	// this consumer can never complete, so retrying it would only burn the cap.
	ReasonUnprocessable Reason = "unprocessable"
	// ReasonShed means the work was refused on a GOVERNED CEILING rather than failing —
	// the tenant was over its rate and stayed over it for longer than the smoothing
	// budget allows.
	//
	// 🔑 IT IS ITS OWN REASON BECAUSE THE REMEDY IS DIFFERENT AND THE WORK IS NOT BROKEN.
	// Folded into ReasonExhausted it would read as "we tried and failed", sending an
	// operator to look at a destination that is perfectly healthy; folded into
	// ReasonUnprocessable it would read as poison, when it is the one reason here whose
	// letter describes work that would succeed if it were sent again.
	ReasonShed Reason = "shed"
)

// Valid reports whether r is one of the declared reasons. Bounded for the same reasons
// Kind.Valid is, and written the same way: a positive switch, so a value nobody declared
// is refused on the write path rather than becoming an unfilterable row.
func (r Reason) Valid() bool {
	switch r {
	case ReasonExhausted, ReasonUnprocessable, ReasonShed:
		return true
	default:
		return false
	}
}

// WorkWasAttempted reports whether the giving-up service actually ATTEMPTED this work and
// lost it, as opposed to declining it before any attempt.
//
// 🔴 IT IS A VETO, NOT A LICENCE, AND THE ASYMMETRY IS DELIBERATE. False means DO NOT SETTLE:
// the producer declined before attempting, so a consumer that marks work lost on the strength
// of the letter would be recording a loss that did not happen. True does not mean "settle" —
// it means the letter is not disqualified on this axis, and the consumer still owns the
// question of what its own Kind's letters imply.
//
// Reading Kind alone is what this exists to stop. Kind says what the work WAS; it does not
// say whether anything was lost, and treating it as though it did is how a letter recording
// a DECLINED write becomes the platform performing that write. That happened: a device's
// answer to a command the platform had returned to the queue was dead-lettered as
// unprocessable, and a consumer filtering on Kind alone stamped FAILED on a row the sweep had
// meanwhile re-dispatched — so the device's real answer to the new dispatch arrived on a
// terminal row and was discarded as late.
//
// 🔑 A KIND WHOSE DECLINED LETTERS ARE THEMSELVES GENUINE LOSSES NEEDS ITS OWN ANSWER, NOT A
// WIDER TRUE HERE. KindNotification is the worked example: an unprocessable notification would
// mean the alarm reached nobody and never will, which is exactly the letter such a consumer
// SHOULD act on — and this method would answer false for it, correctly, because the send was
// never attempted. The two questions are different, and the fix is a Kind-specific rule at that
// consumer rather than promoting a reason here, which would silently re-arm every other
// consumer. No producer emits that letter today; the next author should know the shape anyway.
//
// 🔑 A POSITIVE LIST: an explicit case per reason that returns true, and a default of
// false. A new Reason is a new way of giving up, and whether it settles anything has to be
// answered deliberately for it. Written as a skip — "everything except the ones I know
// about" — a reason nobody classified here would settle state by default, which is
// precisely the direction the defect above came from. This way an unclassified reason is
// an inert no-op at every consumer until someone decides otherwise, which is the
// recoverable direction to be wrong in.
func (r Reason) WorkWasAttempted() bool {
	switch r {
	case ReasonExhausted:
		// The work was retried to the redelivery cap and never landed, so what it
		// describes is genuinely gone and downstream state must stop reading as in flight.
		return true
	default:
		// ReasonUnprocessable and ReasonShed both describe work the producer looked at and
		// declined — nothing was attempted, so nothing is lost — and so does any reason a
		// later build adds without revisiting this.
		return false
	}
}

// Envelope is one dead letter. It is JSON rather than protobuf, unlike the failed-event
// envelope it sits beside, because it is written once by a handful of call sites and
// read by a store and a human — and the fields a reader needs most (which rule, which
// alarm) differ per Kind, which a schema-per-kind proto would multiply and a map does not.
type Envelope struct {
	// Kind and Reason are the two bounded axes. Together they answer "what, and why"
	// without anybody having to read Detail.
	Kind   Kind   `json:"kind"`
	Reason Reason `json:"reason"`

	// Source is the functional area that gave up. It is recorded rather than inferred
	// from Kind because the two can diverge — a kind can move service, and a dead letter
	// outlives the deploy that wrote it.
	Source string `json:"source"`

	// Summary is a fixed, non-interpolated sentence describing the failure class. It is
	// safe to show anywhere.
	Summary string `json:"summary"`

	// Detail is the underlying error, and 🔴 IT IS NOT ALWAYS SAFE TO SHOW. An egress
	// failure's error text can carry the status and address of a destination the caller
	// chose, which on a tenant-readable surface is an oracle. The read side decides what
	// to render; this field records what happened. Empty when there is nothing to add.
	Detail string `json:"detail,omitempty"`

	// Attempts is how many times delivery was attempted before giving up. Zero for a
	// letter written on the first look at a thing that could never succeed.
	Attempts int `json:"attempts"`

	// Subject and Sequence locate the original message on its own stream, which is the
	// only way to find it again while the stream still holds it.
	Subject  string `json:"subject,omitempty"`
	Sequence uint64 `json:"sequence,omitempty"`

	// Correlation carries the inbound correlation id through, so a dead letter can be
	// joined to the logs of the request that produced it.
	Correlation string `json:"correlation,omitempty"`

	// Reference is whatever identifies the failed work WITHIN its kind — a rule id, an
	// alarm token, a command token. One string, because a reader scanning a list wants
	// one column, and a kind that needs two can join them.
	Reference string `json:"reference,omitempty"`

	// OccurredAt is when the platform gave up, from the giving-up service's clock.
	OccurredAt time.Time `json:"occurredAt"`

	// Payload is the original message body, kept so the work can be understood — and,
	// one day, replayed. It is opaque here on purpose: this package must not need to
	// know how to decode every kind it carries.
	Payload []byte `json:"payload,omitempty"`
}

// Validate rejects an envelope that could not be read back usefully.
//
// 🔑 IT IS CALLED ON THE WRITE PATH, NOT THE READ ONE, and the difference matters. A dead
// letter is written at the moment something has already gone wrong, by a caller that is
// about to ack and forget — so a field left empty here is a record nobody can act on,
// discovered weeks later by the person the record existed for.
//
// 🔴 AN OFF-VOCABULARY Kind OR Reason IS REFUSED HERE TOO, NOT ONLY AN EMPTY ONE. Both are
// documented as closed sets because they are metric labels and query filters, but a
// non-empty check enforces neither: Kind("conector-dispatch") compiles, validates,
// marshals onto the stream and lands in an indexed column, and is then invisible to every
// reader that offers the declared set — dcctl's `--kind` filter among them. That is a
// worse outcome than refusing the write, because the operator is shown a list that looks
// complete. A misclassified Reason is worse still: Reason.WorkWasAttempted answers false
// for anything it does not recognise, so a typo'd reason silently makes a genuine loss
// non-actionable to every consumer that settles state on one.
func (e Envelope) Validate() error {
	missing := []string{}
	unknown := []string{}
	if e.Kind == "" {
		missing = append(missing, "kind")
	} else if !e.Kind.Valid() {
		unknown = append(unknown, fmt.Sprintf("kind %q", string(e.Kind)))
	}
	if e.Reason == "" {
		missing = append(missing, "reason")
	} else if !e.Reason.Valid() {
		unknown = append(unknown, fmt.Sprintf("reason %q", string(e.Reason)))
	}
	if strings.TrimSpace(e.Source) == "" {
		missing = append(missing, "source")
	}
	if strings.TrimSpace(e.Summary) == "" {
		missing = append(missing, "summary")
	}
	if e.OccurredAt.IsZero() {
		missing = append(missing, "occurredAt")
	}
	if len(missing) > 0 {
		return fmt.Errorf("refusing to write a dead letter with no %s: it would be a record of "+
			"a failure that nobody reading it could act on", strings.Join(missing, ", "))
	}
	if len(unknown) > 0 {
		return fmt.Errorf("refusing to write a dead letter with an undeclared %s: it would be a "+
			"record of a failure that no reader filtering on the declared vocabulary could find",
			strings.Join(unknown, " and "))
	}
	return nil
}

// Marshal renders an envelope for the wire, refusing an unusable one.
func Marshal(e Envelope) ([]byte, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(e)
}

// Unmarshal reads an envelope back. It does not Validate: a record already on the stream
// is evidence whatever its shape, and refusing to read one written by an older build
// would turn a field that was added later into a consumer that cannot start.
func Unmarshal(b []byte) (Envelope, error) {
	var e Envelope
	if err := json.Unmarshal(b, &e); err != nil {
		return Envelope{}, err
	}
	return e, nil
}

// Writer is the part of messaging.MessageWriter a sink needs. It is narrowed here so the
// sink's failure handling — which is the whole reason this type exists — can be driven by
// a test without a broker.
type Writer interface {
	WriteMessages(ctx context.Context, msgs ...messaging.Message) error
}

// writeAttempts bounds the in-process retries of a dead-letter write.
//
// 🔴 THE RETRY IS NOT OPTIMISM, IT IS THE ONLY CHANCE LEFT. Every caller of this package
// writes on the FINAL delivery — the redelivery cap is exhausted, which is why they are
// giving up — so JetStream will not redeliver the original whatever the consumer does
// with it. Leaving it unacked there does not buy another attempt; it strands the message,
// never processed and never recorded.
const writeAttempts = 3

// writeBackoff paces those attempts. Short, because the caller is holding a consumer
// goroutine while it runs and the failure it is retrying is a broker write.
const writeBackoff = 100 * time.Millisecond

// writeDeadline bounds the whole write, retries included, so a broker that accepts a
// connection and then stops answering cannot hold a consumer goroutine forever.
const writeDeadline = 30 * time.Second

// Sink writes dead letters to the platform's dead-letter subject for a tenant.
//
// 🔑 IT EXISTS SO THE WRITE-FAILURE HANDLING IS WRITTEN ONCE. The handling is the
// non-obvious part of an arm, it is identical at every call site, and it is the part that
// is wrong by default: the natural thing to write is "log and leave it unacked", which
// reads as "we will try again" and means "this message is now lost, silently".
type Sink struct {
	writer Writer
	// onLoss is called exactly once when the letter could not be written at all. It is a
	// hook rather than a log line because a loss is the one outcome an operator has to be
	// able to alert on, and only the caller knows which counter names it. Callers count
	// HERE rather than off the returned error, so the counter cannot drift away from the
	// condition it claims to measure.
	onLoss func(err error)
}

// NewSink builds a sink over a writer scoped to the dead-letter subject. onLoss may be nil.
func NewSink(w Writer, onLoss func(err error)) *Sink { return &Sink{writer: w, onLoss: onLoss} }

// detach returns a context carrying ctx's values — the TENANT, which is what scopes the
// subject — but not its cancellation, bounded instead by writeDeadline.
//
// 🔴 THE CALLER'S CONTEXT IS THE CONSUMER'S, AND IT IS CANCELLED AT SHUTDOWN. Writing the
// letter under it means a rolling restart arriving mid-arm cuts the retries — on a message
// that has exhausted its redelivery cap, so nothing will bring it back. The very moment a
// consumer is being stopped is when it is most likely to be mid-failure, so honouring
// cancellation here loses exactly the letters most worth having. The deadline is what
// keeps that from becoming "block shutdown forever" instead.
func detach(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), writeDeadline)
}

// Write records one dead letter. ctx must carry the tenant, which is what scopes the
// subject; a context without one is refused by the writer, fail-closed.
//
// It returns an error only when the letter could not be written after every attempt — at
// which point the work is LOST, and the caller has already been told through onLoss. A
// caller that ignores the error is not thereby hiding anything.
func (s *Sink) Write(ctx context.Context, e Envelope) error {
	body, err := Marshal(e)
	if err != nil {
		if s.onLoss != nil {
			s.onLoss(err)
		}
		return err
	}
	msg := messaging.Message{Value: body}
	if e.Correlation != "" {
		msg = msg.WithCorrelationID(e.Correlation)
	}
	wctx, cancel := detach(ctx)
	defer cancel()

	attempts := 0
	for attempts < writeAttempts {
		attempts++
		if err = s.writer.WriteMessages(wctx, msg); err == nil {
			return nil
		}
		if attempts < writeAttempts {
			select {
			case <-time.After(writeBackoff):
			case <-wctx.Done():
				// The deadline, not the caller's shutdown — see detach.
				attempts = writeAttempts
			}
		}
	}
	if s.onLoss != nil {
		s.onLoss(err)
	}
	return fmt.Errorf("dead letter LOST — %s could not be written in %d attempt(s) and its "+
		"source message will not redeliver: %w", e.Kind, attempts, err)
}
