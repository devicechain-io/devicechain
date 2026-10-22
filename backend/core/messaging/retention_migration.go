// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package messaging

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	nats "github.com/nats-io/nats.go"
	"github.com/rs/zerolog/log"

	"github.com/devicechain-io/dc-microservice/streams"
)

// Moving a stream onto a different retention.
//
// JetStream cannot change a stream's retention in place, and ensureStream refuses a stream
// whose retention differs from the declared one. core/streams declares exactly one stream
// that may instead be drained and recreated (Stream.RecreateOnRetentionChange), for the move
// from Limits to a work queue. This file is that migration and nothing wider.
//
// # How it runs
//
// ensureStream leaves such a stream as it is and records it. The stream's reader keeps
// consuming it, so the backlog drains by the ordinary path, and once the owning service has
// its sources running it calls MigratePendingRetention. That waits until the service's own
// durable has nothing pending and nothing awaiting an ack, then deletes the stream and
// creates it again with the declared retention. The reader finds its durable gone and binds
// a new one on the recreated stream, the same self-heal it uses for any lost consumer.
//
// If the stream does not drain within the bound, the call fails, and the service refuses to
// start rather than run on a stream that does not behave as declared. The wait says what it
// is waiting for when it starts and every thirty seconds after.
//
// # Replicas
//
// Every replica of the area records the migration and runs it. They are serialised by the
// distributed lock, and each one re-reads the stream under the lock: a stream that already
// holds the declared retention is left alone, and a stream that is not the one that was
// drained (a different creation time) sends the replica back to waiting. Without that a
// second replica would delete the stream the first had just recreated.
//
// # What it keeps
//
// The recreated stream starts well past anything the old one assigned. Sequences are not
// cosmetic here: the capture sequence is the basis of the dedup id on the forwarded event
// (tenant:sequence, held for the destination stream's duplicate window), so a sequence that
// repeated one already forwarded would silently suppress a new event. Gaps in the sequence
// cost nothing, so the start is the old last sequence plus a margin that covers anything
// assigned between reading it and the recreate. A stream created WITHOUT an old one to read
// (a restart after a failed recreate, or a fresh install) starts at the current time in
// milliseconds, which is past any sequence a platform could have assigned.
//
// # What it cannot keep
//
// A publish that arrives between the drained check and the recreate is lost. The old and new
// stream cannot coexist (their subjects would overlap), so no ordering closes that gap.
const (
	// retentionDrainBound is how long MigratePendingRetention waits for a drained stream.
	retentionDrainBound = 5 * time.Minute
	// retentionDrainPoll is the wait between checks. Short, because the stream is drained
	// only transiently while devices keep publishing.
	retentionDrainPoll = 100 * time.Millisecond
	// retentionLogEvery is how often the drain wait says it is still waiting, and why.
	retentionLogEvery = 30 * time.Second
	// retentionLockTTL is the TTL of the distributed lock's bucket. It is the value the other
	// user of that bucket creates it with, which the bucket must be opened with again.
	retentionLockTTL = 5 * time.Second
	// retentionSeqMargin is added to the old stream's last sequence to place the recreated
	// stream's first one.
	retentionSeqMargin = 1_000_000
	// retentionAddAttempts and retentionAddBackoff bound the retry of the create that follows
	// the delete; the stream does not exist until it succeeds.
	retentionAddAttempts = 30
	retentionAddBackoff  = time.Second
)

// captureFirstSeq is where a stream with no predecessor to read starts: the current time in
// milliseconds. See "What it keeps".
func captureFirstSeq() uint64 { return uint64(time.Now().UnixMilli()) }

func (nmgr *NatsManager) notePendingRetention(suffix, name string) {
	nmgr.streamMu.Lock()
	defer nmgr.streamMu.Unlock()
	if nmgr.retentionPending == nil {
		nmgr.retentionPending = map[string]string{}
	}
	nmgr.retentionPending[suffix] = name
}

// PendingRetentionMigrations returns the suffixes whose existing stream holds an older
// retention than declared and awaits MigratePendingRetention. A service that owns such a
// stream calls MigratePendingRetention once its readers are being consumed.
func (nmgr *NatsManager) PendingRetentionMigrations() []string {
	nmgr.streamMu.Lock()
	defer nmgr.streamMu.Unlock()
	out := make([]string, 0, len(nmgr.retentionPending))
	for suffix := range nmgr.retentionPending {
		out = append(out, suffix)
	}
	slices.Sort(out)
	return out
}

// MigratePendingRetention drains and recreates every stream ensureStream found holding an
// older retention than declared. It returns nil when there is none. It must be called while
// the stream's reader is consuming, or the wait cannot end; it is bounded, and an error means
// the stream was NOT recreated by this call.
func (nmgr *NatsManager) MigratePendingRetention(ctx context.Context) error {
	for _, suffix := range nmgr.PendingRetentionMigrations() {
		nmgr.streamMu.Lock()
		name := nmgr.retentionPending[suffix]
		nmgr.streamMu.Unlock()
		if err := nmgr.migrateRetention(ctx, suffix, name); err != nil {
			return err
		}
		nmgr.streamMu.Lock()
		delete(nmgr.retentionPending, suffix)
		nmgr.streamMu.Unlock()
	}
	return nil
}

func (nmgr *NatsManager) migrateRetention(ctx context.Context, suffix, name string) error {
	want := retentionPolicy(streams.RetentionFor(suffix))
	bound, poll := retentionDrainBound, retentionDrainPoll
	if nmgr.retentionDrainBound > 0 {
		bound = nmgr.retentionDrainBound
	}
	if nmgr.retentionDrainPoll > 0 {
		poll = nmgr.retentionDrainPoll
	}
	durable := nmgr.ownDurable(suffix)
	// The broker administrator runs once, from a hook Job that Helm serialises, and holds no
	// publish right on any bucket, so it takes no lock; a manager that is not the administrator
	// (a test's) still serialises its replicas with the distributed lock.
	withLock := func(ctx context.Context, fn func(context.Context) error) error { return fn(ctx) }
	if !nmgr.admin {
		lock, err := nmgr.NewDistributedLock(retentionLockTTL)
		if err != nil {
			return fmt.Errorf("messaging: opening the lock for the retention change of %s: %w", name, err)
		}
		withLock = func(ctx context.Context, fn func(context.Context) error) error {
			return lock.WithLock(ctx, "stream-retention:"+name, fn)
		}
	}
	log.Info().Str("stream", name).Str("durable", durable).Dur("bound", bound).
		Msg("Waiting for a stream to drain before recreating it with its declared retention")
	deadline := time.Now().Add(bound)
	nextLog := time.Now().Add(retentionLogEvery)
	for {
		// Wait, outside the lock, for the stream to drain.
		var drained *nats.StreamInfo
		for {
			info, ok, why, err := nmgr.streamDrained(ctx, name, durable)
			if err != nil {
				return fmt.Errorf("messaging: checking whether stream %s has drained: %w", name, err)
			}
			if info != nil && info.Config.Retention == want {
				return nil // another replica has already moved it
			}
			if ok {
				drained = info
				break
			}
			if time.Now().After(deadline) {
				if info == nil {
					return fmt.Errorf("messaging: stream %s was still missing after %s: it was deleted, and not "+
						"recreated, while this replica waited to migrate it; refusing to start", name, bound)
				}
				return fmt.Errorf("messaging: stream %s still holds unforwarded messages after %s (%s); refusing to "+
					"start rather than delete them. It has to be emptied, by its reader or by an operator, before "+
					"its retention can change", name, bound, why)
			}
			if !time.Now().Before(nextLog) {
				log.Warn().Str("stream", name).Str("durable", durable).Str("waiting", why).
					Msg("Still waiting for a stream to drain before recreating it with its declared retention")
				nextLog = time.Now().Add(retentionLogEvery)
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(poll):
			}
		}
		moved := false
		err := withLock(ctx, func(ctx context.Context) error {
			// Everything is read again under the lock: another replica may have finished while
			// this one waited for it.
			cur, ok, _, err := nmgr.streamDrained(ctx, name, durable)
			if err != nil {
				return err
			}
			if cur != nil && cur.Config.Retention == want {
				moved = true
				return nil
			}
			if !ok || cur == nil || !cur.Created.Equal(drained.Created) {
				return nil // not the stream that was drained, or no longer drained: wait again
			}
			cfg := cur.Config
			cfg.Retention = want
			cfg.FirstSeq = cur.State.LastSeq + 1 + retentionSeqMargin
			if err := nmgr.js.DeleteStream(name); err != nil && !errors.Is(err, nats.ErrStreamNotFound) {
				return fmt.Errorf("messaging: deleting drained stream %s: %w", name, err)
			}
			if err := nmgr.addStreamWithRetry(ctx, &cfg); err != nil {
				return err
			}
			moved = true
			nmgr.noteReconciled("stream", name, "migrated")
			log.Info().Str("stream", name).Str("retention", cfg.Retention.String()).Uint64("firstSeq", cfg.FirstSeq).
				Msg("Recreated a drained stream with its declared retention")
			return nil
		})
		if err != nil {
			return err
		}
		if moved {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("messaging: stream %s could not be recreated within %s: it kept changing under the "+
				"lock; refusing to start", name, bound)
		}
	}
}

// addStreamWithRetry creates a stream the caller has just deleted. Until it succeeds there is
// no stream, so a broker timeout or a leader change is retried rather than returned. A create
// that fails but finds the stream there with the config asked for is a success: the earlier
// attempt landed and only its answer was lost.
func (nmgr *NatsManager) addStreamWithRetry(ctx context.Context, cfg *nats.StreamConfig) error {
	var last error
	for attempt := 0; attempt < retentionAddAttempts; attempt++ {
		_, err := nmgr.js.AddStream(cfg, nats.Context(ctx))
		if err == nil {
			return nil
		}
		if info, ierr := nmgr.js.StreamInfo(cfg.Name, nats.Context(ctx)); ierr == nil && info.Config.Retention == cfg.Retention {
			return nil
		}
		last = err
		log.Warn().Err(err).Str("stream", cfg.Name).Int("attempt", attempt+1).
			Msg("Could not create the stream after deleting it; retrying")
		select {
		case <-ctx.Done():
			return fmt.Errorf("messaging: recreating stream %s after deleting it: %w (last error: %v)", cfg.Name, ctx.Err(), last)
		case <-time.After(nmgr.addBackoff()):
		}
	}
	return fmt.Errorf("messaging: recreating stream %s after deleting it: %w", cfg.Name, last)
}

func (nmgr *NatsManager) addBackoff() time.Duration {
	if nmgr.retentionAddBackoff > 0 {
		return nmgr.retentionAddBackoff
	}
	return retentionAddBackoff
}

// ownDurable is the durable this service reads suffix's stream with. The broker administrator
// reads none itself: the stream is drained by the area that declares the durable reader, so it
// asks that reader's durable, the one the area's own start binds.
func (nmgr *NatsManager) ownDurable(suffix string) string {
	if nmgr.admin {
		for _, r := range streams.ReadersOf(suffix) {
			if r.Mode == streams.ReadDurable {
				return DurableName(nmgr.Microservice.InstanceId, r.Area, suffix)
			}
		}
	}
	nmgr.streamMu.Lock()
	defer nmgr.streamMu.Unlock()
	for _, r := range nmgr.readers {
		if r.suffix == suffix {
			return r.durable
		}
	}
	return DurableName(nmgr.Microservice.InstanceId, nmgr.Microservice.FunctionalArea, suffix)
}

// streamDrained reports whether nothing on the stream is waiting for THIS service's durable
// to read or ack it: the durable has no pending message and none in flight, and has acked
// through the stream's last sequence. Only that durable is asked. A consumer another service
// or an earlier build left behind is not this service's work and cannot hold it up; it goes
// with the stream when the stream is recreated. A stream that holds messages and has no
// durable of this service's to say they were read is NOT drained. why names what is
// outstanding when it is not. A stream that does not exist (another replica is between its
// delete and its create) is not drained either, and is not an error. That holds for either
// read: the stream and its durable are two requests, and another replica's delete can land
// between them, so the durable's read finding no stream is the same answer as the stream's.
func (nmgr *NatsManager) streamDrained(ctx context.Context, name, durable string) (info *nats.StreamInfo, drained bool, why string, err error) {
	const gone = "the stream does not exist (being recreated by another replica)"
	info, err = nmgr.js.StreamInfo(name, nats.Context(ctx))
	if errors.Is(err, nats.ErrStreamNotFound) {
		return nil, false, gone, nil
	}
	if err != nil {
		return nil, false, "", err
	}
	ci, err := nmgr.js.ConsumerInfo(name, durable, nats.Context(ctx))
	switch {
	case errors.Is(err, nats.ErrStreamNotFound):
		// The info read above describes a stream that is no longer there, so it is not
		// returned: the answer is exactly the first read's not-found, and no caller is left
		// holding a stream that does not exist.
		return nil, false, gone, nil
	case errors.Is(err, nats.ErrConsumerNotFound):
		if info.State.Msgs > 0 {
			return info, false, fmt.Sprintf("%d messages and no durable %s", info.State.Msgs, durable), nil
		}
		return info, true, "", nil
	case err != nil:
		return info, false, "", err
	}
	if ci.NumPending != 0 || ci.NumAckPending != 0 || ci.AckFloor.Stream < info.State.LastSeq {
		return info, false, fmt.Sprintf("durable %s: %d pending, %d awaiting ack", durable, ci.NumPending, ci.NumAckPending), nil
	}
	return info, true, "", nil
}
