// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/devicechain-io/dc-microservice/messaging"
)

// THE LIVE LOOP NEVER LETS THE ENGINE'S SEQUENCE PASS A MESSAGE IT HAS NOT SEEN.
//
// The durable hands messages out in delivery order, and a delivery can be lost on the way:
// a connection that drops while a Fetch is answered returns the part of the batch that
// arrived and no error, while the broker has already started AckWait on the whole batch.
// If the connection comes back inside the leadership lease the term simply continues, the
// next Fetch hands out sequences after the lost ones, and the engine's sequence moves past
// a range it never applied. When AckWait expires the broker redelivers the range, and the
// duplicate guard in applyResolved (seq <= LastSeq) drops it as a duplicate and the next
// checkpoint acks it. Nothing was applied, nothing was recorded, and the broker will never
// offer it again.
//
// So before a live message is decoded, guarded or applied, handle compares its stream
// sequence with the engine's. When it is more than one ahead, the sequences in between are
// read from the stream by position (NewRangeReader) and applied in order first. Sequences
// the stream no longer holds (a tenant purge removes interior ones; retention removes the
// oldest) are simply absent and are counted. If the range cannot be read, the loop parks:
// it stops receiving live messages, keeps the message in hand, and retries on the ticker,
// serving rule, roster, attribute and fence updates and tenant purges meanwhile.
//
// With no gap the cost is the one comparison in handle.
//
// HOW WIDE A GAP CAN BE. The reader has one pull request in flight at a time (with fetch-ahead
// on, the next is issued while the current batch is handed out, and its result is consumed
// before another is issued), so a dropped connection can cost the deliveries of at most ONE
// pull: a range of at most one fetch batch. Fetch-ahead adds a second source: a prefetched
// batch that sat longer than half the acknowledgement window is dropped unhanded (left to the
// broker to redeliver), which is one more batch-wide range. The two can meet, an aged drop
// followed by a lost synchronous pull, and make one contiguous gap of two batches. The batch is
// configured by infrastructure.nats.fetch.batch, which is refused above 256 at startup, and 256
// is the widest range NewRangeReader reads one request per sequence (rangeDirectMax). So the
// fill of a one-batch gap is always the cheap path; the two-batch case, and anything else
// (a stream that lost its head, a purge), takes the range reader's wide path, which handles
// any width.

// Outcome label values for detect_live_gap_fills_total.
const (
	// gapFillFilled: the range was read to its end and at least one message came back.
	gapFillFilled = "filled"
	// gapFillAbsentOnly: the range was read to its end and the stream held none of it.
	gapFillAbsentOnly = "absent_only"
	// gapFillFailed: the range could not be read; the loop parked.
	gapFillFailed = "failed"
)

// Outcome label values for detect_live_gap_sequences_total.
const (
	// gapSeqApplied: a sequence in a gap that the engine took.
	gapSeqApplied = "applied"
	// gapSeqAbsent: a sequence in a gap that the stream does not hold.
	gapSeqAbsent = "absent"
	// gapSeqSkipped: a sequence in a gap that came back unprocessable (no tenant, unparseable
	// payload) and was recorded as handled without applying any state, like any other poison.
	gapSeqSkipped = "skipped"
)

// gapFillOutcomes and gapSeqOutcomes are the label sets, registered at zero.
var (
	gapFillOutcomes = []string{gapFillFilled, gapFillAbsentOnly, gapFillFailed}
	gapSeqOutcomes  = []string{gapSeqApplied, gapSeqAbsent, gapSeqSkipped}
)

// gapParkLimit is how long a live gap fill may fail continuously before the term is ended.
// It sits past the 5m DetectLiveGapFillFailing alert on purpose: an operator is paged for a
// broker fault before anything restarts on its own, and only a failure that outlasts that is
// treated as one a retry will not fix.
const gapParkLimit = 10 * time.Minute

// fillGap is handle's slow path: msg's sequence is more than one past the engine's. It
// reads and applies the missing range, and reports whether msg may now be applied. On
// false the loop is parked on msg (see run).
func (rp *ResolvedEventsProcessor) fillGap(msg messaging.Message) bool {
	from, to := rp.engine.LastSeq()+1, msg.StreamSeq-1
	rp.phases.enter(phaseGapFill)
	if err := rp.fillRange(from, to); err != nil {
		if rp.pctx().Err() != nil {
			// The term is ending, which is why the read failed. That is not a gap the loop
			// could not read, so nothing is logged, counted or parked: the message was never
			// acked and redelivers to whoever leads next.
			return false
		}
		if !rp.gapFailLogged {
			rp.gapFailLogged = true
			log.Error().Err(err).Uint64("from", from).Uint64("to", to).Uint64("heldSeq", msg.StreamSeq).
				Msg("Live consumption found a gap in the stream sequences and could not read it; parking until it can. Nothing is applied past the gap.")
		}
		held := msg
		rp.gapHeld = &held
		now := rp.clock.Now()
		if rp.gapParkedSince.IsZero() {
			rp.gapParkedSince = now
		}
		if parked := now.Sub(rp.gapParkedSince); parked >= gapParkLimit {
			// A broker fault clears itself and is alert-only; a fill that has failed for this long
			// is far more likely deterministic (a range reader that returns the wrong sequences, a
			// poisoned range), and retrying the same read every tick will never change the answer.
			// Ending the TERM, not the process, is the escalation: the rebuild re-reads the range
			// through the ordered replay path, and if that cannot read it either, the term-build
			// fuse takes the process down. The held message was never acked and redelivers.
			log.Error().Err(err).Dur("parked", parked).Uint64("from", from).Uint64("to", to).
				Msg("Live gap fill has failed continuously past its bound; ending the term so the rebuild re-reads the range.")
			rp.gapParkedSince = time.Time{}
			if !rp.leadershipEnabled() && rp.Microservice != nil {
				// No supervisor to rebuild the term: ending it alone would leave a Ready pod that
				// detects nothing, which is the shape haltStaleWriter refuses for the same reason.
				rp.Microservice.FailNow(fmt.Errorf("event-processing: the live gap %d..%d on partition %q "+
					"could not be read for %s", from, to, rp.cfg.PartitionId, parked.Round(time.Second)))
			}
			rp.pcancel()
		}
		return false
	}
	rp.gapFailLogged = false
	rp.gapParkedSince = time.Time{}
	rp.phases.enter(phaseDecode) // the held message is decoded next, not applied
	return true
}

// retryGapFill runs on the ticker while the loop is parked on a gap. A fill that has
// applied part of the range before failing has advanced the engine, so the retry starts
// again from wherever the engine now is.
func (rp *ResolvedEventsProcessor) retryGapFill() {
	if rp.gapHeld == nil {
		return
	}
	msg := *rp.gapHeld
	rp.gapHeld = nil
	rp.handle(msg) // re-parks on a failed fill
	if rp.gapHeld == nil && len(rp.pendingAcks) >= rp.cfg.CheckpointEvents {
		rp.checkpoint(rp.pctx())
	}
}

// fillRange applies every message the stream holds in [from, to], in order, through the
// same path a replayed message takes. The messages are not appended to pendingAcks: they
// are not deliveries of the durable, whose own copies are redelivered later and dropped
// (and acked) correctly because they were applied.
func (rp *ResolvedEventsProcessor) fillRange(from, to uint64) (err error) {
	// delivered is every message the reader returned; applied is those that changed engine
	// state and skipped those recorded as handled without (poison); absentBehind counts
	// sequences found absent that lie before a message that advanced the engine, which the
	// engine has therefore moved past for good.
	var delivered, applied, skipped, absentBehind uint64
	defer func() {
		switch {
		case rp.pctx().Err() != nil && err != nil:
			// Shutting down: not a failure of the fill (see fillGap). What was applied is still
			// counted below.
		case err != nil:
			rp.metrics.recordGapFill(gapFillFailed)
		case delivered == 0:
			rp.metrics.recordGapFill(gapFillAbsentOnly)
		default:
			rp.metrics.recordGapFill(gapFillFilled)
		}
		if err != nil {
			// A failed fill is retried from wherever the engine now is, so the retry will read
			// again anything that did not advance it. Only what the engine has passed is
			// counted here; the rest is counted by the pass that completes.
			rp.metrics.recordGapSequences(gapSeqApplied, applied)
			rp.metrics.recordGapSequences(gapSeqSkipped, skipped)
			rp.metrics.recordGapSequences(gapSeqAbsent, absentBehind)
			return
		}
		rp.metrics.recordGapSequences(gapSeqApplied, applied)
		rp.metrics.recordGapSequences(gapSeqSkipped, skipped)
		rp.metrics.recordGapSequences(gapSeqAbsent, (to-from+1)-delivered)
	}()

	if rp.Replay == nil {
		return errors.New("no range reader is configured")
	}
	reader, err := rp.Replay.NewRangeReader(rp.cfg.Suffix, from, to)
	if err != nil {
		return fmt.Errorf("open range reader: %w", err)
	}
	defer func() { _ = reader.Close() }()

	var sinceCheckpoint uint64
	last := from - 1
	for {
		m, err := reader.Read(rp.pctx())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read range: %w", err)
		}
		// A reader that hands back a sequence outside the range, or out of order, is not
		// one whose result can be trusted, so the fill fails rather than applying it.
		if m.StreamSeq <= last || m.StreamSeq > to {
			return fmt.Errorf("range reader returned seq %d after %d for range %d..%d", m.StreamSeq, last, from, to)
		}
		gap := m.StreamSeq - last - 1
		last = m.StreamSeq
		delivered++
		skippedBefore := rp.poisonSkipped
		if rp.applyResolved(m) {
			if rp.poisonSkipped != skippedBefore {
				skipped++
			} else {
				applied++
			}
			absentBehind += gap
			sinceCheckpoint++
		}
		// A wide gap would otherwise hold every detection it fires until the end of the fill.
		if rp.cfg.CheckpointEvents > 0 && sinceCheckpoint >= uint64(rp.cfg.CheckpointEvents) {
			rp.checkpoint(rp.pctx())
			sinceCheckpoint = 0
		}
	}

	absent := (to - from + 1) - delivered
	if delivered == 0 {
		log.Debug().Uint64("from", from).Uint64("to", to).
			Msg("Live sequence gap held nothing in the stream (purged or evicted); continuing.")
		return nil
	}
	// applied > 0 is the signature of a lost delivery (a partial fetch, or a co-writer that
	// took part of the stream), which before this was invisible.
	log.Warn().Uint64("from", from).Uint64("to", to).Uint64("applied", applied).
		Uint64("skipped", skipped).Uint64("absent", absent).
		Msg("Live consumption skipped ahead of the engine's sequence; the missing range was read from the stream and applied.")
	return nil
}
