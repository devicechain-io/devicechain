// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"encoding/json"
	"errors"

	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewShedPrioritySchema adds the per-tenant ADR-063 shed-priority override column to
// iam_tenants (ADR-063 decision 1 — the stored int 1–100 that survives ADR-065 as this
// ADR's own operational override over the tier-derived default). Nullable: NULL means
// "inherit" (the tenant's tier shedPriority, then the platform fail-safe).
//
// Written the way every migration after the baseline must be (see baseline.go /
// data-modeling.md): its OWN snapshot of just the column it touches, never the live
// iam.Tenant, and it never edits the baseline. The table is pinned with tx.Table
// rather than a TableName method, so gorm does not derive "tenants" and miss the iam_
// prefix; tx.Table drives both the add and the rollback drop.
func NewShedPrioritySchema() *gormigrate.Migration {
	// The snapshot: iam_tenants as this migration needs to see it — its primary key plus
	// only the new column.
	type tenant struct {
		ID           uint `gorm:"primarykey"`
		ShedPriority *int
	}
	return &gormigrate.Migration{
		ID: "20260721120000",
		Migrate: func(tx *gorm.DB) error {
			return tx.Table("iam_tenants").AutoMigrate(&tenant{})
		},
		Rollback: func(tx *gorm.DB) error {
			// HasColumn-guarded so the rollback is individually re-runnable (the
			// UseTransaction:false doctrine).
			m := tx.Table("iam_tenants").Migrator()
			if m.HasColumn(&tenant{}, "ShedPriority") {
				return m.DropColumn(&tenant{}, "ShedPriority")
			}
			return nil
		},
	}
}

// tierShedPriorityDefaults are the ADR-063 shed-priority defaults seeded onto the
// gold/silver/bronze tiers (ADR-065 S6), so the promise a fresh install SELLS — gold
// is the last to shed, bronze the first (already the words in their seeded
// descriptions) — holds without an operator configuring anything. The behavior may
// NOT key on the tier's token (ADR-065 forbids assuming a token still means what it
// meant at seed), so it must come from config; these values put it there.
//
// Gold sits in the never-shed band (80–100), silver mid (50–79), bronze the
// first-shed band (20–49). No best-effort tier is seeded (there is none — see
// baseline.go), so the deepest-shed band exists only via a per-tenant override.
var tierShedPriorityDefaults = []struct {
	token    string
	priority float64
}{
	{"gold", 90},
	{"silver", 60},
	{"bronze", 30},
}

// NewTierShedPrioritySeed adds shedPriority to the seeded tiers' config blobs IF
// ABSENT — never stomping an operator's edit (packaging is the operator's to define,
// the same reason seedTenantTiers seeds-if-absent rather than upserting). A tier an
// operator has deleted is not recreated. Idempotent and individually re-runnable: the
// if-absent guard makes a replay a no-op.
//
// Seeds through a local SNAPSHOT of iam_tenant_tiers (token + config) for the snapshot
// rule — inserting/reading through iam.TenantTier would bind whatever columns the live
// model carries today. It merges into the existing JSON config rather than replacing
// it, so a tier's ceilings survive.
func NewTierShedPrioritySeed() *gormigrate.Migration {
	type tenantTier struct {
		ID     uint           `gorm:"primarykey"`
		Token  string         `gorm:"uniqueIndex;not null;size:128"`
		Config map[string]any `gorm:"serializer:json"`
	}
	const key = "shedPriority"
	return &gormigrate.Migration{
		ID: "20260721120100",
		Migrate: func(tx *gorm.DB) error {
			for _, d := range tierShedPriorityDefaults {
				var row tenantTier
				err := tx.Table("iam_tenant_tiers").Where("token = ?", d.token).First(&row).Error
				if errors.Is(err, gorm.ErrRecordNotFound) {
					continue // operator removed this seeded tier; do not recreate it
				}
				if err != nil {
					return err
				}
				if row.Config == nil {
					row.Config = map[string]any{}
				}
				if _, ok := row.Config[key]; ok {
					continue // already carries a shed priority (operator-set or a prior run)
				}
				row.Config[key] = d.priority
				if err := tx.Table("iam_tenant_tiers").Where("token = ?", d.token).
					Select("Config").Updates(&row).Error; err != nil {
					return err
				}
			}
			return nil
		},
		Rollback: func(tx *gorm.DB) error {
			// Remove only the key we added, and only where it still equals the value we
			// seeded — an operator who retuned it keeps their value. Re-runnable.
			for _, d := range tierShedPriorityDefaults {
				var row tenantTier
				err := tx.Table("iam_tenant_tiers").Where("token = ?", d.token).First(&row).Error
				if errors.Is(err, gorm.ErrRecordNotFound) {
					continue
				}
				if err != nil {
					return err
				}
				if row.Config == nil {
					continue
				}
				if v, ok := row.Config[key]; !ok || !numericEquals(v, d.priority) {
					continue
				}
				delete(row.Config, key)
				if err := tx.Table("iam_tenant_tiers").Where("token = ?", d.token).
					Select("Config").Updates(&row).Error; err != nil {
					return err
				}
			}
			return nil
		},
	}
}

// numericEquals reports whether a JSON-decoded config value equals want. The json
// serializer decodes a number to float64 (or json.Number), so a bare == on `any`
// would miss; this coerces both to float64 first. Local to this migration for the
// snapshot rule — it may not reach into iam's helpers.
func numericEquals(v any, want float64) bool {
	switch n := v.(type) {
	case float64:
		return n == want
	case float32:
		return float64(n) == want
	case int:
		return float64(n) == want
	case int64:
		return float64(n) == want
	case json.Number:
		f, err := n.Float64()
		return err == nil && f == want
	default:
		return false
	}
}
