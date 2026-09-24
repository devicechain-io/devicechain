// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/rs/zerolog/log"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/streams"
)

// The max-delivery recorder: a durable record of every message whose deliveries ran out.
//
// # The gap it closes
//
// Every durable is created with MaxDeliver, and a service's dead-letter arm writes a letter
// when its handler REACHES the final delivery and gives up. That covers a handler that runs
// and fails. It does not cover a delivery that ends with NO outcome at all: the pod was
// stopped mid-handling, or the handler ran past its ack window, and the broker's clock ran
// out on the fifth delivery with nothing having been decided. No arm ran, so no letter was
// written; the broker stopped redelivering, so nothing would run again; and the message
// aged out of its stream with no record anywhere.
//
// # How it is recorded
//
// The broker DOES know. When it gives up on a message it publishes a max-delivery advisory
// on "$JS.EVENT.ADVISORY.CONSUMER.MAX_DELIVERIES.<stream>.<durable>". The advisory is sent
// on the NEXT PULL of that durable after the final delivery's ack window expires — not from
// a timer — and only if something is subscribed to the subject then. So a work-queue stream
// (streams.MaxDeliveries) captures every such subject, and each area runs one recorder: a
// durable on that stream whose filter is exactly its own durables' advisory subjects, which
// makes the areas' filters disjoint. The recorder fetches the original by stream sequence
// and hands both to the area's MaxDeliveryFunc, which writes the letter (core/deadletter).
//
// The letter carries a dedup id derived from (stream, durable, sequence), and a service's
// own arm derives the same id from the message's Origin — so a give-up that both an arm and
// the recorder see (the arm overran its window, then wrote) is stored once.
//
// # What it does not letter
//
// A durable that its stream's declaration names REPLAY-COVERED for this area
// (streams.Stream.ReplayCovered) loses nothing when its deliveries run out: the area re-reads
// the stream by sequence from a checkpoint it commits itself, and acks only behind that
// checkpoint. Its exhaustion means the checkpoint has been failing, not that a message was
// abandoned, so the recorder acks the advisory without fetching the original or calling the
// MaxDeliveryFunc, and counts it under outcome "replay-covered", which the chart alerts on.
// The recorder reads that from the declaration once, at start; it knows no area or stream by
// name.
//
// # What it cannot do
//
// A record is LATE, not lost, while an area is down: nothing is emitted until some replica
// pulls the exhausted durable again. And the broker produces an advisory into nothing when
// the capture stream does not exist yet, lists no subject for that stream, or has no leader.
// Both are properties of the broker, stated on the stream's declaration.

// MaxDelivery is one message the broker stopped redelivering, as its advisory reported it.
type MaxDelivery struct {
	// Suffix, Stream and Consumer name the original's declared stream, its JetStream stream
	// and the durable whose deliveries ran out.
	Suffix, Stream, Consumer string
	// StreamSeq is the original's position; Deliveries is how many times it was delivered.
	StreamSeq, Deliveries uint64
	// At is the advisory's own timestamp: when the broker gave up.
	At time.Time
	// Original is the message as its stream holds it, or nil when the stream no longer
	// does (it aged out, was evicted, or was purged with its tenant).
	Original *nats.RawStreamMsg
	// Final is true on the capture durable's own LAST delivery of this advisory: an error
	// returned now is not retried, so the func must record a loss rather than return one.
	Final bool
}

// MaxDeliveryOutcome is what a MaxDeliveryFunc did with one advisory. It is a metric label,
// so the set is closed.
type MaxDeliveryOutcome string

const (
	// MaxDeliveryLettered: a dead letter was written (or was already there).
	MaxDeliveryLettered MaxDeliveryOutcome = "lettered"
	// MaxDeliveryGone: the stream no longer held the original.
	MaxDeliveryGone MaxDeliveryOutcome = "gone"
	// MaxDeliveryUnattributable: the original carries no tenant it can be filed under.
	MaxDeliveryUnattributable MaxDeliveryOutcome = "unattributable"
	// MaxDeliveryTenantDeleted: the original's tenant has been deleted.
	MaxDeliveryTenantDeleted MaxDeliveryOutcome = "tenant-deleted"
	// MaxDeliveryNotLettered: the original was on a stream whose give-ups are never
	// lettered (a dead-letter reader's own); counted as a loss instead.
	MaxDeliveryNotLettered MaxDeliveryOutcome = "not-lettered"
	// MaxDeliveryLost: the letter could not be written on the recorder's final attempt.
	MaxDeliveryLost MaxDeliveryOutcome = "lost"
	// MaxDeliveryReplayCovered: the durable is declared replay-covered for this area
	// (streams.Stream.ReplayCovered), so nothing was lost and no letter is written; the
	// exhaustion is counted, because it means the area's checkpoint has not committed for
	// longer than AckWait x MaxDeliver. Decided here, never by the func.
	MaxDeliveryReplayCovered MaxDeliveryOutcome = "replay-covered"
	// maxDeliveryMalformed: the capture delivered something that is not a max-delivery
	// advisory for one of this manager's durables. Decided here, never by the func.
	maxDeliveryMalformed MaxDeliveryOutcome = "malformed"
)

// maxDeliveryOutcomes is every outcome the func can return, plus malformed, for initialising a
// lettered durable's series at zero. A replay-covered durable gets replayCoveredOutcomes
// instead: the func never sees its advisories, so a zero under "lettered" or "lost" would
// claim a measurement nothing makes.
var maxDeliveryOutcomes = []MaxDeliveryOutcome{
	MaxDeliveryLettered, MaxDeliveryGone, MaxDeliveryUnattributable, MaxDeliveryTenantDeleted,
	MaxDeliveryNotLettered, MaxDeliveryLost, maxDeliveryMalformed,
}

// replayCoveredOutcomes is what the handler can count for a replay-covered durable.
var replayCoveredOutcomes = []MaxDeliveryOutcome{MaxDeliveryReplayCovered, maxDeliveryMalformed}

// MaxDeliveryFunc records one max-delivery. An error leaves the advisory unacked, so it is
// redelivered after the ack window; see MaxDelivery.Final for the one delivery on which that
// is no longer true.
type MaxDeliveryFunc func(ctx context.Context, d MaxDelivery) (MaxDeliveryOutcome, error)

// RecordMaxDeliveries installs how this manager records a message whose deliveries ran out.
// It is called in the INITIALIZE phase; build runs at each start, after the oncreate callback
// has built the readers, so it can see them (ReaderSuffixes) and build its writers on the
// same connection. core/deadletter.MaxDeliveryRecorder is the one production build.
//
// 🔴 A MANAGER WITH READERS REFUSES TO START WITHOUT IT. Omitting the recorder is not a
// smaller feature set, it is a silent gap in the one record of abandoned work — so it is a
// startup failure, not a default.
func (nmgr *NatsManager) RecordMaxDeliveries(build func(*NatsManager) (MaxDeliveryFunc, error)) {
	if build == nil {
		panic("messaging: RecordMaxDeliveries needs a build function")
	}
	nmgr.recordBuild = build
}

// ReaderSuffixes returns the declared suffix of every durable reader this manager has built,
// each once, in creation order.
func (nmgr *NatsManager) ReaderSuffixes() []string {
	out := []string{}
	for _, r := range nmgr.readers {
		if !slices.Contains(out, r.suffix) {
			out = append(out, r.suffix)
		}
	}
	return out
}

// MaxMsgSize is the largest message suffix's stream accepts, as this manager configures it.
func (nmgr *NatsManager) MaxMsgSize(suffix string) int32 {
	return nmgr.streamBounds(suffix).maxMsgSize
}

// maxDeliveryAdvisoryType is the schema type of the broker's max-delivery advisory.
const maxDeliveryAdvisoryType = "io.nats.jetstream.advisory.v1.max_deliver"

// originalFetchTimeout bounds the fetch of the original by sequence.
const originalFetchTimeout = 5 * time.Second

// maxDeliveryAdvisory is the wire shape of the advisory, the fields the recorder reads.
type maxDeliveryAdvisory struct {
	Type       string    `json:"type"`
	Time       time.Time `json:"timestamp"`
	Stream     string    `json:"stream"`
	Consumer   string    `json:"consumer"`
	StreamSeq  uint64    `json:"stream_seq"`
	Deliveries uint64    `json:"deliveries"`
}

// startRecorder starts this manager's recorder over the readers oncreate built. A manager
// with no readers starts none: it has no durable whose deliveries can run out.
func (nmgr *NatsManager) startRecorder(ctx context.Context) error {
	if len(nmgr.readers) == 0 {
		return nil
	}
	if nmgr.recordBuild == nil {
		return fmt.Errorf("messaging: %d reader(s) but no max-delivery recorder: a message whose deliveries "+
			"run out with no outcome would leave no record anywhere; call RecordMaxDeliveries in the "+
			"initialize phase", len(nmgr.readers))
	}
	// The same re-entry guard the sampler uses: a start retried without a stop keeps the
	// recorder it already has.
	if nmgr.recorderCancel != nil {
		return nil
	}
	record, err := nmgr.recordBuild(nmgr)
	if err != nil {
		return fmt.Errorf("building the max-delivery recorder: %w", err)
	}
	if record == nil {
		return errors.New("messaging: the max-delivery recorder build returned no function")
	}
	stream, err := nmgr.ensureStream(streams.MaxDeliveries)
	if err != nil {
		return err
	}
	suffixOf := map[string]string{}
	covered := map[string]bool{}
	filters := []string{}
	for _, r := range nmgr.readers {
		if streams.RetentionFor(r.suffix) == streams.RetentionWorkQueue {
			continue
		}
		suffixOf[r.stream] = r.suffix
		// The one place the replay-covered declaration is read: the handler decides from
		// this map, and the series are initialised from it below.
		covered[r.stream] = streams.ReplayCoveredBy(r.suffix, nmgr.Microservice.FunctionalArea)
		if subj := AdvisorySubject(r.stream, r.durable); !slices.Contains(filters, subj) {
			filters = append(filters, subj)
		}
	}
	slices.Sort(filters)
	rec := &natsReader{
		nmgr:    nmgr,
		suffix:  streams.MaxDeliveries,
		stream:  stream,
		durable: DurableName(nmgr.Microservice.InstanceId, nmgr.Microservice.FunctionalArea, streams.MaxDeliveries),
		filters: filters,
		// No readiness gate: recording an abandoned delivery needs nothing the data plane's
		// auth provides, and an area that is not ready yet is exactly one that may be
		// abandoning deliveries.
	}
	if err := rec.bind(); err != nil {
		return fmt.Errorf("binding the max-delivery recorder: %w", err)
	}
	h := &maxDeliveryHandler{nmgr: nmgr, record: record, suffixOf: suffixOf, replayCovered: covered}
	for s := range suffixOf {
		nmgr.metrics.initMaxDeliveryRecords(s, covered[s])
	}
	rctx, cancel := context.WithCancel(ctx)
	nmgr.recorder = rec
	nmgr.recorderCancel = cancel
	nmgr.recorderWg.Add(1)
	pacer := core.NewReadPacer(nmgr.Microservice, "max-delivery advisories")
	go func() {
		defer nmgr.recorderWg.Done()
		RunConsumer(rctx, rec, pacer, func(msg Message) bool {
			h.handle(rctx, msg)
			return true
		})
	}()
	log.Info().Str("durable", rec.durable).Strs("filters", filters).Msg("Started the max-delivery recorder")
	return nil
}

// stopRecorder ends the recorder and joins it, bounded by ctx, then releases its
// subscription. The durable survives, as every reader's does: another replica, or this
// area's next pod, picks up whatever it had not acked.
func (nmgr *NatsManager) stopRecorder(ctx context.Context) {
	if nmgr.recorderCancel == nil {
		return
	}
	nmgr.recorderCancel()
	joined := make(chan struct{})
	go func() {
		nmgr.recorderWg.Wait()
		close(joined)
	}()
	select {
	case <-joined:
	case <-ctx.Done():
		log.Warn().Err(ctx.Err()).Msg("Max-delivery recorder did not stop within the shutdown budget; " +
			"continuing. An advisory it was handling stays unacked and is redelivered.")
	}
	nmgr.recorderCancel = nil
	if rec := nmgr.recorder; rec != nil && nmgr.nc != nil && !nmgr.nc.IsClosed() {
		if s := rec.sub.Load(); s != nil {
			if err := s.Unsubscribe(); err != nil {
				log.Error().Err(err).Msg("Error unsubscribing the max-delivery recorder.")
			}
		}
	}
	nmgr.recorder = nil
}

// maxDeliveryHandler turns one captured advisory into a MaxDelivery and records it.
type maxDeliveryHandler struct {
	nmgr   *NatsManager
	record MaxDeliveryFunc
	// suffixOf maps each of this manager's reader streams to its declared suffix. The
	// recorder's filter admits only those streams' advisories.
	suffixOf map[string]string
	// replayCovered marks the streams on which this area's durable is declared
	// replay-covered (streams.Stream.ReplayCovered).
	replayCovered map[string]bool
}

// handle records one advisory and acks it, or leaves it unacked to be redelivered when the
// original could not be fetched or the record func failed.
func (h *maxDeliveryHandler) handle(ctx context.Context, msg Message) {
	stream, consumer, parsed := parseAdvisorySubject(msg.Subject)
	var adv maxDeliveryAdvisory
	decodeErr := json.Unmarshal(msg.Value, &adv)
	suffix, known := h.suffixOf[stream]
	if !parsed || decodeErr != nil || adv.Type != maxDeliveryAdvisoryType || adv.Stream != stream ||
		adv.Consumer != consumer || adv.StreamSeq == 0 || !known {
		label := stream
		if !known {
			label = "unknown"
		}
		log.Error().Err(decodeErr).Str("subject", msg.Subject).Str("type", adv.Type).
			Str("stream", adv.Stream).Str("consumer", adv.Consumer).Uint64("seq", adv.StreamSeq).
			Msg("The max-delivery capture delivered something that is not a max-delivery advisory for one of " +
				"this service's durables; acking it so it does not block the queue")
		h.nmgr.metrics.countMaxDelivery(label, maxDeliveryMalformed)
		h.ack(msg)
		return
	}
	if h.replayCovered[stream] {
		// Not lettered, and not fetched: the area re-reads its stream from a checkpoint it
		// commits itself, so the message is not lost and a letter would report a loss that
		// did not happen. What IS true is that the area's checkpoint has been failing for
		// longer than AckWait x MaxDeliver — possibly for every message in that window, so
		// this is counted rather than logged per message.
		h.nmgr.metrics.countMaxDelivery(stream, MaxDeliveryReplayCovered)
		h.ack(msg)
		return
	}
	final := msg.NumDelivered >= MaxDeliver
	fctx, cancel := context.WithTimeout(ctx, originalFetchTimeout)
	orig, err := h.nmgr.js.GetMsg(stream, adv.StreamSeq, nats.Context(fctx))
	cancel()
	if errors.Is(err, nats.ErrMsgNotFound) {
		orig, err = nil, nil
	}
	if err != nil {
		ev := log.Warn()
		if final {
			// The advisory stays on the work queue, unacked and no longer redelivered, which is
			// what the MaxDeliveryRecordsWaiting alert watches for.
			ev = log.Error()
		}
		ev.Err(err).Str("stream", stream).Uint64("seq", adv.StreamSeq).Bool("final", final).
			Msg("Could not fetch a message whose deliveries ran out; its advisory is left unacked")
		return
	}
	at := adv.Time
	if at.IsZero() {
		at = time.Now().UTC()
	}
	outcome, err := h.record(ctx, MaxDelivery{
		Suffix: suffix, Stream: stream, Consumer: consumer,
		StreamSeq: adv.StreamSeq, Deliveries: adv.Deliveries,
		At: at, Original: orig, Final: final,
	})
	if err != nil {
		log.Warn().Err(err).Str("stream", stream).Uint64("seq", adv.StreamSeq).
			Msg("Could not record a message whose deliveries ran out; its advisory will be redelivered")
		return
	}
	h.nmgr.metrics.countMaxDelivery(stream, outcome)
	h.ack(msg)
}

func (h *maxDeliveryHandler) ack(msg Message) {
	if err := msg.Ack(); err != nil {
		log.Warn().Err(err).Msg("Could not ack a max-delivery advisory; it will be redelivered and recorded " +
			"again, which the letter's dedup id absorbs")
	}
}

// parseAdvisorySubject splits "$JS.EVENT.ADVISORY.CONSUMER.MAX_DELIVERIES.<stream>.<durable>".
func parseAdvisorySubject(subject string) (stream, durable string, ok bool) {
	rest, found := strings.CutPrefix(subject, maxDeliveriesAdvisoryPrefix+".")
	if !found {
		return "", "", false
	}
	parts := strings.Split(rest, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}
