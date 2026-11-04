// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package processor

import (
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/devicechain-io/dc-event-processing/internal/runtime"
	"github.com/devicechain-io/dc-microservice/messaging"
	"github.com/devicechain-io/dc-microservice/resolvedevent"
	"github.com/rs/zerolog/log"
)

// newAppliedGuard builds the duplicate-event guard, or returns nil when event envelopes are off:
// with the switch off DETECT builds, primes and consults nothing, and behaviour and cost are
// exactly as they were before the guard existed.
func (rp *ResolvedEventsProcessor) newAppliedGuard() *runtime.AppliedGuard {
	if !rp.cfg.AppliedGuard {
		return nil
	}
	return runtime.NewAppliedGuard(rp.cfg.AppliedRetention, rp.cfg.AppliedMaxEntries)
}

// duplicateEvent reports whether an event with the same identity was already applied inside the
// guard's retention, recording this one (as appended at t) when it was not. Always false with no
// guard. An identity that cannot be computed is never a duplicate (the event applies).
func (rp *ResolvedEventsProcessor) duplicateEvent(t time.Time, tenant string, event *resolvedevent.ResolvedEvent) bool {
	if rp.applied == nil {
		return false
	}
	id, err := runtime.EventIdentity(tenant, event)
	if err != nil {
		return false
	}
	return !rp.applied.Admit(t, id)
}

// messageAtOrAfter is the first message the stream holds at or after seq, and false when it
// holds none.
func (rp *ResolvedEventsProcessor) messageAtOrAfter(seq uint64) (messaging.Message, bool, error) {
	reader, _, err := rp.Replay.NewReplayReader(rp.cfg.Suffix, seq)
	if err != nil {
		return messaging.Message{}, false, fmt.Errorf("open a read at seq %d: %w", seq, err)
	}
	defer func() { _ = reader.Close() }()
	msg, err := reader.Read(rp.pctx())
	if errors.Is(err, io.EOF) {
		return messaging.Message{}, false, nil
	}
	if err != nil {
		return messaging.Message{}, false, fmt.Errorf("read at seq %d: %w", seq, err)
	}
	return msg, true, nil
}

// appendTimeAtOrBefore is the append time of the newest message the stream holds at or before
// seq (LastSeq may itself have been deleted), and false when it holds none.
func (rp *ResolvedEventsProcessor) appendTimeAtOrBefore(seq uint64) (time.Time, bool, error) {
	for step := uint64(0); ; step = max(step*4, 1) {
		from := uint64(1)
		if seq > step {
			from = seq - step
		}
		msg, found, err := rp.messageAtOrAfter(from)
		if err != nil {
			return time.Time{}, false, err
		}
		if found && msg.StreamSeq <= seq {
			return msg.AppendTime, true, nil
		}
		if from == 1 {
			return time.Time{}, false, nil
		}
	}
}

// primeApplied rebuilds the duplicate guard for a restored engine: it observes every event of
// the retention's worth of messages at or below LastSeq, in order, exactly as the loop did when
// it applied them. The guard is a pure function of the events before the point it is asked about
// and their append times, so this is the set the loop that wrote the snapshot held, and the
// replay that follows decides each duplicate as that loop did. Nothing is applied: only the guard
// is fed. It does nothing when the guard is off.
//
// The start is found by bisecting the sequence space on append time (monotone in the sequence),
// which needs only the reader the replay already uses. When the guard's entry cap would bind, the
// start is moved up to the cap boundary (the newest MaxEntries messages hold at least as many
// events as the cap keeps), so a busy stream is not read in full for entries the cap would evict.
//
// 🔴 A STREAM THAT NO LONGER REACHES BACK THE WHOLE RETENTION IS SAID, NOT HIDDEN. When the oldest
// message the stream still holds is newer than the retention before LastSeq (and is not the
// first message ever written), the primed set is shorter than the live one was: it is logged and
// counted on detect_guard_prime_partial_total, because a duplicate of an event that fell off the
// front of the stream would be applied twice.
//
// COST: a term build reads and decodes the retention's worth of the stream (10 minutes, ~12
// million events at 20,000/s) before it replays; it is why a leader change is not instant, and it
// is measured on detect_guard_prime_seconds.
func (rp *ResolvedEventsProcessor) primeApplied() error {
	last := rp.engine.LastSeq()
	if rp.applied == nil || last == 0 {
		return nil
	}
	began := rp.clock.Now()
	retention := rp.applied.Retention()
	tLast, ok, err := rp.appendTimeAtOrBefore(last)
	if err != nil {
		return err
	}
	if !ok {
		return nil // the stream holds nothing at or before LastSeq: nothing was applied that a later message could duplicate
	}
	// Start a little early so the first message read lands in a generation the loop also held.
	cutoff := tLast.Add(-retention - time.Minute)
	lo, hi := uint64(1), last
	for lo < hi {
		mid := lo + (hi-lo)/2
		msg, found, err := rp.messageAtOrAfter(mid)
		if err != nil {
			return err
		}
		if !found || !msg.AppendTime.Before(cutoff) {
			hi = mid
		} else {
			lo = mid + 1
		}
	}
	capBound := false
	if boundary := last + 1 - min(last+1, uint64(rp.applied.MaxEntries())); lo < boundary {
		lo, capBound = boundary, true
	}
	reader, _, err := rp.Replay.NewReplayReader(rp.cfg.Suffix, max(lo, 1))
	if err != nil {
		return fmt.Errorf("open the duplicate-guard priming read from seq %d: %w", lo, err)
	}
	defer func() { _ = reader.Close() }()
	events, first, partial := 0, true, false
	for {
		msg, err := reader.Read(rp.pctx())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("duplicate-guard priming read: %w", err)
		}
		if msg.StreamSeq > last {
			break
		}
		if first {
			first = false
			// The oldest message the stream holds is newer than the retention before LastSeq, and
			// is not the first ever written: history this guard should remember was removed.
			partial = !capBound && msg.StreamSeq > 1 && msg.AppendTime.After(tLast.Add(-retention))
		}
		_, tenant, ok := messaging.TenantContextFromSubject(rp.pctx(), msg.Subject)
		if !ok {
			continue
		}
		values := [][]byte{msg.Value}
		if msg.IsEnvelope() {
			units, _, serr := messaging.SplitEnvelope(msg, tenant, func(string) {}, nil)
			if serr != nil {
				continue
			}
			values = make([][]byte, len(units))
			for i, u := range units {
				values[i] = u.Msg.Value
			}
		}
		for _, v := range values {
			if event, derr := rp.decode(v); derr == nil {
				rp.duplicateEvent(msg.AppendTime, tenant, event)
				events++
			}
		}
	}
	rp.primedPartial = partial
	took := rp.clock.Now().Sub(began)
	rp.metrics.recordGuardPrimed(took.Seconds(), events, partial)
	ev := log.Info().Uint64("fromSeq", lo).Uint64("lastSeq", last).Int("events", events).
		Int("remembered", rp.applied.Len()).Dur("took", took).Bool("capBound", capBound)
	if partial {
		log.Warn().Uint64("fromSeq", lo).Uint64("lastSeq", last).Dur("retention", retention).
			Msg("The resolved-events stream no longer reaches back the duplicate guard's whole retention; " +
				"a duplicate of an event older than the stream's oldest message will be applied twice.")
	}
	ev.Msg("Primed the DETECT duplicate-event guard from the stream.")
	return nil
}
