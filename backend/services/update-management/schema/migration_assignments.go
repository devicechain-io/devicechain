// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// AssignmentsID is the assignments-and-attempts migration's frozen ID. It is recorded in every
// database that ran it, so it never changes.
const AssignmentsID = "20261011000100"

// updateAssignment is the snapshot of the update_assignments table: the append-only history of
// which artifact a device was told to install. A row is never updated except to stamp
// SupersededAt when a newer assignment replaces it, and never deleted.
//
// Self-contained down to the mixins, like every snapshot here. It has no TableName method for the
// reason given on artifact: the area's table prefix applies only to a DERIVED name, and renaming
// this type retargets the migration.
type updateAssignment struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time

	TenantId string `gorm:"index;not null;size:128"`

	Token       string `gorm:"not null;size:128"`
	DeviceToken string `gorm:"not null;size:128;index"`
	Component   string `gorm:"not null;size:64"`
	ArtifactId  uint   `gorm:"not null;index"`
	Actor       string `gorm:"size:256"`

	SupersededAt sql.NullTime
}

// updateAttempt is the snapshot of the update_attempts table: one try at moving one component of
// one device to an assignment's artifact, exactly the value the core update reducer folds
// reports into (ota.Attempt), flattened to columns.
//
// Two columns exist only because several replicas write it. Version is an optimistic-concurrency
// counter: a report, a deadline tick and a cancel each read the row, compute the next state with
// the pure reducer, and write it back only if Version is still what they read. LastSeq is the
// device's report sequence, stored as a bigint (a sequence past 2^63 is not a real device).
type updateAttempt struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time

	TenantId string `gorm:"index;not null;size:128"`

	Token        string `gorm:"not null;size:128"`
	AssignmentId uint   `gorm:"not null"`
	DeviceToken  string `gorm:"not null;size:128"`
	Component    string `gorm:"not null;size:64"`

	TargetVersion  string `gorm:"not null;size:64"`
	TargetDigest   string `gorm:"not null;size:71"`
	RequiresReboot bool   `gorm:"not null"`

	State        string    `gorm:"not null;size:16"`
	Version      int64     `gorm:"not null"`
	LastSeq      int64     `gorm:"not null"`
	LastReportAt time.Time `gorm:"not null"`

	BootIdAtStart   string `gorm:"not null;size:128"`
	InstallBootId   string `gorm:"not null;size:128"`
	LastBootId      string `gorm:"not null;size:128"`
	LastStage       string `gorm:"not null;size:16"`
	CancelRequested bool   `gorm:"not null"`
	Reconciled      bool   `gorm:"not null"`

	ProgressBytes sql.NullInt64
	ProgressTotal sql.NullInt64

	FailureStage    string `gorm:"not null;size:16"`
	FailureCode     string `gorm:"not null;size:64"`
	FailureDetail   string `gorm:"not null;size:512"`
	FailurePlatform bool   `gorm:"not null"`
}

// NewAssignmentsSchema appends the assignment history and the attempt record.
//
// It is individually re-runnable (migrations run with UseTransaction:false and replay from the top
// after a failure): AutoMigrate adds only what is missing and every index is CREATE ... IF NOT
// EXISTS.
//
// Four unique indexes carry the invariants the code leans on:
//   - (tenant_id, token) on both tables: the lookup key a wire message names a row by;
//   - one CURRENT assignment per (tenant_id, device_token, component), WHERE superseded_at IS NULL:
//     a device is working toward exactly one target per component, and a second concurrent
//     assign loses on this index rather than on a check somebody could forget;
//   - one attempt per assignment.
//
// Index names and predicates are written out here against the snapshot, deliberately not through a
// helper in core: a name and a WHERE are schema, and one sourced from another module starts
// building something different on fresh installs the day that module changes.
func NewAssignmentsSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: AssignmentsID,
		Migrate: func(tx *gorm.DB) error {
			if err := tx.AutoMigrate(&updateAssignment{}, &updateAttempt{}); err != nil {
				return err
			}
			for _, ix := range []struct {
				model     any
				name      string
				columns   []string
				predicate string
			}{
				{&updateAssignment{}, "uix_update_assignments_tenant_token", []string{"tenant_id", "token"}, ""},
				{&updateAssignment{}, "uix_update_assignments_current", []string{"tenant_id", "device_token", "component"}, "superseded_at IS NULL"},
				{&updateAttempt{}, "uix_update_attempts_tenant_token", []string{"tenant_id", "token"}, ""},
				{&updateAttempt{}, "uix_update_attempts_assignment", []string{"assignment_id"}, ""},
			} {
				if err := createUniqueIndex(tx, ix.model, ix.name, ix.predicate, ix.columns...); err != nil {
					return err
				}
			}
			// The deadline sweep reads the attempts whose last report is old, by state.
			return createIndex(tx, &updateAttempt{}, "ix_update_attempts_state_report", "state", "last_report_at")
		},
		Rollback: func(tx *gorm.DB) error {
			if err := tx.Migrator().DropTable("update_attempts"); err != nil {
				return err
			}
			return tx.Migrator().DropTable("update_assignments")
		},
	}
}

// indexParts parses the model and quotes the table, the index name and the column list the way
// the dialect wants them.
func indexParts(tx *gorm.DB, model any, name string, columns []string) (table, quotedName, cols string, err error) {
	stmt := &gorm.Statement{DB: tx}
	if err := stmt.Parse(model); err != nil {
		return "", "", "", fmt.Errorf("parse model for index %s: %w", name, err)
	}
	quoted := make([]string, len(columns))
	for i, c := range columns {
		quoted[i] = stmt.Quote(c)
	}
	return stmt.Quote(stmt.Table), stmt.Quote(name), strings.Join(quoted, ", "), nil
}

func createUniqueIndex(tx *gorm.DB, model any, name, predicate string, columns ...string) error {
	table, qname, cols, err := indexParts(tx, model, name, columns)
	if err != nil {
		return err
	}
	where := ""
	if predicate != "" {
		where = " WHERE " + predicate
	}
	return tx.Exec(fmt.Sprintf("CREATE UNIQUE INDEX IF NOT EXISTS %s ON %s (%s)%s", qname, table, cols, where)).Error
}

func createIndex(tx *gorm.DB, model any, name string, columns ...string) error {
	table, qname, cols, err := indexParts(tx, model, name, columns)
	if err != nil {
		return err
	}
	return tx.Exec(fmt.Sprintf("CREATE INDEX IF NOT EXISTS %s ON %s (%s)", qname, table, cols)).Error
}
