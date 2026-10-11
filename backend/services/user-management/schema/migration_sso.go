// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package schema

import (
	"time"

	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

// The storage for external-IdP single sign-on: the per-instance IdP connections, the
// tenants each serves, the email domains it has been asserted for, the group->role
// mappings, the explicit identity links, and the queue of sign-ins that were refused
// because they collided with an existing identity.
//
// ONE migration carries every table and column the feature needs, including the ones
// whose writers arrive in later slices (the role-mapping table, the session ceiling, the
// tenant-wide SSO requirement). THE SETTING lands in the slice that enforces it; the
// COLUMN lands once. Splitting the DDL across slices would stack several appended
// migrations onto a chain that every released instance replays from the top, for tables
// nothing could read yet.
//
// These are SNAPSHOT types, never the live models (see baseline_snapshot.go for the
// rule and the failure it prevents). They carry no gorm.Model and no DeletedAt: every
// delete in this feature is a HARD delete. A soft-deleted row keeps its UNIQUE slot, so
// a removed domain could not be re-added to another connection and a removed link could
// not be re-created — the same reason iam.Store deletes identities Unscoped.
//
// 🔴 EVERY TableName IS LOAD-BEARING. Without one gorm derives the table from the Go type
// name and silently creates a different table. TestTheSsoMigrationCreatesExactlyItsTables
// is the guard.

// ssoIdentityRef and ssoRoleRef are references to tables this migration does not own:
// the primary key only, so a foreign key can name them. AutoMigrate on an existing table
// with a struct this thin adds nothing and drops nothing.
type ssoIdentityRef struct {
	ID uint `gorm:"primarykey"`
}

func (ssoIdentityRef) TableName() string { return "iam_identities" }

type ssoRoleRef struct {
	ID uint `gorm:"primarykey"`
}

func (ssoRoleRef) TableName() string { return "iam_roles" }

// idpConnection is one configured external identity provider.
//
// Kind is "tenant" (serves the listed tenants and can never reach an identity holding a
// system role) or "instance" (the operator's own IdP, linked only by explicit superuser
// action). The client secret is NEVER a column: ClientSecretRef holds only the name of
// the sealed secret-store entry.
//
// DiscoverySnapshot is the provider's discovery document as the superuser approved it;
// the sign-in flow dials only the endpoints recorded here, never a live discovery fetch.
type idpConnection struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time

	Token   string `gorm:"not null;size:128;uniqueIndex:idx_iam_idp_connections_token"`
	Name    string `gorm:"not null;size:256"`
	Kind    string `gorm:"not null;size:16"`
	Enabled bool   `gorm:"not null;default:false"`

	Issuer          string `gorm:"not null;size:512"`
	ClientID        string `gorm:"not null;size:256"`
	ClientSecretRef string `gorm:"not null;size:256;default:''"`

	Scopes         []string          `gorm:"serializer:json"`
	EmailClaim     string            `gorm:"not null;size:64;default:'email'"`
	GroupsClaim    string            `gorm:"not null;size:64;default:''"`
	RequiredClaims map[string]string `gorm:"serializer:json"`
	RequiredAmr    []string          `gorm:"serializer:json"`

	JitEnabled         bool     `gorm:"not null;default:false"`
	DefaultTenantRoles []string `gorm:"serializer:json"`

	// The session ceiling in seconds, anchored on our own callback time. 12 hours.
	SessionMaxAgeSeconds int `gorm:"not null;default:43200"`

	DiscoverySnapshot   *ssoDiscoverySnapshot `gorm:"serializer:json"`
	DiscoveryApprovedAt *time.Time
	DiscoveryApprovedBy string `gorm:"not null;size:256;default:''"`
}

func (idpConnection) TableName() string { return "iam_idp_connections" }

// ssoDiscoverySnapshot is the JSON stored in idpConnection.DiscoverySnapshot. It is
// declared here, in the snapshot, because the migration must not depend on a type
// another package can change.
type ssoDiscoverySnapshot struct {
	Issuer                string    `json:"issuer"`
	AuthorizationEndpoint string    `json:"authorization_endpoint"`
	TokenEndpoint         string    `json:"token_endpoint"`
	JwksURI               string    `json:"jwks_uri"`
	EndSessionEndpoint    string    `json:"end_session_endpoint,omitempty"`
	FetchedAt             time.Time `json:"fetched_at"`
}

// idpConnectionTenant is the set of tenants a kind=tenant connection serves. The pair is
// the primary key, and the pair is what the role-mapping table's composite foreign key
// references: a mapping can never name a tenant its connection does not serve.
//
// TenantId (column tenant_id) is a plain reference, not a foreign key: tenant rows are
// released by the purge while their token is being reclaimed, and the purge sweeps this
// table by that column.
type idpConnectionTenant struct {
	ConnectionID uint   `gorm:"primaryKey;autoIncrement:false"`
	TenantId     string `gorm:"primaryKey;size:128"`

	Connection *idpConnection `gorm:"foreignKey:ConnectionID;constraint:OnDelete:CASCADE"`

	// 🔴 The composite foreign key from idpRoleMapping is declared HERE, as a has-many, and
	// not on the mapping as a belongs-to. Both columns of the pair exist on both tables, so
	// gorm cannot tell which side owns the key from the tags, and declared from the mapping
	// it picked the wrong one: the constraint landed on THIS table, referencing the mapping
	// table — which silently permits a mapping for a tenant the connection does not serve.
	// TestMappingRefusedForTenantNotServed is the guard.
	Mappings []idpRoleMapping `gorm:"foreignKey:ConnectionID,TenantId;references:ConnectionID,TenantId;constraint:OnDelete:CASCADE"`
}

func (idpConnectionTenant) TableName() string { return "iam_idp_connection_tenants" }

// idpConnectionDomain asserts that a connection speaks for an email domain. Domain is
// UNIQUE across all connections: one domain belongs to one IdP. Stored IDNA-normalised,
// lowercase ASCII (the validator's job, not the column's).
type idpConnectionDomain struct {
	ID uint `gorm:"primarykey"`

	Domain       string    `gorm:"not null;size:253;uniqueIndex:idx_iam_idp_connection_domains_domain"`
	ConnectionID uint      `gorm:"not null;index:idx_iam_idp_connection_domains_connection"`
	VerifiedAt   time.Time `gorm:"not null"`
	VerifiedBy   string    `gorm:"not null;size:256"`

	Connection *idpConnection `gorm:"foreignKey:ConnectionID;constraint:OnDelete:CASCADE"`
}

func (idpConnectionDomain) TableName() string { return "iam_idp_connection_domains" }

// idpRoleMapping maps one IdP group value to one tenant role within one tenant the
// connection serves. The composite foreign key into idpConnectionTenant (declared on that
// type) is what refuses a mapping for a tenant the connection does not serve; the RESTRICT
// foreign key to the role is what stops a role in use from being deleted out from under a
// mapping.
type idpRoleMapping struct {
	ID uint `gorm:"primarykey"`

	ConnectionID uint   `gorm:"not null;uniqueIndex:idx_iam_idp_role_mappings_unique,priority:1"`
	TenantId     string `gorm:"not null;size:128;uniqueIndex:idx_iam_idp_role_mappings_unique,priority:2"`
	GroupValue   string `gorm:"not null;size:512;uniqueIndex:idx_iam_idp_role_mappings_unique,priority:3"`
	RoleID       uint   `gorm:"not null;uniqueIndex:idx_iam_idp_role_mappings_unique,priority:4"`

	Role *ssoRoleRef `gorm:"foreignKey:RoleID;constraint:OnDelete:RESTRICT"`
}

func (idpRoleMapping) TableName() string { return "iam_idp_role_mappings" }

// identityLink binds an external (connection, subject) to an Identity. Links are only
// ever created explicitly. UNIQUE(connection, subject) means one external account maps
// to one identity; UNIQUE(connection, identity) refuses a second subject for an identity
// already linked on that connection, which is what an email-recycling takeover looks like.
type identityLink struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time

	IdentityID   uint   `gorm:"not null;uniqueIndex:idx_iam_identity_links_connection_identity,priority:2"`
	ConnectionID uint   `gorm:"not null;uniqueIndex:idx_iam_identity_links_connection_subject,priority:1;uniqueIndex:idx_iam_identity_links_connection_identity,priority:1"`
	Subject      string `gorm:"not null;size:512;uniqueIndex:idx_iam_identity_links_connection_subject,priority:2"`
	CreatedBy    string `gorm:"not null;size:256;default:''"`

	Identity   *ssoIdentityRef `gorm:"foreignKey:IdentityID;constraint:OnDelete:CASCADE"`
	Connection *idpConnection  `gorm:"foreignKey:ConnectionID;constraint:OnDelete:CASCADE"`
}

func (identityLink) TableName() string { return "iam_identity_links" }

// ssoLinkRequest is a sign-in that was refused because the verified email already belongs
// to an identity with no link, waiting for a superuser to approve or reject. It records
// that the attempt happened and who it claimed to be; it grants nothing.
type ssoLinkRequest struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time

	ConnectionID uint   `gorm:"not null;uniqueIndex:idx_iam_sso_link_requests_connection_subject,priority:1"`
	Subject      string `gorm:"not null;size:512;uniqueIndex:idx_iam_sso_link_requests_connection_subject,priority:2"`
	Email        string `gorm:"not null;size:256"`
	ClaimsDigest string `gorm:"not null;size:128;default:''"`

	Connection *idpConnection `gorm:"foreignKey:ConnectionID;constraint:OnDelete:CASCADE"`
}

func (ssoLinkRequest) TableName() string { return "iam_sso_link_requests" }

// ssoMembershipSnapshot adds the one column iam_memberships gains: the connection that
// manages the membership, or NULL for a manual one.
//
// There is NO foreign key, on purpose. When a connection is deleted its managed
// memberships are DISABLED and keep the id: deleting them, or nulling the column, would
// silently turn IdP-granted roles into manual grants nobody reviews.
type ssoMembershipSnapshot struct {
	ID        uint  `gorm:"primarykey"`
	ManagedBy *uint `gorm:"index:idx_iam_memberships_managed_by"`
}

func (ssoMembershipSnapshot) TableName() string { return "iam_memberships" }

// ssoTenantSnapshot adds the tenant-wide "sign-in must go through SSO" flag. The column
// lands here with the rest of the DDL; the setting that writes it, and the check that
// reads it, arrive together in the slice that enforces it.
type ssoTenantSnapshot struct {
	ID          uint `gorm:"primarykey"`
	SsoEnforced bool `gorm:"not null;default:false"`
}

func (ssoTenantSnapshot) TableName() string { return "iam_tenants" }

// NewSsoConnectionsMigration creates the single-sign-on tables and the two columns that
// join them to memberships and tenants.
//
// Order matters only for foreign keys: connections before everything that references
// them. AutoMigrate is individually re-runnable (creates what is absent, no-op on what is
// present), which the chain requires since migrations run with UseTransaction:false and
// replay from the top after a failure.
func NewSsoConnectionsMigration() *gormigrate.Migration {
	return &gormigrate.Migration{
		ID: "20261011120000",
		Migrate: func(tx *gorm.DB) error {
			return tx.AutoMigrate(
				&idpConnection{},
				&idpConnectionTenant{},
				&idpConnectionDomain{},
				&idpRoleMapping{},
				&identityLink{},
				&ssoLinkRequest{},
				&ssoMembershipSnapshot{},
				&ssoTenantSnapshot{},
			)
		},
		Rollback: func(tx *gorm.DB) error {
			if err := dropTables(tx, []string{
				"iam_sso_link_requests", "iam_identity_links", "iam_idp_role_mappings",
				"iam_idp_connection_domains", "iam_idp_connection_tenants", "iam_idp_connections",
			}); err != nil {
				return err
			}
			if err := tx.Migrator().DropColumn(&ssoMembershipSnapshot{}, "managed_by"); err != nil {
				return err
			}
			return tx.Migrator().DropColumn(&ssoTenantSnapshot{}, "sso_enforced")
		},
	}
}
