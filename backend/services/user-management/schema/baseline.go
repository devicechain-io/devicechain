// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

// This file imports none of the service's own packages, and that is a property worth
// keeping rather than an accident. A migration that can reach a live model, a live
// vocabulary or a live validator is a migration whose behaviour changes when those do.
// Everything it needs — shapes, seed values, helpers — it holds itself.
import (
	"database/sql"
	"fmt"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// dropTables drops the named tables, ignoring absent ones.
func dropTables(tx *gorm.DB, tables []string) error {
	for _, table := range tables {
		if err := tx.Migrator().DropTable(table); err != nil {
			return err
		}
	}
	return nil
}

// seededTiers is the tenant tier vocabulary the baseline installs (ADR-065
// decision 4). It is a SEED, not a definition: an operator may edit these rows or
// add their own — live, with no deploy — because what "bronze includes" is a product
// decision that changes. Nothing in the code may assume these values still hold.
//
// Config carries ADR-023 rate ceilings. These literals were valid against the key
// registry (decision 8) when this migration was written and are not re-checked against
// it at boot — see seedTenantTiers. The registry guards what an operator writes; a
// shipped migration's seed is not a write anyone makes again.
//
// SILVER DECLARES NOTHING, deliberately. Its ceilings are the platform defaults
// (event-sources ingest 1000/s burst 2000; outbound-connectors 100/s burst 200),
// and the standard tier IS the platform's own default packaging — so an operator
// who raises a platform default in Helm moves silver with it, rather than silently
// leaving every standard tenant pinned to a number frozen here. Gold and bronze are
// the explicit deviations, and being absolute they do NOT track that default; that
// is the trade for saying exactly what a tier is sold as.
//
// The AI-inference dimension is left to the platform default at every tier: what a
// tier offers there is WHICH MODELS, a join table rather than a rate (decision 10),
// and that lands with the provider menu.
//
// No "best-effort" tier is seeded. It only ever existed in ADR-063 to name the
// bottom band of a dial; with a required FK there is no unset state for it to name.
var seededTiers = []struct {
	token, name, description string
	config                   map[string]any
}{
	{"gold", "Gold",
		"Premium packaging: the last to shed under contention, the broadest set of AI models, and the highest ceilings — double the standard ingest and egress rate.",
		map[string]any{
			"ingestMessagesPerSecond":   float64(2000),
			"ingestBurst":               float64(4000),
			"outboundMessagesPerSecond": float64(200),
			"outboundBurst":             float64(400),
		}},
	{"silver", "Silver",
		"Standard packaging: the platform's default ceilings and entitlements.",
		nil},
	{"bronze", "Bronze",
		"Entry packaging: the first to shed under contention, a reduced set of AI models, and conservative ceilings — a quarter of the standard ingest and egress rate.",
		map[string]any{
			"ingestMessagesPerSecond":   float64(250),
			"ingestBurst":               float64(500),
			"outboundMessagesPerSecond": float64(25),
			"outboundBurst":             float64(50),
		}},
}

// baselineTables is every table this baseline owns, ordered so a Rollback drops
// dependents before the rows they reference (memberships/tenants before tiers).
var baselineTables = []string{
	"iam_identity_system_roles",
	"iam_membership_tenant_roles",
	"iam_memberships",
	"iam_identities",
	"iam_roles",
	"iam_oauth_clients",
	"iam_tenants",
	"iam_tenant_tiers",
	"system_settings",
	"signing_keys",
}

// NewBaselineSchema materializes the whole user-management schema in one step: the
// instance signing key (ADR-008), the global iam model — identities, memberships,
// the role catalog, tenants and their tier (ADR-033/065) — the OAuth 2.1 client
// registry (ADR-047), and system settings (ADR-038). It then seeds the tenant tier
// vocabulary, which must exist before any tenant can be created against its
// required FK.
//
// This REPLACES the former 14-migration chain, which was collapsed pre-GA (the
// convention: until v1.0.0 all models and APIs are changeable, and a decisive
// cutover beats migration scaffolding). Those migrations only ever built up to this
// same shape by adding one column at a time to instances that no longer exist
// outside a maintainer's laptop; the chain's own history has no value before there
// is a released version to upgrade FROM.
//
// Collapsing it also removes an entire failure mode rather than patching it. Every
// migration in that chain called AutoMigrate against the CURRENT structs, so an
// early migration silently acquired whatever a later slice added to a model — which
// is how adding the tier's NOT NULL FK made the long-settled tenant migration create
// that FK on a fresh database, wedging a later migration that then tried to add it
// again. Collapsing the chain did not fix that — it hid it, because a chain of length
// one has nothing to wedge against. What fixes it is that this migration now builds
// from its own snapshot types rather than the live models.
//
// Consequence, and it is deliberate: an EXISTING instance is not migrated onto this
// baseline, it is recreated (dcctl destroy + bootstrap). Nothing here is written to
// preserve a pre-GA developer database.
//
// CHANGING THE SCHEMA FROM HERE ON: append a new migration. Do not touch this one, and
// do not "update" the snapshot types it builds from — see baseline_snapshot.go. This
// file describes the schema as of the flatten and must keep describing exactly that,
// however far the live models move away from it; the difference between the two is
// carried by the migrations stacked on top, and the GA squash folds them all back into
// one. Anything appended must be individually re-runnable — migrations run with
// UseTransaction:false (core/rdb: Timescale DDL cannot run in a transaction), so a
// half-applied migration is never rolled back and replays from the top on the next
// boot.
//
// The earlier advice here said the opposite — that additive changes needed no migration
// because AutoMigrate converges. That was true only while this was the ONLY migration,
// and it was the thing that made a second one impossible to write: the baseline pointed
// at the live models, so adding a field to one silently changed what this migration
// creates, and the migration adding that column then hit "column already exists" on
// every fresh install.
func NewBaselineSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260716190000",
		Migrate: func(tx *gorm.DB) error {
			// The SNAPSHOT types in baseline_snapshot.go, never the live models —
			// see the note there. tenantTier before tenant: the tenant's required
			// FK references it. AutoMigrate creates the many2many join tables for
			// the role associations on its own.
			if err := tx.AutoMigrate(
				&signingKey{},
				&systemSetting{},
				&tenantTier{},
				&tenant{},
				&role{},
				&identity{},
				&membership{},
				&oauthClient{},
			); err != nil {
				return err
			}
			return seedTenantTiers(tx)
		},
		Rollback: func(tx *gorm.DB) error {
			return dropTables(tx, baselineTables)
		},
	}
}

// seedTenantTiers installs the gold/silver/bronze vocabulary if absent (ADR-065).
//
// Seed-if-absent, NOT the Assign-style upsert iam.Store.EnsureRole uses for the
// built-in `viewer` role. That one deliberately clobbers on every startup because
// for a role the code IS the source of truth; for a tier it is not — packaging is
// the operator's to define, so re-asserting these values would silently revert their
// edits. Here it runs once, from the one writer, and cannot clobber at all.
func seedTenantTiers(tx *gorm.DB) error {
	for _, s := range seededTiers {
		name, desc := s.name, s.description
		// These values are NOT validated against iam's live tier-config key registry,
		// and that is the snapshot rule rather than an omission. The blobs above are
		// literals that were valid when this migration was written; checking them
		// against a registry that moves would let a later slice retiring a governance
		// dimension make this long-settled migration start failing at boot on every
		// fresh install. The API validates what an OPERATOR writes, which is the write
		// path that can actually carry a typo — nobody edits a shipped migration's seed.
		// Seeded through the SNAPSHOT type, for the same reason the tables are created
		// from it. Inserting through iam.TenantTier would write whatever columns the
		// live model has TODAY into the table this migration created back THEN — so the
		// first column added to the model would make this INSERT name a column that does
		// not exist here yet, and the migration would die at boot on a fresh install.
		// A migration's writes are part of its shape.
		row := tenantTier{Token: s.token}
		if err := tx.Where(tenantTier{Token: s.token}).
			Attrs(tenantTier{
				Name:        sql.NullString{String: name, Valid: true},
				Description: sql.NullString{String: desc, Valid: true},
				Config:      s.config,
			}).
			FirstOrCreate(&row).Error; err != nil {
			return fmt.Errorf("seed tenant tier %q: %w", s.token, err)
		}
	}
	return nil
}
