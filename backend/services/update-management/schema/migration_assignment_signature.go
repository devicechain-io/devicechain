// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"database/sql"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// AssignmentSignatureID is the pinned-signature migration's frozen ID.
const AssignmentSignatureID = "20261013000200"

// NewAssignmentSignatureSchema appends update_assignments.signature_id: the signature an
// assignment was made under, pinned when the assignment is created. It is nullable because an
// assignment made under the instance's allowUnverifiedArtifacts flag with no signature by an
// active signer carries none, and it is not a foreign key because signatures are never deleted.
//
// Pinning is what keeps a revoke from changing the wire under an attempt already in flight: the
// message a device is sent (and re-sent on a sync) names the pinned signature, not whichever
// signature is currently trusted.
//
// The snapshot is declared INSIDE the function and named updateAssignment, for the reason given
// on NewUploadLeaseSchema: gorm derives the table name from the type's name, and the
// package-level updateAssignment is the assignments migration's frozen shape. AutoMigrate on
// this partial struct adds only the missing column, which also makes the migration re-runnable.
func NewAssignmentSignatureSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: AssignmentSignatureID,
		Migrate: func(tx *gorm.DB) error {
			type updateAssignment struct {
				ID          uint `gorm:"primarykey"`
				SignatureId sql.NullInt64
			}
			return tx.AutoMigrate(&updateAssignment{})
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Migrator().DropColumn("update_assignments", "signature_id")
		},
	}
}
