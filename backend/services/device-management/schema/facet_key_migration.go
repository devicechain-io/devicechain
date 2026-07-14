// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"database/sql"

	"github.com/devicechain-io/dc-microservice/rdb"
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// facetKey is the migration-local shape of the facet-key registry (ADR-061 G2 /
// SD-5): a per-tenant declaration that an EntityAttribute key, for a member family,
// is a classification facet. Kept local to the migration so it never drifts with
// the live model.
type facetKey struct {
	gorm.Model
	rdb.TenantScoped

	MemberType string `gorm:"not null;size:32;index"`
	Key        string `gorm:"column:attr_key;not null;size:128"`
	ValueType  string `gorm:"not null;size:16"`
	Source     string `gorm:"not null;size:16;default:attribute"`
	Values     *datatypes.JSON
	Label      sql.NullString
}

func (facetKey) TableName() string { return "facet_keys" }

// NewFacetKeySchema adds the facet_keys table (ADR-061 G2). A facet key is
// addressed by its natural key (member_type, attr_key) within a tenant — there is
// no opaque token — so the uniqueness invariant is a per-tenant partial-unique
// index on (tenant_id, member_type, attr_key) over LIVE rows only (the ADR-042
// tombstone-safe pattern), which lets a deleted facet's natural key be reused.
func NewFacetKeySchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260714130000",
		Migrate: func(tx *gorm.DB) error {
			if err := tx.AutoMigrate(&facetKey{}); err != nil {
				return err
			}
			return rdb.CreatePartialUniqueIndex(tx, &facetKey{},
				"uix_facet_keys_tenant_member_key", "tenant_id", "member_type", "attr_key")
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Migrator().DropTable("facet_keys")
		},
	}
}
