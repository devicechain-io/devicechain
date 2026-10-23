// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"database/sql"
	"time"

	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// serviceAccountRoleRef names iam_roles, a table this migration does not own: the primary
// key only, so a foreign key can reference it. AutoMigrate on an existing table with a
// struct this thin adds nothing and drops nothing.
type serviceAccountRoleRef struct {
	ID uint `gorm:"primarykey"`
}

func (serviceAccountRoleRef) TableName() string { return "iam_roles" }

// serviceAccountSnapshot is this migration's snapshot of iam_service_accounts: a tenant's
// non-human principal for the OAuth client_credentials grant.
//
//   - tenant_id is the owning tenant's TOKEN, a plain reference like every other iam tenant
//     column, and what the tenant purge sweeps the table by with no list to maintain.
//   - (tenant_id, token) is unique: an account's token names it within its tenant.
//   - client_id is unique across the instance: the token endpoint finds the account by it.
//   - scope is the jsonb anchor list, NULL for the whole tenant, as iam_memberships.scope.
//
// No gorm.Model and no deleted_at: an account is removed, not soft-deleted.
type serviceAccountSnapshot struct {
	ID          uint `gorm:"primarykey"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
	Name        sql.NullString `gorm:"size:128"`
	Description sql.NullString `gorm:"size:1024"`
	TenantId    string         `gorm:"not null;size:128;uniqueIndex:idx_iam_service_accounts_tenant_token,priority:1"`
	Token       string         `gorm:"not null;size:128;uniqueIndex:idx_iam_service_accounts_tenant_token,priority:2"`
	ClientId    string         `gorm:"not null;size:64;uniqueIndex:idx_iam_service_accounts_client_id"`
	Enabled     bool           `gorm:"not null"`
	Scope       []string       `gorm:"type:jsonb;serializer:json"`
	CreatedBy   string         `gorm:"size:256"`
}

func (serviceAccountSnapshot) TableName() string { return "iam_service_accounts" }

// serviceAccountRoleSnapshot is the account→role join. Both foreign keys CASCADE: an
// assignment means nothing without either end. (Deleting a tenant's own role while an
// account holds it is refused above the database, as it is for members; the cascade is what
// clears the join when the operator deletes a catalogue role or an account is removed.)
type serviceAccountRoleSnapshot struct {
	ServiceAccountID uint                    `gorm:"primaryKey"`
	ServiceAccount   *serviceAccountSnapshot `gorm:"foreignKey:ServiceAccountID;constraint:OnDelete:CASCADE"`
	RoleID           uint                    `gorm:"primaryKey"`
	Role             *serviceAccountRoleRef  `gorm:"foreignKey:RoleID;constraint:OnDelete:CASCADE"`
}

func (serviceAccountRoleSnapshot) TableName() string { return "iam_service_account_roles" }

// serviceAccountSecretSnapshot is one client secret of an account. digest is the keyed
// digest of the secret — never the secret — and hint its last four characters. expires_at
// is NOT NULL: every secret expires. The account foreign key CASCADES.
type serviceAccountSecretSnapshot struct {
	ID               uint `gorm:"primarykey"`
	CreatedAt        time.Time
	ServiceAccountID uint                    `gorm:"not null;index:idx_iam_service_account_secrets_account"`
	ServiceAccount   *serviceAccountSnapshot `gorm:"foreignKey:ServiceAccountID;constraint:OnDelete:CASCADE"`
	Digest           string                  `gorm:"not null;size:128"`
	Hint             string                  `gorm:"not null;size:8"`
	ExpiresAt        time.Time               `gorm:"not null"`
	LastUsedAt       *time.Time
}

func (serviceAccountSecretSnapshot) TableName() string { return "iam_service_account_secrets" }

// NewServiceAccountsMigration creates a tenant's service accounts, their role assignments
// and their secrets. Individually re-runnable: AutoMigrate creates each table when it is
// absent and converges nothing when it is present.
func NewServiceAccountsMigration() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261022120000",
		Migrate: func(tx *gorm.DB) error {
			return tx.AutoMigrate(&serviceAccountSnapshot{}, &serviceAccountRoleSnapshot{}, &serviceAccountSecretSnapshot{})
		},
		Rollback: func(tx *gorm.DB) error {
			return tx.Migrator().DropTable("iam_service_account_secrets", "iam_service_account_roles", "iam_service_accounts")
		},
	}
}
