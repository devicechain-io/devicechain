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
// its sources running it calls MigratePendingRetention. That waits until every consumer on
// the stream has nothing pending and nothing awaiting an ack, then deletes the stream and
// creates it again with the declared retention. The reader finds its durable gone and binds
// a new one on the recreated stream, the same self-heal it uses for any lost consumer.
//
// If the stream does not drain within the bound, the call fails, and the service refuses to
// start rather than run on a stream that does not behave as declared.
//
// # What it keeps
//
// The recreated stream starts at the old stream's next sequence. Sequences are not cosmetic
// here: the capture sequence is the basis of the dedup id on the forwarded event, so a
// sequence restarting at 1 would collide with ids still inside the destination stream's
// duplicate window and silently suppress new events.
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
)

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
// the stream was NOT recreated.
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
	bound, poll := retentionDrainBound, retentionDrainPoll
	if nmgr.retentionDrainBound > 0 {
		bound = nmgr.retentionDrainBound
	}
	if nmgr.retentionDrainPoll > 0 {
		poll = nmgr.retentionDrainPoll
	}
	deadline := time.Now().Add(bound)
	var info *nats.StreamInfo
	for {
		var drained bool
		var why string
		var err error
		info, drained, why, err = nmgr.streamDrained(ctx, name)
		if err != nil {
			return fmt.Errorf("messaging: checking whether stream %s has drained: %w", name, err)
		}
		if drained {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("messaging: stream %s still holds unforwarded messages after %s (%s); refusing to "+
				"start rather than delete them. It has to be emptied, by its reader or by an operator, before "+
				"its retention can change", name, bound, why)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}
	cfg := info.Config
	cfg.Retention = retentionPolicy(streams.RetentionFor(suffix))
	// Continue the sequence: see "What it keeps".
	cfg.FirstSeq = info.State.LastSeq + 1
	if err := nmgr.js.DeleteStream(name); err != nil && !errors.Is(err, nats.ErrStreamNotFound) {
		return fmt.Errorf("messaging: deleting drained stream %s: %w", name, err)
	}
	if _, err := nmgr.js.AddStream(&cfg); err != nil {
		return fmt.Errorf("messaging: recreating stream %s as %s after deleting it: %w", name, cfg.Retention, err)
	}
	log.Info().Str("stream", name).Str("retention", cfg.Retention.String()).Uint64("firstSeq", cfg.FirstSeq).
		Msg("Recreated a drained stream with its declared retention")
	return nil
}

// streamDrained reports whether nothing on the stream is waiting to be read or acked: every
// consumer has no pending message and no message in flight, and has acked through the
// stream's last sequence. A stream that holds messages and has no consumer to say they were
// read is NOT drained. why names what is outstanding when it is not.
func (nmgr *NatsManager) streamDrained(ctx context.Context, name string) (info *nats.StreamInfo, drained bool, why string, err error) {
	info, err = nmgr.js.StreamInfo(name, nats.Context(ctx))
	if err != nil {
		return nil, false, "", err
	}
	consumers := 0
	for ci := range nmgr.js.Consumers(name, nats.Context(ctx)) {
		consumers++
		if ci.NumPending != 0 || ci.NumAckPending != 0 || ci.AckFloor.Stream < info.State.LastSeq {
			return info, false, fmt.Sprintf("consumer %s: %d pending, %d awaiting ack", ci.Name, ci.NumPending, ci.NumAckPending), nil
		}
	}
	if consumers == 0 && info.State.Msgs > 0 {
		return info, false, fmt.Sprintf("%d messages and no consumer", info.State.Msgs), nil
	}
	return info, true, "", nil
}
