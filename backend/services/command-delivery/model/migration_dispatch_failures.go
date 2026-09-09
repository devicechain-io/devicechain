// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package model

// Like the migrations beside it, this file imports none of the service's own packages. A
// migration that can reach a live model is a migration whose behaviour changes when that
// model does.
import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewCommandDispatchFailuresSchema counts the failed dispatches of one command, so a
// command the platform cannot publish can reach a terminal state.
//
// Without it a command whose publish always fails cycles QUEUED -> SENT -> QUEUED on
// every sweep tick — the claim is placed, the publish errors, the release takes the row
// back — with nothing counting and nothing able to stop it. It ends only when the TTL
// elapses, days later, and it ends on TIMEOUT, which says the command was dispatched to a
// device that never answered. Nothing was ever dispatched. The column is what lets the
// release path stop at a bound and record FAILED with the platform named as the cause.
//
// 🔴 NO SNAPSHOT STRUCT, BY THE RULE THIS PACKAGE FOLLOWS: a NEW table is created from a
// snapshot struct, while a column added to an EXISTING one is literal, schema-qualified
// DDL. AutoMigrate over a whole-row snapshot would re-assert every other column of
// `commands`, which is how a one-column change silently becomes a table-wide one.
//
// 🔴 NOT NULL WITH A DEFAULT OF 0, WHICH IS THE SHAPE THE ARITHMETIC REQUIRES rather than
// a tidiness preference. The increment is `dispatch_failures = dispatch_failures + 1`, and
// NULL + 1 is NULL: a nullable column would leave every row that predates this migration
// writing NULL back on each failure, never reaching the bound, and reporting no error
// while doing it — the bound would be inert on exactly the rows that already existed. The
// default also backfills those rows in the same statement, so there is no separate
// backfill to forget.
//
// The statement is individually re-runnable, which it has to be: migrations run with
// UseTransaction:false, so a half-applied migration is never rolled back and replays from
// the top on the next boot.
func NewCommandDispatchFailuresSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260908120000",
		Migrate: func(tx *gorm.DB) error {
			return tx.Exec(
				`ALTER TABLE "command-delivery".commands ` +
					`ADD COLUMN IF NOT EXISTS dispatch_failures integer NOT NULL DEFAULT 0;`).Error
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec(
				`ALTER TABLE "command-delivery".commands ` +
					`DROP COLUMN IF EXISTS dispatch_failures;`).Error
		},
	}
}
