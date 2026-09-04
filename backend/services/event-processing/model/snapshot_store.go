// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"

	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrStaleCheckpoint is returned by Save when it refuses to move the durable stream
// sequence backward (a lagging split-brain writer). The loop treats it like any
// commit failure — it does NOT ack the buffered messages — so those events redeliver
// (and are picked up by the engine that legitimately owns the higher sequence, or on
// the post-split replay) rather than being acked-and-dropped.
var ErrStaleCheckpoint = errors.New("event-processing: refusing to move DETECT checkpoint backward")

// ErrResetRaced is returned by Reset when the row has moved since the caller read it — a
// later owner committed a checkpoint while this one was still building its term. The
// caller's premise (the stream head is behind THIS snapshot) was formed against a row that
// no longer exists, so the delete is refused and the term build fails, which is the outcome
// the term-build fuse already handles.
var ErrResetRaced = errors.New("event-processing: refusing to reset a DETECT checkpoint that " +
	"another owner has advanced since it was read")

// SnapshotStore persists and restores the DETECT engine's per-partition checkpoint
// (ADR-051). It is the durable half of the correctness spine: Save commits the
// engine state and its last-applied stream sequence atomically, and the loop acks
// JetStream only after Save returns — so a crash never leaves an acked message whose
// effect is not durable.
type SnapshotStore struct {
	rdb *rdb.RdbManager
}

// NewSnapshotStore wraps the rdb manager.
func NewSnapshotStore(r *rdb.RdbManager) *SnapshotStore {
	return &SnapshotStore{rdb: r}
}

// Save commits the snapshot for its partition, MONOTONICALLY: it refuses to move the
// durable stream sequence backward. The row is locked FOR UPDATE and compared before
// the write, so two engines briefly co-bound to one partition — the split-brain a
// rolling update can create before the Slice-6 singleton deploy lands — cannot let a
// lagging writer's lower-sequence checkpoint clobber a higher one (which would strand
// acked events whose effects are then in neither the surviving snapshot nor
// redeliverable). A stale write is refused with ErrStaleCheckpoint (the caller then leaves
// its messages unacked and halts the losing writer). State, watermark, and sequence live in
// one row, so the write is atomic.
func (s *SnapshotStore) Save(ctx context.Context, snap *DetectSnapshot) error {
	return s.rdb.DB(ctx).Transaction(func(tx *gorm.DB) error {
		var existing DetectSnapshot
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("partition_id = ?", snap.PartitionId).First(&existing).Error
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			return tx.Create(snap).Error
		case err != nil:
			return err
		}
		if snap.StreamSeq < existing.StreamSeq {
			log.Warn().Str("partition", snap.PartitionId).
				Int64("existingSeq", existing.StreamSeq).Int64("attemptedSeq", snap.StreamSeq).
				Msg("Refusing to move DETECT checkpoint backward (stale/split-brain writer); leaving messages unacked to redeliver.")
			return ErrStaleCheckpoint
		}
		if snap.StreamSeq == existing.StreamSeq && snap.Watermark.Before(existing.Watermark) {
			// Equal sequence but a LOWER watermark. This is a split-brain peer trying to move the
			// logical clock backward: a co-writer that idle-advanced its watermark off the wall
			// clock (ADR-051 slice 4c) commits at the shared sequence, and the other, lagging on
			// event time, would otherwise overwrite that higher frontier with its lower one —
			// re-opening windows/timers the first writer already closed, so a replay diverges. A
			// single writer's watermark is monotonic, so this only ever fires on a split brain.
			log.Warn().Str("partition", snap.PartitionId).Int64("seq", snap.StreamSeq).
				Time("existingWatermark", existing.Watermark).Time("attemptedWatermark", snap.Watermark).
				Msg("Refusing to move DETECT watermark backward at an equal sequence (split-brain writer).")
			return ErrStaleCheckpoint
		}
		// Force every field (a map, not the struct, so a legitimate StreamSeq==0 after
		// an engine reset is not skipped as a zero value); gorm sets updated_at.
		return tx.Model(&DetectSnapshot{}).Where("partition_id = ?", snap.PartitionId).
			Updates(map[string]interface{}{
				"stream_seq": snap.StreamSeq,
				"watermark":  snap.Watermark,
				"payload":    snap.Payload,
			}).Error
	})
}

// Reset deletes a partition's checkpoint so the next Save starts fresh, COMPARE-AND-SWAP
// against the row the caller actually read. It is the deliberate backward move Save
// refuses: used only when the stream is found to be behind the snapshot (a re-created or
// truncated stream), where the stale row must be cleared rather than preserved by the
// monotonic guard.
//
// observedSeq is the StreamSeq of the row the caller restored from — the state its whole
// decision was made against. If the row now carries a different sequence, a later owner
// has committed since, the caller's premise is gone, and the delete is refused with
// ErrResetRaced rather than applied.
//
// 🔴 WITHOUT THE COMPARE THIS IS THE ONE UNGUARDED WRITE IN THE STORE, and a warm standby
// (ADR-070) is what makes it reachable. Save refuses a backward move on both axes it
// guards, sequence and watermark; Reset was an unconditional DELETE, issued from inside a
// TERM BUILD — after the lease is acquired but before the term is live — which is a long
// operation (a snapshot restore, three view builds and a whole replay) and therefore
// exactly the place a lease is lost.
// The losing replica then deletes the checkpoint its successor is already leading from,
// and what goes with the row is not just bytes: it is the monotonic FLOOR every other
// split-brain guard in this file compares against.
//
// 🔴 IT COMPARES THE OBSERVED SEQUENCE RATHER THAN AN OWNERSHIP EPOCH, and that is a
// measured choice, not a shortcut around a schema change. An epoch column would only help
// if a term STAMPED it before doing anything else — and a term has not earned a checkpoint
// at that point, so stamping one would be writing a claim it cannot yet support. Against
// the successor that has not checkpointed yet, an epoch column refuses exactly the same
// deletes this does and no more; against the one that HAS, both refuse. What the sequence
// buys over the epoch is that it needs no new column and no migration, and it is the value
// the caller genuinely read.
//
// The residual is honest and small: a successor that has taken over and NOT yet committed
// anything leaves the row exactly as the loser saw it, so the loser's delete still lands.
// The successor holds that state in memory and re-Creates the row on its next checkpoint;
// what is briefly lost is the monotonic floor, not the state. Closing that too would mean
// a write at term start, which is the trade described above.
func (s *SnapshotStore) Reset(ctx context.Context, partitionId string, observedSeq int64) error {
	return s.rdb.DB(ctx).Transaction(func(tx *gorm.DB) error {
		var existing DetectSnapshot
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("partition_id = ?", partitionId).First(&existing).Error
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			// Nothing to clear. Idempotent rather than an error: replayToHead can reach this
			// after a peer cleared the same stale row for the same reason.
			return nil
		case err != nil:
			return err
		}
		if existing.StreamSeq != observedSeq {
			log.Warn().Str("partition", partitionId).
				Int64("observedSeq", observedSeq).Int64("currentSeq", existing.StreamSeq).
				Msg("Refusing to reset a DETECT checkpoint that has moved since it was read; another owner has committed to this partition.")
			return ErrResetRaced
		}
		return tx.Where("partition_id = ? AND stream_seq = ?", partitionId, observedSeq).
			Delete(&DetectSnapshot{}).Error
	})
}

// LoadCommittedSeq returns just the committed stream sequence for a partition (ok=false when
// none exists), reading only the sequence column — NOT the potentially large state payload.
// The idle-advance split-brain fence (detectStaleOwner) calls it every idle cycle purely to
// compare one integer, so pulling the whole engine snapshot would be wasteful.
func (s *SnapshotStore) LoadCommittedSeq(ctx context.Context, partitionId string) (int64, bool, error) {
	var row struct{ StreamSeq int64 }
	err := s.rdb.DB(ctx).Model(&DetectSnapshot{}).Select("stream_seq").
		Where("partition_id = ?", partitionId).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return row.StreamSeq, true, nil
}

// Load returns the durable checkpoint for a partition, or ok=false when none exists
// yet (a fresh Instance) so the caller starts from an empty engine and the stream's
// first message. A real error (not the not-found sentinel) is returned as-is.
func (s *SnapshotStore) Load(ctx context.Context, partitionId string) (*DetectSnapshot, bool, error) {
	var snap DetectSnapshot
	err := s.rdb.DB(ctx).Where("partition_id = ?", partitionId).First(&snap).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return &snap, true, nil
}
