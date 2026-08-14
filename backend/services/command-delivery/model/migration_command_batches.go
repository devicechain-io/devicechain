// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

// Like baseline.go, this file imports none of the service's own packages. A migration that
// can reach a live model, a live vocabulary or a live validator is a migration whose
// behaviour changes when those do.
import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// commandBatch is the SNAPSHOT of the batch record this migration creates, captured at the
// point in time it was written. It is private to this package and is NOT the live model.
//
// It follows every rule baseline_snapshot.go states, for the reasons stated there: the
// mixins are INLINED rather than embedded, so a change in backend/core cannot silently
// rewrite this migration; field order matches the embedded layout, because physical column
// order is part of the schema; and it will diverge from the live model over time, which is
// the point rather than a defect.
//
// 🔴 IT HAS NO TableName METHOD, and — as with `command` — that is load-bearing. core/rdb
// pins the gorm NamingStrategy's TablePrefix to the functional area, and that prefix
// applies only when gorm DERIVES a table name. An explicit TableName bypasses it and the
// indexes come out unprefixed. Gorm derives "command_batches" from this type name, so
// RENAMING THE TYPE RETARGETS THE MIGRATION. Leave both the absence and the name alone.
type commandBatch struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`

	TenantId string `gorm:"index;not null;size:128"`

	Token string `gorm:"index;not null;size:128"`

	Metadata *datatypes.JSON

	// The command every member of the batch receives. One key, one payload — that is what
	// makes the enqueue gate affordable at fleet scale, and it is why these are columns on
	// the batch rather than being read back off its commands.
	Name    string `gorm:"not null;size:128"`
	Payload *datatypes.JSON

	// How the target set was named: DEVICE_LIST or GROUP.
	TargetKind string `gorm:"not null;size:16"`
	// The group targeted, and the FROZEN version actually resolved. Both null for a
	// device list. The version is stored because it is the only thing that can answer
	// "what did this group MEAN when the batch fired?" after someone edits the selector.
	GroupToken   sql.NullString `gorm:"size:128"`
	GroupVersion sql.NullInt32

	// Whether the caller accepted a partial fan-out.
	AllowPartial bool `gorm:"not null"`

	// Resolved is how many distinct devices the target resolved to; Accepted is how many
	// commands were actually written. Both are STORED FACTS about the moment the batch
	// fired, not live queries: command rows are not immortal (soft delete, tenant purge),
	// so a derived count drifts below the creation-time truth with no refusal explaining
	// the gap, and `resolved = accepted + refusals` quietly stops holding.
	Resolved int `gorm:"not null"`
	Accepted int `gorm:"not null"`

	// Refusals is a BOUNDED sample of the per-device refusals; RefusalCounts is the
	// complete per-code total and is never truncated. A dynamic group can legally resolve
	// to tens of thousands of devices, so an unbounded refusal list would be
	// tenant-controlled write amplification on a single row — but the arithmetic still has
	// to close, which is what the untruncated counts are for.
	Refusals      *datatypes.JSON
	RefusalCounts *datatypes.JSON
}

// NewCommandBatchSchema adds the batch record (ADR-012) and links commands to it.
//
// A batch is one command fanned out to many devices. It is a persisted record rather than
// only a mutation response because the response is consumed once: a refused device
// otherwise leaves no trace at all, and an operator who fires a fleet push and comes back
// in the morning has nothing to read.
//
// 🔴 THE TWO HALVES ARE WRITTEN DIFFERENTLY ON PURPOSE, and the asymmetry is the rule this
// package already documents on migration_held_ceiling_index.go:
//
//   - the NEW table is created from a snapshot STRUCT, because a whole table's shape is
//     what a struct expresses well;
//   - the columns added to `commands` are LITERAL, schema-qualified DDL with no struct,
//     because a second type in this package mapping to "commands" is impossible without a
//     TableName method — and a TableName method bypasses the area TablePrefix, so the
//     migration would create its index against an unqualified table resolved through
//     search_path. AutoMigrate over a whole-row snapshot would also re-assert columns this
//     change has no business touching, which is how a narrow migration silently becomes a
//     wide one.
//
// Every statement is individually re-runnable (IF NOT EXISTS), which it has to be:
// migrations run with UseTransaction:false, so a half-applied migration is never rolled
// back and replays from the top on the next boot.
func NewCommandBatchSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260813180000",
		Migrate: func(tx *gorm.DB) error {
			if err := tx.AutoMigrate(&commandBatch{}); err != nil {
				return err
			}
			if err := createBatchTenantTokenIndex(tx, &commandBatch{}); err != nil {
				return err
			}

			// batch_id is the link a batch's commands are counted and cancelled by;
			// batch_token is DENORMALIZED alongside it so the command search criteria can
			// filter on the token without a join — the criteria builder is single-table,
			// and the repo already denormalizes a target token onto an edge for the same
			// reason.
			for _, ddl := range []string{
				`ALTER TABLE "command-delivery".commands ADD COLUMN IF NOT EXISTS batch_id bigint;`,
				`ALTER TABLE "command-delivery".commands ADD COLUMN IF NOT EXISTS batch_token varchar(128);`,
				// 🔴 THE INTRA-BATCH DUPLICATE GUARD — and NOT the batch's idempotency
				// mechanism, which is the (tenant_id, token) index on command_batches plus
				// the all-or-nothing commit. A replay creates no command rows at all, so
				// this index never fires on that path. What it does is make "one command
				// per device per batch" true in the schema rather than only in the code
				// that dedupes the request, and give the insert a backstop that cannot be
				// forgotten. tenant_id leads because every unique index here carries it.
				`CREATE UNIQUE INDEX IF NOT EXISTS uix_commands_batch_device ` +
					`ON "command-delivery".commands (tenant_id, batch_id, device_token) ` +
					`WHERE batch_id IS NOT NULL AND deleted_at IS NULL;`,
				// Serves "show me that batch's commands". Partial for the same reason the
				// dispatchable index is: a deleted row is never the subject of it.
				//
				// ⚠️ AND NOT THE BATCH CANCEL, WHICH NOW EXISTS AND DOES NOT USE THIS
				// INDEX. An earlier version of this comment credited it in advance with
				// serving "the batch cancel, a bounded UPDATE over exactly this
				// predicate", and was then corrected to say no such cancel existed. It
				// does now — and it filters on batch_id, so it is served by
				// uix_commands_batch_device above, not by this. The claim was wrong in
				// both directions, which is why the correction is kept rather than
				// quietly dropped.
				`CREATE INDEX IF NOT EXISTS idx_commands_batch_token ` +
					`ON "command-delivery".commands (tenant_id, batch_token) ` +
					`WHERE batch_token IS NOT NULL AND deleted_at IS NULL;`,
			} {
				if err := tx.Exec(ddl).Error; err != nil {
					return err
				}
			}
			return nil
		},
		// Rollback drops what this created, indexes before columns so an interrupted
		// rollback never leaves an index over a column that no longer exists. Both
		// directions are individually re-runnable.
		Rollback: func(tx *gorm.DB) error {
			for _, ddl := range []string{
				`DROP INDEX IF EXISTS "command-delivery".idx_commands_batch_token;`,
				`DROP INDEX IF EXISTS "command-delivery".uix_commands_batch_device;`,
				`ALTER TABLE "command-delivery".commands DROP COLUMN IF EXISTS batch_token;`,
				`ALTER TABLE "command-delivery".commands DROP COLUMN IF EXISTS batch_id;`,
			} {
				if err := tx.Exec(ddl).Error; err != nil {
					return err
				}
			}
			return tx.Migrator().DropTable("command_batches")
		},
	}
}

// createBatchTenantTokenIndex creates the per-tenant partial unique index on the batch
// table: (tenant_id, token) unique among rows where deleted_at IS NULL (ADR-042 P1).
//
// 🔴 THIS IS A DELIBERATE COPY OF baseline.go's createTenantTokenIndex, NOT AN OVERSIGHT,
// and the reason is the same one stated there one level out. That helper is frozen inside
// an applied migration; calling it from a LATER migration would put this migration's output
// — an index name and a WHERE predicate, both of which are schema — under the control of a
// function that exists to serve a different, already-shipped migration. The two are free to
// diverge, and that freedom is the point. Do not "clean this up".
func createBatchTenantTokenIndex(tx *gorm.DB, model any) error {
	stmt := &gorm.Statement{DB: tx}
	if err := stmt.Parse(model); err != nil {
		return fmt.Errorf("parse model for batch tenant-token index: %w", err)
	}
	bare := stmt.Table
	if i := strings.LastIndex(bare, "."); i >= 0 {
		bare = bare[i+1:]
	}
	return tx.Exec(fmt.Sprintf(
		"CREATE UNIQUE INDEX IF NOT EXISTS %s ON %s (%s, %s) WHERE deleted_at IS NULL",
		stmt.Quote("uix_"+bare+"_tenant_token"), stmt.Quote(stmt.Table),
		stmt.Quote("tenant_id"), stmt.Quote("token"),
	)).Error
}
