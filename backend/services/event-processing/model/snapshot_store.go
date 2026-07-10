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

// Reset deletes a partition's checkpoint so the next Save starts fresh. It is the
// deliberate backward move Save refuses: used only when the stream is found to be
// behind the snapshot (a re-created/truncated stream), where the stale row must be
// cleared rather than preserved by the monotonic guard.
func (s *SnapshotStore) Reset(ctx context.Context, partitionId string) error {
	return s.rdb.DB(ctx).Where("partition_id = ?", partitionId).Delete(&DetectSnapshot{}).Error
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
