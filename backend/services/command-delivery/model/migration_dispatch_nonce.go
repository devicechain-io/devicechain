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

// NewCommandDispatchNonceSchema names the dispatch a command is currently on.
//
// Every write that moves a row into SENT stamps a fresh value, and the published delivery
// envelope carries it. A transport that finds the device unreachable quotes it back when it
// hands the command over, and the park matches on it — so a request still in redelivery
// names a dispatch that no longer exists and moves nothing. Without that, parking would
// re-arm a command the device had already run. The full sequence is on
// Command.DispatchNonce.
//
// 🔴 NO SNAPSHOT STRUCT, BY THE RULE THIS PACKAGE FOLLOWS: a NEW table is created from a
// snapshot struct, while a column added to an EXISTING one is literal, schema-qualified
// DDL. AutoMigrate over a whole-row snapshot would re-assert every other column of
// `commands`, which is how a one-column change silently becomes a table-wide one.
//
// Nullable and without a default, which is the honest shape rather than a convenience.
// NULL means "this row is not on any dispatch" — it has never been claimed, or its claim
// was retired — and it is what makes the park predicate safe on rows that predate this
// migration: a stamped envelope can never quote NULL, so an old row is simply not parkable
// and rides the behaviour it already had.
//
// The statement is individually re-runnable, which it has to be: migrations run with
// UseTransaction:false, so a half-applied migration is never rolled back and replays from
// the top on the next boot.
func NewCommandDispatchNonceSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260815090000",
		Migrate: func(tx *gorm.DB) error {
			return tx.Exec(
				`ALTER TABLE "command-delivery".commands ` +
					`ADD COLUMN IF NOT EXISTS dispatch_nonce varchar(64);`).Error
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Exec(
				`ALTER TABLE "command-delivery".commands ` +
					`DROP COLUMN IF EXISTS dispatch_nonce;`).Error
		},
	}
}
