// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

// This file imports none of the service's own packages, and that is a property worth
// keeping rather than an accident. A migration that can reach a live model, a live
// vocabulary or a live validator is a migration whose behaviour changes when those do.
import (
	"fmt"
	"strings"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewBaselineSchema materializes notification-management's OWN schema in one step: a tenant's
// delivery channels, the routing policies and their per-severity rules, and the per-alarm
// notification/escalation state the dispatcher and scheduler share (ADR-017).
//
// 🔴 IT DOES NOT CREATE THE SECRETS TABLE. The shared secret store (ADR-059) — this service was
// its first consumer — is migrated by core/secrets and stays a separate entry in the chain,
// deliberately: three services consume it and every one must seal with the same instance KEK,
// so that table IS core's schema. Contrast the inlined mixins in baseline_snapshot.go, where
// core owning a fragment of THIS service's table is accidental and hazardous.
//
// This REPLACES the former four-migration chain for this area's own tables, collapsed pre-GA.
// Equivalence is proven by hack/migration-diff.sh verify against the golden captured from that
// chain.
//
// 🔴 ONE OF THE FOUR WAS AN IRREVERSIBLE CUTOVER, AND DROPPING IT IS SAFE ONLY BECAUSE OF THE
// PRE-GA RULE. NewNotificationChannelDropSecretSchema removed the legacy reversible plaintext
// `secret` column from notification_channels once the delivery secret moved into the store. On
// a FRESH database it was already a no-op — the create migration had been edited so the column
// was never created — which is why this baseline reproduces the golden without it. But a
// database that ran the ORIGINAL create still holds that column, and its plaintext, and
// nothing in this chain will remove it any more. That is acceptable only under the convention
// that an existing pre-GA instance is RECREATED rather than migrated (dcctl destroy +
// bootstrap); it would not be acceptable after v1.0.0, and it is the reason the squash has to
// land before the tag rather than during 0.9.x.
//
// None of the four carried data — no seed, no backfill — so nothing else is dropped and nothing
// needs a row-level test.
//
// CHANGING THE SCHEMA FROM HERE ON: append a new migration. Do not touch this one, and do not
// "update" the snapshot types it builds from. Anything appended must be individually
// re-runnable, since migrations run with UseTransaction:false and replay from the top after a
// failure.
func NewBaselineSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260729000000",
		Migrate: func(tx *gorm.DB) error {
			// The SNAPSHOT types in baseline_snapshot.go, never the live models.
			if err := tx.AutoMigrate(
				&notificationChannel{},
				&notificationPolicy{},
				&notificationRule{},
				&notificationState{},
			); err != nil {
				return err
			}

			// ADR-042 P1: a token is unique within a tenant among LIVE rows only.
			for _, model := range []any{&notificationChannel{}, &notificationPolicy{}} {
				if err := createTenantTokenIndex(tx, model); err != nil {
					return err
				}
			}

			// One live state row per raised alarm within a tenant. This is what makes the
			// dispatcher's upsert a clean conflict target, and it is soft-delete aware for the
			// same reason the token indexes are: a tombstone left in the uniqueness set would
			// keep an alarm's slot locked after the row was deleted.
			return createPartialUniqueIndex(tx, &notificationState{},
				"uix_notification_states_tenant_alarm_token", "tenant_id", "alarm_token")
		},
		Rollback: func(tx *gorm.DB) error {
			for _, table := range []string{
				"notification_states",
				"notification_rules",
				"notification_policies",
				"notification_channels",
			} {
				if err := tx.Migrator().DropTable(table); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

// createTenantTokenIndex creates the per-tenant partial unique index on a token-referenced,
// tenant-scoped, soft-deletable table.
//
// 🔴 THIS AND createPartialUniqueIndex ARE DELIBERATE COPIES of the rdb helpers, NOT an
// oversight. Calling core would put this migration's output — index NAMES and WHERE
// predicates, both of which are schema — under the control of code in another module. The day
// core renames an index or widens a predicate, this already-applied migration starts creating
// something different on fresh installs while every existing database keeps the old one, from
// a PR that never touched notification-management. The snapshot rule outranks DRY inside a
// migration. Do not "clean this up".
func createTenantTokenIndex(tx *gorm.DB, model any) error {
	stmt := &gorm.Statement{DB: tx}
	if err := stmt.Parse(model); err != nil {
		return fmt.Errorf("parse model for tenant-token index: %w", err)
	}
	bare := stmt.Table
	if i := strings.LastIndex(bare, "."); i >= 0 {
		bare = bare[i+1:]
	}
	return createPartialUniqueIndex(tx, model, "uix_"+bare+"_tenant_token", "tenant_id", "token")
}

// createPartialUniqueIndex creates a UNIQUE index restricted to live rows. GORM's struct-tag
// `unique` counts soft-deleted rows, so a deleted row would keep its slot locked forever and a
// lookup could still match a tombstone; the partial predicate frees the slot on delete.
func createPartialUniqueIndex(tx *gorm.DB, model any, name string, columns ...string) error {
	stmt := &gorm.Statement{DB: tx}
	if err := stmt.Parse(model); err != nil {
		return fmt.Errorf("parse model for partial unique index %s: %w", name, err)
	}
	quoted := make([]string, len(columns))
	for i, c := range columns {
		quoted[i] = stmt.Quote(c)
	}
	return tx.Exec(fmt.Sprintf(
		"CREATE UNIQUE INDEX IF NOT EXISTS %s ON %s (%s) WHERE deleted_at IS NULL",
		stmt.Quote(name), stmt.Quote(stmt.Table), strings.Join(quoted, ", "),
	)).Error
}
