// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// rateKeyRename is one ceiling whose name now states its unit: the per-tenant override
// column on iam_tenants and the key a tier declares the same ceiling under in its config.
// The names are literals, frozen here as of this migration — never derived from the live
// governance dimensions, which is what makes this a snapshot and not a pointer at today's
// model.
type rateKeyRename struct {
	oldColumn, newColumn string
	oldKey, newKey       string
}

// rateKeyRenames: ingest is metered in READINGS (decoded samples), outbound in CALLS (one
// connector action dispatched). "Messages" was accurate for neither once the ingest
// ceiling started counting readings.
var rateKeyRenames = []rateKeyRename{
	{"ingest_messages_per_second", "ingest_readings_per_second", "ingestMessagesPerSecond", "ingestReadingsPerSecond"},
	{"outbound_messages_per_second", "outbound_calls_per_second", "outboundMessagesPerSecond", "outboundCallsPerSecond"},
}

// rateKeyTenantSnapshot names the table the columns are renamed on, and nothing else: the
// rename touches no other column. 🔴 The TableName is load-bearing — see
// migration_tenant_locale.go.
type rateKeyTenantSnapshot struct {
	ID uint `gorm:"primarykey"`
}

func (rateKeyTenantSnapshot) TableName() string { return "iam_tenants" }

// rateKeyTierRow is this migration's own read shape for iam_tenant_tiers: the id and the
// config blob as the TEXT it is stored as. It deliberately does not go through the gorm
// json serializer, so numbers are re-written exactly as they were stored (json.Number),
// and keys this migration does not rename pass through untouched.
type rateKeyTierRow struct {
	ID     uint
	Config *string
}

// NewRateKeysNameUnitsMigration renames the ingest and outbound rate ceilings so their
// names state their units: ingestMessagesPerSecond becomes ingestReadingsPerSecond and
// outboundMessagesPerSecond becomes outboundCallsPerSecond — on the tenant override
// columns, and on every tier config that declares one (the baseline's own seeded gold and
// bronze tiers included, which is why a fresh install comes out under the new keys).
//
// There is no alias. The tier-config registry refuses the old keys like any other unknown
// key, so a config left under them would be stored and never read — the tenant silently
// metered at the platform default while its tier says otherwise. That is why every stored
// config is re-keyed here rather than left for an operator to notice.
//
// Individually re-runnable, as the chain requires: a column already renamed and a config
// already re-keyed are both no-ops. A row that carries BOTH spellings of one ceiling, or a
// table that carries both columns, is refused rather than resolved: neither release can
// write that state, so it means someone wrote it by hand, and picking a winner would
// silently discard a ceiling they set.
func NewRateKeysNameUnitsMigration() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261010120000",
		Migrate: func(db *gorm.DB) error {
			return db.Transaction(func(tx *gorm.DB) error {
				for _, r := range rateKeyRenames {
					if err := renameRateColumn(tx, r.oldColumn, r.newColumn); err != nil {
						return err
					}
				}
				return rekeyTierConfigs(tx, func(r rateKeyRename) (string, string) { return r.oldKey, r.newKey })
			})
		},
		Rollback: func(db *gorm.DB) error {
			return db.Transaction(func(tx *gorm.DB) error {
				for _, r := range rateKeyRenames {
					if err := renameRateColumn(tx, r.newColumn, r.oldColumn); err != nil {
						return err
					}
				}
				return rekeyTierConfigs(tx, func(r rateKeyRename) (string, string) { return r.newKey, r.oldKey })
			})
		},
	}
}

// renameRateColumn renames one iam_tenants column, once. RENAME COLUMN keeps the column's
// position, type and nullability on both engines (SQLite has supported it since 3.25), so
// the result is the same table with a different name in one place.
func renameRateColumn(tx *gorm.DB, from, to string) error {
	m := tx.Migrator()
	hasFrom, hasTo := m.HasColumn(&rateKeyTenantSnapshot{}, from), m.HasColumn(&rateKeyTenantSnapshot{}, to)
	switch {
	case hasFrom && hasTo:
		return fmt.Errorf("iam_tenants carries both %s and %s; refusing to choose between them", from, to)
	case hasFrom:
		return tx.Exec(fmt.Sprintf("ALTER TABLE iam_tenants RENAME COLUMN %s TO %s", from, to)).Error
	case hasTo:
		return nil
	default:
		return fmt.Errorf("iam_tenants carries neither %s nor %s", from, to)
	}
}

// rekeyTierConfigs moves each renamed ceiling from one key to the other in every tier's
// config, soft-deleted tiers included (a restored tier must not come back under a key
// nothing reads). Only rows that change are written.
func rekeyTierConfigs(tx *gorm.DB, direction func(rateKeyRename) (from, to string)) error {
	var rows []rateKeyTierRow
	if err := tx.Raw("SELECT id, config FROM iam_tenant_tiers").Scan(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		if row.Config == nil {
			continue
		}
		dec := json.NewDecoder(bytes.NewReader([]byte(*row.Config)))
		dec.UseNumber()
		var cfg map[string]any
		if err := dec.Decode(&cfg); err != nil {
			return fmt.Errorf("tier %d: config is not a JSON object: %w", row.ID, err)
		}
		if cfg == nil {
			continue
		}
		changed := false
		for _, r := range rateKeyRenames {
			from, to := direction(r)
			v, ok := cfg[from]
			if !ok {
				continue
			}
			if _, both := cfg[to]; both {
				return fmt.Errorf("tier %d: config carries both %s and %s; refusing to choose between them", row.ID, from, to)
			}
			cfg[to] = v
			delete(cfg, from)
			changed = true
		}
		if !changed {
			continue
		}
		out, err := json.Marshal(cfg)
		if err != nil {
			return fmt.Errorf("tier %d: %w", row.ID, err)
		}
		if err := tx.Exec("UPDATE iam_tenant_tiers SET config = ? WHERE id = ?", string(out), row.ID).Error; err != nil {
			return err
		}
	}
	return nil
}
