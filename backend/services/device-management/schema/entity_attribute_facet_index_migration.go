// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewEntityAttributeFacetIndexSchema adds the composite indexes that back the dynamic-group
// selector lowering (ADR-061 G3 §3.4) and normalizes any pre-G3 attribute values into the
// storage form the lowering relies on. Each facet leaf lowers to an EXISTS semi-join
// correlated on (tenant_id, entity_type, scope, attr_key, entity_id); the first index drives
// that presence/correlation lookup, and the second (…, value) lets an equality leaf push its
// value predicate into the index rather than scanning matching keys. Both are partial over
// live rows (deleted_at IS NULL), matching the semi-join's own predicate so tombstones stay
// out of the index. Non-unique — many entities share a (key, value) facet.
//
// The backfill is a decisive data cutover (not a compat shim): SetEntityAttribute only began
// enforcing the storage form in this slice, so a pre-existing row could hold non-numeric text
// under a LONG/DOUBLE type (which would fault ea.value::numeric at resolve) or a non-canonical
// boolean like 'True'/'1' (which a bool leaf, binding 'true'/'false', would silently miss). A
// non-castable numeric is nulled (unset); a boolean is canonicalized to 'true'/'false' (an
// unparseable one nulled) — exactly what normalizeAttributeValue now does on write.
func NewEntityAttributeFacetIndexSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260714140000",
		Migrate: func(tx *gorm.DB) error {
			// Null any LONG/DOUBLE value that is not plain-decimal numeric text (a conservative
			// subset of what ::numeric accepts — never keeps an uncastable value).
			if err := tx.Exec(
				`UPDATE entity_attributes SET value = NULL ` +
					`WHERE value_type IN ('LONG','DOUBLE') AND value IS NOT NULL ` +
					`AND value !~ '^[+-]?([0-9]+\.?[0-9]*|\.[0-9]+)([eE][+-]?[0-9]+)?$'`).Error; err != nil {
				return err
			}
			// Canonicalize booleans to 'true'/'false' (matching strconv.ParseBool's accepted
			// spellings, case-insensitively); anything else becomes unset.
			if err := tx.Exec(
				`UPDATE entity_attributes SET value = CASE ` +
					`WHEN lower(value) IN ('1','t','true') THEN 'true' ` +
					`WHEN lower(value) IN ('0','f','false') THEN 'false' ELSE NULL END ` +
					`WHERE value_type = 'BOOLEAN' AND value IS NOT NULL`).Error; err != nil {
				return err
			}
			if err := tx.Exec(
				`CREATE INDEX IF NOT EXISTS ix_entity_attributes_facet_lookup ` +
					`ON entity_attributes (tenant_id, entity_type, scope, attr_key, entity_id) ` +
					`WHERE deleted_at IS NULL`).Error; err != nil {
				return err
			}
			return tx.Exec(
				`CREATE INDEX IF NOT EXISTS ix_entity_attributes_facet_value ` +
					`ON entity_attributes (tenant_id, entity_type, scope, attr_key, value) ` +
					`WHERE deleted_at IS NULL`).Error
		},
		Rollback: func(tx *gorm.DB) error {
			if err := tx.Exec(`DROP INDEX IF EXISTS ix_entity_attributes_facet_value`).Error; err != nil {
				return err
			}
			return tx.Exec(`DROP INDEX IF EXISTS ix_entity_attributes_facet_lookup`).Error
		},
	}
}
