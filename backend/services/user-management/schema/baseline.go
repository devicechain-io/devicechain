// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"fmt"

	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-user-management/iam"
	"github.com/devicechain-io/dc-user-management/model"
	"github.com/devicechain-io/dc-user-management/settings"
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
// Config carries ADR-023 rate ceilings, validated against the key registry
// (decision 8; see iam.ValidateTierConfig). Every key here must be registered or the
// tier is unwritable — which is the point: a typo cannot become a setting nothing
// reads.
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
	{iam.TierGoldToken, "Gold",
		"Premium packaging: the last to shed under contention, the broadest set of AI models, and the highest ceilings — double the standard ingest and egress rate.",
		map[string]any{
			"ingestMessagesPerSecond":   float64(2000),
			"ingestBurst":               float64(4000),
			"outboundMessagesPerSecond": float64(200),
			"outboundBurst":             float64(400),
		}},
	{iam.TierSilverToken, "Silver",
		"Standard packaging: the platform's default ceilings and entitlements.",
		nil},
	{iam.TierBronzeToken, "Bronze",
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
// again. A single baseline over the current structs cannot drift from itself.
//
// Consequence, and it is deliberate: an EXISTING instance is not migrated onto this
// baseline, it is recreated (dcctl destroy + bootstrap). Nothing here is written to
// preserve a pre-GA developer database.
//
// Adding a column from here on: AutoMigrate is idempotent and converges an existing
// table on the struct, so a purely additive change needs no new migration at all
// until v1.0.0 freezes this baseline. Anything NOT additive (a backfill, a
// constraint over existing rows) still needs its own migration appended after this
// one, and must be individually re-runnable — migrations run with
// UseTransaction:false (core/rdb: Timescale DDL cannot run in a transaction), so a
// half-applied migration is never rolled back and replays from the top on the next
// boot.
func NewBaselineSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260716190000",
		Migrate: func(tx *gorm.DB) error {
			// TenantTier before Tenant: the tenant's required FK references it.
			// AutoMigrate creates the many2many join tables for the role
			// associations on its own.
			if err := tx.AutoMigrate(
				&model.SigningKey{},
				&settings.SystemSetting{},
				&iam.TenantTier{},
				&iam.Tenant{},
				&iam.Role{},
				&iam.Identity{},
				&iam.Membership{},
				&iam.OAuthClient{},
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
		// The seeds are held to the same registry the API enforces, so a bad key
		// here fails the migration loudly at boot rather than shipping a tier whose
		// setting nothing reads.
		if err := iam.ValidateTierConfig(s.config); err != nil {
			return fmt.Errorf("seed tenant tier %q: %w", s.token, err)
		}
		tier := iam.TenantTier{Token: s.token}
		if err := tx.Where(iam.TenantTier{Token: s.token}).
			Attrs(iam.TenantTier{
				NamedEntity: rdb.NamedEntity{
					Name:        rdb.NullStrOf(&name),
					Description: rdb.NullStrOf(&desc),
				},
				Config: s.config,
			}).
			FirstOrCreate(&tier).Error; err != nil {
			return fmt.Errorf("seed tenant tier %q: %w", s.token, err)
		}
	}
	return nil
}
