// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"time"

	"github.com/devicechain-io/dc-microservice/rdb"
)

// Why a profile's active version changed.
const (
	ActivationKindPublish  = "publish"
	ActivationKindRollback = "rollback"
)

// DeviceProfileActivation is one entry of a profile's append-only activation log: the
// instant a version BECAME the one devices resolve. A publish writes one (for the version it
// mints) and a rollback writes one (for the version it re-points to), each in the same
// transaction as the move of DeviceProfile.ActiveVersion, so the log and the pointer cannot
// disagree.
//
// It answers "which version was active at T", which nothing else can: ActiveSince holds only
// the latest activation and DeviceProfileVersion.CreatedAt is the publish instant, wrong for
// any version a rollback later re-activated. The version that was active at T is the entry
// with the greatest ActivatedAt not after T.
//
// The log starts when the migration that created it ran. A profile last activated before
// that has no entry for it, and a reader must read that absence as "unknown".
//
// Activation is per profile; every device type that uses the profile follows it.
//
// ActivatedAt is the same instant stored as DeviceProfile.ActiveSince and carried by the
// detection-rules-published fact (see nextActivationInstant), not a second clock reading.
type DeviceProfileActivation struct {
	ID uint `gorm:"primarykey"`
	rdb.TenantScoped

	DeviceProfileId uint  `gorm:"not null;index:ix_device_profile_activations_profile_id,priority:1"`
	Version         int32 `gorm:"not null"`

	// ActivationKindPublish or ActivationKindRollback.
	Kind        string    `gorm:"not null;size:16"`
	ActivatedAt time.Time `gorm:"not null"`
	// The identity that caused it: username, falling back to email. Empty when unknown.
	ActivatedBy string `gorm:"size:256"`
}

// TableName is NOT declared: a TableName() bypasses this area's TablePrefix (see
// schema/migration_list_order_indexes.go), and the type name derives device_profile_activations.

// AuditExempt opts the log out of the audit journal: each row restates a publish or a
// rollback the journal already records on the version and the profile.
func (DeviceProfileActivation) AuditExempt() bool { return true }
