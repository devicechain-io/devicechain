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

// NewConnectorsSchema adds the connectors table (ADR-060 slice C4): the mutable draft
// of a tenant-scoped, versioned outbound-connector definition. The struct is
// re-declared locally (the gormigrate convention) so the migration is pinned to the
// shape at this point in history, independent of later model changes. rdb.TokenReference
// declares only a non-unique index; the per-tenant partial unique index on token
// (ADR-042 P1) is created explicitly via rdb.CreateTenantTokenIndex, mirroring every
// other token-referenced entity — without it two live connectors could share a token
// within a tenant.
func NewConnectorsSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260713130000",
		Migrate: func(tx *gorm.DB) error {
			type Connector struct {
				gorm.Model
				rdb.TenantScoped
				rdb.TokenReference
				rdb.NamedEntity
				Type   string         `gorm:"not null;size:64"`
				Config datatypes.JSON `gorm:"not null"`
			}
			if err := tx.AutoMigrate(&Connector{}); err != nil {
				return err
			}
			// ADR-042 P1: per-tenant partial unique index on token.
			return rdb.CreateTenantTokenIndex(tx, &Connector{})
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Migrator().DropTable("connectors")
		},
	}
}

// NewConnectorVersionsSchema adds the connector_versions table (ADR-060 slice C4):
// immutable published snapshots of a connector's {type, config}, addressed by parent +
// monotonic version. No token index (a version has no token, unlike Connector); the
// (connector_id, version) unique index comes from the struct tags via AutoMigrate.
func NewConnectorVersionsSchema() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20260713130001",
		Migrate: func(tx *gorm.DB) error {
			type ConnectorVersion struct {
				gorm.Model
				rdb.TenantScoped
				ConnectorID uint           `gorm:"not null;uniqueIndex:uix_connector_versions_connector_version,priority:1"`
				Version     int32          `gorm:"not null;uniqueIndex:uix_connector_versions_connector_version,priority:2"`
				Type        string         `gorm:"not null;size:64"`
				Config      datatypes.JSON `gorm:"not null"`
				Label       sql.NullString `gorm:"size:128"`
				Description sql.NullString `gorm:"size:1024"`
				PublishedBy string         `gorm:"size:256"`
			}
			return tx.AutoMigrate(&ConnectorVersion{})
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Migrator().DropTable("connector_versions")
		},
	}
}
