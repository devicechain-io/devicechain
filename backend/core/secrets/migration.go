// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package secrets

import (
	"github.com/devicechain-io/dc-microservice/rdb"
	gormigrate "github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// NewSecretStoreSchema is the shared migration that creates the secrets table
// (ADR-059 §2), under the ID the first three secret-store areas applied it with. A
// consuming service appends it to its own gormigrate slice — each service keeps its
// secrets in its own database, sealed with the same instance KEK, so the crypto is
// uniform without a shared datastore.
//
// The ID is frozen: notification-management, outbound-connectors and ai-inference have
// it recorded in their migration tables. It is NOT an ID every area can use. An area
// that adopts the store after its own chain has moved past this timestamp would put it
// out of order in a chain its tests require to be strictly increasing, so it calls
// NewSecretStoreSchemaAt with an ID of its own instead (user-management does).
func NewSecretStoreSchema() *gormigrate.Migration {
	return NewSecretStoreSchemaAt("20260713120000")
}

// NewSecretStoreSchemaAt is NewSecretStoreSchema under a caller-chosen migration ID.
// The body is the same code, so every area builds the same table and the same handle
// index whichever ID it recorded them under.
//
// The snapshot struct is defined inline so the migration is frozen against future
// model changes; it must produce the same columns as model Secret. An ID only needs to
// be stable and unique within one service's history: each service has its own
// migrations table, so two areas recording the same ID do not collide.
func NewSecretStoreSchemaAt(id string) *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: id,
		Migrate: func(tx *gorm.DB) error {
			type Secret struct {
				gorm.Model
				rdb.TenantScoped

				Scope      string `gorm:"not null;size:16;index"`
				Name       string `gorm:"not null;size:256;index"`
				Ciphertext []byte `gorm:"not null"`
				Nonce      []byte `gorm:"not null"`
				WrappedDEK []byte `gorm:"not null"`
				KEKVersion int    `gorm:"not null"`
				Alg        string `gorm:"not null;size:32"`
			}
			if err := tx.AutoMigrate(&Secret{}); err != nil {
				return err
			}
			// The secret handle (tenant_id, scope, name) is unique among LIVE rows —
			// soft-delete-aware so a deleted handle frees for reuse (the ADR-042
			// token-lock lesson). The store's Delete is now a HARD delete, so it leaves
			// no soft-deleted row behind for the predicate to skip; the predicate stays
			// because this index is frozen schema, and it still frees any soft-deleted
			// row written before that change. tenant_id is the empty sentinel for instance rows, a
			// normal comparable value, so instance secrets are constrained too (a NULL
			// would be treated as distinct and defeat the index).
			return rdb.CreatePartialUniqueIndex(tx, &Secret{}, "uix_secrets_tenant_scope_name", "tenant_id", "scope", "name")
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Migrator().DropTable("secrets")
		},
	}
}
