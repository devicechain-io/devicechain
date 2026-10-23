// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"database/sql"
	"time"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// RolloutsID is the group-rollout migration's frozen ID. It is recorded in every database that
// ran it, so it never changes.
const RolloutsID = "20261023000100"

// updateRollout is the snapshot of the update_rollouts table: one operator decision to move a
// group of devices to an artifact, a bounded number at a time.
//
// Self-contained down to the mixins, like every snapshot here, and with no TableName method for
// the reason given on artifact: the area's table prefix applies only to a DERIVED name.
type updateRollout struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time

	TenantId string `gorm:"index;not null;size:128"`

	Token      string `gorm:"not null;size:128"`
	Name       string `gorm:"not null;size:128"`
	ArtifactId uint   `gorm:"not null;index"`
	GroupToken string `gorm:"not null;size:128"`
	// GroupVersion is the published version of a dynamic group the start resolved; null for a
	// static group and for a rollout that has not started.
	GroupVersion     sql.NullInt32
	MaxInFlight      int `gorm:"not null"`
	FailureThreshold sql.NullInt32
	State            string `gorm:"not null;size:16"`
	PauseReason      string `gorm:"not null;size:32"`
	TargetCount      int    `gorm:"not null"`
	CreatedBy        string `gorm:"size:256"`
	StartedAt        sql.NullTime
	FinishedAt       sql.NullTime
	StateChangedBy   string `gorm:"size:256"`
	StateChangedAt   sql.NullTime
}

// updateRolloutTarget is the snapshot of the update_rollout_targets table: one device of a
// rollout's frozen target set, and what the rollout decided about it. The device's OUTCOME is not
// stored here: it is the attempt row the assignment names.
type updateRolloutTarget struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time

	TenantId string `gorm:"index;not null;size:128"`

	RolloutId    uint   `gorm:"not null"`
	DeviceToken  string `gorm:"not null;size:128"`
	Status       string `gorm:"not null;size:16"`
	AssignmentId sql.NullInt64
	DispatchedAt sql.NullTime
	SkipCode     string `gorm:"not null;size:64"`
	SkipDetail   string `gorm:"not null;size:512"`
}

// NewRolloutsSchema appends the rollout and its target set.
//
// It is individually re-runnable (migrations run with UseTransaction:false and replay from the top
// after a failure): AutoMigrate adds only what is missing and every index is CREATE ... IF NOT
// EXISTS.
//
// The unique indexes carry the invariants the code leans on: a rollout's token is unique within a
// tenant, and a device appears at most once in a rollout's target set. The (rollout_id, status, id)
// index serves the driver, which reads a rollout's PENDING targets oldest first and counts the
// DISPATCHED ones on every step.
func NewRolloutsSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: RolloutsID,
		Migrate: func(tx *gorm.DB) error {
			if err := tx.AutoMigrate(&updateRollout{}, &updateRolloutTarget{}); err != nil {
				return err
			}
			for _, ix := range []struct {
				model   any
				name    string
				columns []string
			}{
				{&updateRollout{}, "uix_update_rollouts_tenant_token", []string{"tenant_id", "token"}},
				{&updateRolloutTarget{}, "uix_update_rollout_targets_rollout_device", []string{"rollout_id", "device_token"}},
			} {
				if err := createUniqueIndex(tx, ix.model, ix.name, "", ix.columns...); err != nil {
					return err
				}
			}
			if err := createIndex(tx, &updateRolloutTarget{}, "ix_update_rollout_targets_rollout_status",
				"rollout_id", "status", "id"); err != nil {
				return err
			}
			// The driver lists RUNNING rollouts across tenants on every pass.
			return createIndex(tx, &updateRollout{}, "ix_update_rollouts_state", "state")
		},
		Rollback: func(tx *gorm.DB) error {
			if err := tx.Migrator().DropTable("update_rollout_targets"); err != nil {
				return err
			}
			return tx.Migrator().DropTable("update_rollouts")
		},
	}
}
