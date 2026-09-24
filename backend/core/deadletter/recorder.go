// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package deadletter

import (
	"context"
	"fmt"

	nats "github.com/nats-io/nats.go"
	"github.com/rs/zerolog/log"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/governance"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/streams"
)

// tenantLifecycleGate builds the recorder's tenant-deletion gate. A variable only so a test
// can stand in for user-management; production never reassigns it.
var tenantLifecycleGate = governance.NewTenantLifecycleGate

// noOutcomeSummary is the fixed sentence on every letter the recorder writes.
const noOutcomeSummary = "delivery attempts exhausted with no outcome recorded by the consumer"

// payloadOverhead is the room left in a letter for everything but its payload, which the
// envelope carries base64-encoded (4 bytes for every 3).
const payloadOverhead = 4 << 10

// MaxDeliveryRecorder builds the platform's handler for a message whose every delivery ran
// out with no outcome (see core/messaging's recorder.go). It is what
// messaging.NatsManager.RecordMaxDeliveries takes; core/service installs it for every
// service it assembles, and a service that builds its manager by hand installs it itself.
//
// The build runs at the manager's start, after the readers exist, and makes:
//
//   - a writer on dead-letters, and one on each VERBATIM-COPY stream a reader's stream
//     declares (connector-dispatch.dead for connector-dispatch);
//   - a tenant-deletion gate, but only when some reader is on a stream whose give-ups are
//     lettered — a manager whose only reader is on dead-letters writes no letters and has
//     no tenant to ask about.
//
// It refuses to build when a reader's stream declares no DeadLetterKind or an undeclared
// one: that is a stream whose abandoned deliveries would be neither lettered nor counted.
func MaxDeliveryRecorder(p *Producer) func(*messaging.NatsManager) (messaging.MaxDeliveryFunc, error) {
	p.mustBeBuilt("MaxDeliveryRecorder")
	return func(nmgr *messaging.NatsManager) (messaging.MaxDeliveryFunc, error) {
		r := &maxDeliveryRecorder{producer: p, copies: map[string]Writer{}}
		lettered := false
		for _, suffix := range nmgr.ReaderSuffixes() {
			switch kind := streams.DeadLetterKindFor(suffix); {
			case kind == streams.NotLettered:
				continue
			case kind == "" || !Kind(kind).Valid():
				return nil, fmt.Errorf("stream %q declares dead-letter kind %q, which is not in the "+
					"vocabulary; its abandoned deliveries could not be lettered", suffix, kind)
			}
			lettered = true
			if c := streams.VerbatimCopyFor(suffix); c != "" && r.copies[c] == nil {
				w, err := nmgr.NewWriter(c)
				if err != nil {
					return nil, err
				}
				r.copies[c] = w
			}
		}
		letters, err := nmgr.NewWriter(streams.DeadLetters)
		if err != nil {
			return nil, err
		}
		// The recorder counts its own losses (MaxDeliveryLost, on the producer's counter), so
		// the sink's own hook is a no-op: a loss is then counted once, where the outcome is
		// decided.
		r.sink = &Sink{writer: letters, source: p.source, onLoss: func(error) {}}
		r.maxLetter = int(nmgr.MaxMsgSize(streams.DeadLetters))
		if lettered {
			infra := nmgr.Microservice.InstanceConfiguration.Infrastructure
			r.deleted = tenantLifecycleGate(infra.UserManagement, infra.ServiceAuth.Secret,
				nmgr.Microservice.FunctionalArea)
		}
		return r.record, nil
	}
}

// maxDeliveryRecorder is one area's recorder, built per manager start.
type maxDeliveryRecorder struct {
	producer *Producer
	sink     *Sink
	// copies holds a writer per verbatim-copy suffix the area's streams declare.
	copies map[string]Writer
	// deleted reports a deleted tenant; nil when the gate is not configured, which reads as
	// "not deleted", the same default every other consumer of the gate has.
	deleted func(string) bool
	// maxLetter is the dead-letter stream's largest message.
	maxLetter int
}

// record letters one max-delivery. See MaxDeliveryRecorder.
func (r *maxDeliveryRecorder) record(ctx context.Context, d messaging.MaxDelivery) (messaging.MaxDeliveryOutcome, error) {
	if d.Original == nil {
		log.Warn().Str("stream", d.Stream).Uint64("seq", d.StreamSeq).Str("durable", d.Consumer).
			Msg("A message ran out of deliveries with no outcome, but its stream no longer holds it; " +
				"nothing to letter")
		return messaging.MaxDeliveryGone, nil
	}
	kind := streams.DeadLetterKindFor(d.Suffix)
	if kind == streams.NotLettered {
		// The dead-letter readers — the store and the command writeback — read a sink, and a
		// letter about a letter would loop. The letter itself stays on its stream until it
		// ages out, and may never have been stored or settled, so it is counted as a loss. It
		// MAY overstate one: the reader's last delivery may have committed and lost only its
		// ack, which nothing here can see.
		r.producer.Lost()
		log.Error().Str("stream", d.Stream).Uint64("seq", d.StreamSeq).Str("durable", d.Consumer).
			Msg("LOST: a dead-letter reader exhausted its deliveries on a letter; it may not have been " +
				"stored/settled (its last delivery may have committed and lost only its ack), and it will " +
				"age out of the stream")
		return messaging.MaxDeliveryNotLettered, nil
	}
	tenant, ok := messaging.ParseTenantFromSubject(d.Original.Subject)
	if !ok || core.ValidateToken(tenant) != nil {
		// Not filed under anybody: writing it onto a tenant's dead-letter subject would
		// attribute to that tenant something that was never demonstrably theirs.
		log.Warn().Str("stream", d.Stream).Uint64("seq", d.StreamSeq).Str("subject", d.Original.Subject).
			Msg("A message ran out of deliveries with no outcome, and carries no tenant it can be lettered under")
		return messaging.MaxDeliveryUnattributable, nil
	}
	// Checked before writing, so a letter is not filed for a tenant already on its way out.
	// A purge that starts after this check can still race the write; its next pass erases it.
	if r.deleted != nil && r.deleted(tenant) {
		return messaging.MaxDeliveryTenantDeleted, nil
	}
	tctx := core.WithTenant(ctx, tenant)
	id := originID(d.Stream, d.Consumer, d.StreamSeq)
	correlation := d.Original.Header.Get(messaging.HeaderCorrelationID)

	if c := streams.VerbatimCopyFor(d.Suffix); c != "" {
		if _, err := writeWithRetries(tctx, r.copies[c], verbatimCopy(d.Original, id)); err != nil {
			return r.failed(d, fmt.Errorf("writing the verbatim copy to %s: %w", c, err))
		}
	}

	letter := Envelope{
		Kind:        Kind(kind),
		Reason:      ReasonNoOutcome,
		Summary:     noOutcomeSummary,
		Attempts:    int(d.Deliveries),
		Subject:     d.Original.Subject,
		Sequence:    d.StreamSeq,
		Correlation: correlation,
		OccurredAt:  d.At.UTC(),
	}
	switch {
	case streams.VerbatimCopyFor(d.Suffix) != "":
		// The index-entry contract of the kind: the body is on the copy, not here.
	case streams.TierFor(d.Suffix) == streams.Cold && len(d.Original.Data)*4/3+payloadOverhead <= r.maxLetter:
		letter.Payload = d.Original.Data
	default:
		// A Hot stream's letter is a POINTER: a flood of abandoned device traffic must not
		// copy itself into the cold dead-letter stream and evict the letters that matter.
		letter.Detail = fmt.Sprintf("payload omitted; the original is at %s#%d until it ages out",
			d.Stream, d.StreamSeq)
	}
	if err := r.sink.write(tctx, letter, id); err != nil {
		return r.failed(d, err)
	}
	return messaging.MaxDeliveryLettered, nil
}

// failed decides what a failed write means: on the capture durable's final delivery of the
// advisory nothing will retry it, so it is a counted LOSS; otherwise the error leaves the
// advisory unacked to be redelivered.
func (r *maxDeliveryRecorder) failed(d messaging.MaxDelivery, err error) (messaging.MaxDeliveryOutcome, error) {
	if !d.Final {
		return "", err
	}
	r.producer.Lost()
	log.Error().Err(err).Str("stream", d.Stream).Uint64("seq", d.StreamSeq).Str("durable", d.Consumer).
		Msg("LOST: a message ran out of deliveries with no outcome and could not be dead-lettered")
	return messaging.MaxDeliveryLost, nil
}

// verbatimCopy is the byte-identical copy of an original for its declared copy stream: its
// body and headers, plus the reason header, under the letter's dedup id. The original's own
// Nats-Msg-Id is not stripped here: the NATS writer is the authority on it, setting it from
// DedupID and never copying it from Headers, so the copy carries the letter's id.
func verbatimCopy(orig *nats.RawStreamMsg, id string) messaging.Message {
	headers := map[string]string{}
	for k := range orig.Header {
		headers[k] = orig.Header.Get(k)
	}
	headers[HeaderDeadReason] = DeadReasonNoOutcome
	return messaging.Message{Value: orig.Data, Headers: headers, DedupID: id}
}
