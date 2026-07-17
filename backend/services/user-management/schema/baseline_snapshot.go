// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"database/sql"
	"time"

	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// The structures the BASELINE migration creates, captured at the point in time it was
// written. They are private to this package and are NOT the live models.
//
// THAT SEPARATION IS THE WHOLE POINT, so state it plainly: a migration's structs are a
// SNAPSHOT; the live models in iam/, model/ and settings/ are the CURRENT incarnation of
// the same datatypes. Today they describe the same tables. They are still different
// things, and the difference is time — the live model moves whenever the product does,
// and this must not move at all.
//
// The baseline used to AutoMigrate the live models directly, and that made it impossible
// to stack a migration on top of it. Adding one field to iam.TenantTier would have
// changed what THIS migration creates — editing an already-applied migration from
// another file, with nothing in the diff that looks like a migration change. A fresh
// install would create the new column here, then the migration adding it would hit
// "column already exists" and wedge, while an existing database applied it cleanly and
// showed nothing wrong. That is not hypothetical: it is exactly how the 14-migration
// chain this baseline replaced broke (an early migration silently acquired a later
// slice's NOT NULL FK), and collapsing the chain only hid it — a chain of length one has
// nothing to wedge against. The disease was dormant, not gone.
//
// So the rule these types exist to enforce: A MIGRATION CONTAINS ITS OWN SHAPES. Never
// reference a live model from a migration, and never "update" the shapes below to match
// one. When a model gains a column, the column arrives in a NEW migration; these stay as
// they are. They are wrong about the current schema by design, and that is what makes
// them correct about this migration.
//
// Nothing here carries a version suffix. There is no lineage to number: at the GA squash
// every migration collapses back into one baseline, so a "V2" would name a thing that
// never exists. These are simply what the baseline creates.
//
// They are also self-contained down to the mixins: gorm.Model and rdb.NamedEntity are
// INLINED rather than embedded. Embedding rdb.NamedEntity would leave a core change
// silently rewriting this migration — the same bug one level down, and one that would
// arrive from a PR that never touched user-management. Field order matches the embedded
// layout exactly, because physical column order is part of the schema this reproduces.

// signingKey is the instance JWT signing key (ADR-008).
//
// IT DELIBERATELY HAS NO TableName METHOD, and it is the only snapshot type here that
// does not. That is not an oversight — it reproduces the live model, which has none
// either, and the difference is load-bearing. core/rdb pins the gorm NamingStrategy's
// TablePrefix to the functional area ("user-management."), and that prefix is applied
// only when gorm DERIVES a table name. An explicit TableName bypasses it, so the table
// resolves through search_path instead and its indexes come out unprefixed. The two paths
// therefore name indexes differently:
//
//	no TableName  → idx_user-management_signing_keys_active   (what this table has)
//	TableName()   → idx_iam_roles_deleted_at                  (what every other one has)
//
// Adding a TableName here would silently rename all three of this table's indexes. The
// harness caught exactly that on the first run of this rewrite. Gorm derives
// "signing_keys" from this type's name, so renaming the type retargets the migration —
// which is the hazard TableName exists to remove, and it cannot be used here. Leave both
// the absence and the type name alone.
type signingKey struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`

	Active        bool       `gorm:"not null;default:true;index"`
	PrivateKeyPem string     `gorm:"not null;type:text"`
	PublicKeyPem  string     `gorm:"not null;type:text"`
	RetiredAt     *time.Time `gorm:"index"`
}

// TableName is pinned on every snapshot type below, mirroring the live models. Without
// it gorm derives the table from the Go type name, so renaming a snapshot struct would
// silently retarget the migration — see signingKey above for the one case where pinning
// it is itself the bug.
//
// systemSetting is the operator's instance settings store. It has no gorm.Model: its
// primary key is the setting key.
type systemSetting struct {
	Key       string         `gorm:"primaryKey;size:190"`
	Value     datatypes.JSON `gorm:"not null"`
	UpdatedAt time.Time
	UpdatedBy string `gorm:"size:190"`
}

func (systemSetting) TableName() string { return "system_settings" }

// role is the global, named bundle of authorities (ADR-033). Scope is a plain string
// here: the live model types it as iam.RoleScope, and a snapshot may not depend on a
// type another package can redefine. Both produce the same varchar(16) column.
type role struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`

	Name        sql.NullString `gorm:"size:128"`
	Description sql.NullString `gorm:"size:1024"`

	Scope       string   `gorm:"uniqueIndex:idx_iam_role_scope_token;not null;size:16"`
	Token       string   `gorm:"uniqueIndex:idx_iam_role_scope_token;not null;size:128"`
	Authorities []string `gorm:"serializer:json"`
}

func (role) TableName() string { return "iam_roles" }

// identity is the global, email-keyed principal (ADR-033).
//
// The association tags are EXPLICIT rather than inferred. gorm derives a join table's
// columns from the Go type name, so an inferred `identity_id` would silently become
// `identity_v2_id` the day someone renames this struct — a schema change with no schema
// edit. Naming the columns pins them to the table instead of to the type.
type identity struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`

	Email        string `gorm:"uniqueIndex;not null;size:256"`
	FirstName    string `gorm:"size:128"`
	LastName     string `gorm:"size:128"`
	Enabled      bool   `gorm:"not null;default:true"`
	PasswordHash string `gorm:"not null;size:256"`

	SystemRoles []role       `gorm:"many2many:iam_identity_system_roles;foreignKey:ID;joinForeignKey:IdentityID;references:ID;joinReferences:RoleID"`
	Memberships []membership `gorm:"foreignKey:IdentityID"`
}

func (identity) TableName() string { return "iam_identities" }

// membership binds an identity to one tenant with tenant-scoped roles (ADR-033).
type membership struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`

	IdentityID uint   `gorm:"uniqueIndex:idx_iam_membership_identity_tenant;not null"`
	TenantId   string `gorm:"uniqueIndex:idx_iam_membership_identity_tenant;not null;size:128"`
	Enabled    bool   `gorm:"not null;default:true"`

	TenantRoles []role `gorm:"many2many:iam_membership_tenant_roles;foreignKey:ID;joinForeignKey:MembershipID;references:ID;joinReferences:RoleID"`
}

func (membership) TableName() string { return "iam_memberships" }

// tenantTier is the operator-defined packaging entity (ADR-065).
type tenantTier struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`

	Name        sql.NullString `gorm:"size:128"`
	Description sql.NullString `gorm:"size:1024"`

	Token  string         `gorm:"uniqueIndex;not null;size:128"`
	Config map[string]any `gorm:"serializer:json"`
}

func (tenantTier) TableName() string { return "iam_tenant_tiers" }

// tenant is the control-plane tenant record (ADR-033), with its required tier FK
// (ADR-065) and the per-tenant governance / branding / AI overrides that have graduated
// out of the config blob into their own columns.
type tenant struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`

	Name        sql.NullString `gorm:"size:128"`
	Description sql.NullString `gorm:"size:1024"`

	Token   string         `gorm:"uniqueIndex;not null;size:128"`
	Enabled bool           `gorm:"not null;default:true"`
	Config  map[string]any `gorm:"serializer:json"`

	// The relationship field stays named Tier: gorm builds the FK constraint name from
	// the table plus this field (fk_iam_tenants_tier), so renaming it renames a
	// constraint in an already-applied migration.
	TierID uint        `gorm:"not null;index"`
	Tier   *tenantTier `gorm:"foreignKey:TierID;constraint:OnDelete:RESTRICT"`

	IngestMessagesPerSecond *float64
	IngestBurst             *int

	OutboundMessagesPerSecond *float64
	OutboundBurst             *int

	BrandingTitle         *string
	BrandingLogo          *string
	BrandingLogoMaxHeight *int
	BrandingPrimary       *string
	BrandingBackground    *string
	BrandingForeground    *string
	BrandingAccent        *string

	AiExternalEnabled *bool

	AiInferenceRequestsPerMinute *float64
	AiInferenceBurst             *int
}

func (tenant) TableName() string { return "iam_tenants" }

// oauthClient is a registered OAuth 2.1 client (ADR-047).
type oauthClient struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`

	Name        sql.NullString `gorm:"size:128"`
	Description sql.NullString `gorm:"size:1024"`

	ClientId     string   `gorm:"uniqueIndex;not null;size:128"`
	RedirectURIs []string `gorm:"serializer:json"`
	Scopes       []string `gorm:"serializer:json"`
	Enabled      bool     `gorm:"not null;default:true"`
	SecretHash   string   `gorm:"size:100"`
}

func (oauthClient) TableName() string { return "iam_oauth_clients" }
