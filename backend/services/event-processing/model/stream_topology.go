// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrShardingDetectionHistory     = errors.New("event-processing: sharding resolved-events on an instance with detection history is not supported in this release; enable it on a fresh install; a fenced cutover is a future capability")
	ErrDetectStreamTopologyMismatch = errors.New("event-processing: changing or disabling resolved-events sharding is unsupported")
)

// DetectStreamTopology is instance metadata, never a purge checkpoint partition.
// No tenant identifiers, payloads or historical events belong in this row.
type DetectStreamTopology struct {
	ID            string    `gorm:"column:id;type:varchar(32);primaryKey;check:detect_stream_topology_singleton,id = 'singleton'"`
	BaseSuffix    string    `gorm:"column:base_suffix;type:varchar(128);not null"`
	GroupCount    int       `gorm:"column:group_count;type:integer;not null"`
	CreatedAt     time.Time `gorm:"column:created_at;not null"`
	FormatVersion int       `gorm:"column:format_version;type:integer;not null"`
}

func (DetectStreamTopology) TableName() string { return "detect_stream_topology" }

// EnsureStreamTopology must be called under the singleton lease, after the
// predecessor has stopped and before restoring or consuming any DETECT input.
// The history check and first topology write commit in one transaction.
func (s *SnapshotStore) EnsureStreamTopology(ctx context.Context, suffix string, groups int, sharded bool) error {
	return s.rdb.DB(ctx).Transaction(func(tx *gorm.DB) error {
		var row DetectStreamTopology
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", "singleton").Take(&row).Error
		if err == nil {
			if !sharded || row.BaseSuffix != suffix || row.GroupCount != groups || row.FormatVersion != 1 {
				return fmt.Errorf("%w: recorded suffix=%q groups=%d format=%d; configured suffix=%q groups=%d sharded=%t",
					ErrDetectStreamTopologyMismatch, row.BaseSuffix, row.GroupCount, row.FormatVersion, suffix, groups, sharded)
			}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if !sharded {
			return nil
		}
		floor, err := lockSnapshotFloor(tx, "singleton")
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if floor.StreamSeq > 0 {
			return ErrShardingDetectionHistory
		}
		return tx.Create(&DetectStreamTopology{ID: "singleton", BaseSuffix: suffix,
			GroupCount: groups, CreatedAt: time.Now().UTC(), FormatVersion: 1}).Error
	})
}
