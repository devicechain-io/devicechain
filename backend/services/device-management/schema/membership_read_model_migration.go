// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"github.com/devicechain-io/dc-microservice/rdb"
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewMembershipReadModelSchema adds the ADR-062 S2 membership subsystem's two tables:
// entity_group_membership (the positive-only read-model of which entities belong to
// which rule-scoped group@version) and entity_group_facet_ref (the facet-key→group@v
// reverse index that bounds incremental recompute). Both are additive, carry no data
// step (pre-GA fresh bring-up), and are populated lazily — a table stays empty until a
// rule references a group (ADR-062 S4), so an install not using rule scoping pays
// nothing. The migration-local structs must produce the same columns/indexes as
// model.EntityGroupMembership / model.EntityGroupFacetRef.
func NewMembershipReadModelSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260714160000",
		Migrate: func(tx *gorm.DB) error {
			type EntityGroupMembership struct {
				gorm.Model
				rdb.TenantScoped
				EntityType      string `gorm:"not null;size:32;uniqueIndex:uix_entity_group_membership,priority:3;index:idx_egm_entity,priority:1"`
				EntityId        uint   `gorm:"not null;uniqueIndex:uix_entity_group_membership,priority:4;index:idx_egm_entity,priority:2"`
				GroupId         uint   `gorm:"not null;uniqueIndex:uix_entity_group_membership,priority:1;index:idx_egm_group,priority:1"`
				SelectorVersion int32  `gorm:"not null;uniqueIndex:uix_entity_group_membership,priority:2;index:idx_egm_group,priority:2"`
				GroupToken      string `gorm:"not null;size:255"`
			}
			type EntityGroupFacetRef struct {
				gorm.Model
				rdb.TenantScoped
				FacetKey        string `gorm:"not null;size:256;index:idx_egfr_lookup,priority:1;uniqueIndex:uix_entity_group_facet_ref,priority:3"`
				MemberType      string `gorm:"not null;size:32;index:idx_egfr_lookup,priority:2"`
				GroupId         uint   `gorm:"not null;uniqueIndex:uix_entity_group_facet_ref,priority:1;index:idx_egfr_group,priority:1"`
				SelectorVersion int32  `gorm:"not null;uniqueIndex:uix_entity_group_facet_ref,priority:2;index:idx_egfr_group,priority:2"`
				GroupToken      string `gorm:"not null;size:255"`
			}
			return tx.AutoMigrate(&EntityGroupMembership{}, &EntityGroupFacetRef{})
		},
		Rollback: func(tx *gorm.DB) error {
			if err := tx.Migrator().DropTable("entity_group_facet_refs"); err != nil {
				return err
			}
			return tx.Migrator().DropTable("entity_group_memberships")
		},
	}
}
