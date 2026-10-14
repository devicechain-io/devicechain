// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package iam

import (
	"fmt"
	"time"

	"github.com/devicechain-io/dc-microservice/secrets"
)

// The live models for external-IdP single sign-on. They describe the tables
// NewSsoConnectionsMigration creates (schema/migration_sso.go) and are NOT what that
// migration is built from.
//
// No gorm.Model and no DeletedAt anywhere here: every delete is a HARD delete, because a
// soft-deleted row would keep its UNIQUE slot (a removed domain could not move to another
// connection; a removed link could not be re-created).
//
// None of these types declares a relation field. The foreign keys live in the migration,
// where they are verified against a database that enforces them; the store reads related
// rows with explicit queries, so a model never silently pulls another table in.

// IdpKind says whose identity provider a connection is.
type IdpKind string

const (
	// IdpKindTenant serves one or more tenants' own people. A tenant connection can never
	// reach an identity that holds a system role.
	IdpKindTenant IdpKind = "tenant"
	// IdpKindInstance is the operator's own provider, linked to identities only by explicit
	// superuser action and never auto-provisioning.
	IdpKindInstance IdpKind = "instance"
)

// Valid reports whether k is a known kind.
func (k IdpKind) Valid() bool { return k == IdpKindTenant || k == IdpKindInstance }

// DefaultSessionMaxAgeSeconds is the session ceiling a connection starts with: 12 hours,
// anchored on our own callback time.
const DefaultSessionMaxAgeSeconds = 12 * 60 * 60

// DiscoverySnapshot is a provider's discovery document as fetched and shown to the
// superuser. The sign-in flow dials ONLY the endpoints recorded here; it never fetches
// discovery live, so a provider that later moves an endpoint cannot redirect a
// sign-in without a fresh, explicitly approved snapshot.
type DiscoverySnapshot struct {
	Issuer                string    `json:"issuer"`
	AuthorizationEndpoint string    `json:"authorization_endpoint"`
	TokenEndpoint         string    `json:"token_endpoint"`
	JwksURI               string    `json:"jwks_uri"`
	EndSessionEndpoint    string    `json:"end_session_endpoint,omitempty"`
	FetchedAt             time.Time `json:"fetched_at"`
}

// SameEndpoints reports whether two snapshots name the same issuer and endpoints. The
// fetch time is not part of the answer: re-fetching an unchanged document must not
// demand a new approval.
func (s DiscoverySnapshot) SameEndpoints(o DiscoverySnapshot) bool {
	return s.Issuer == o.Issuer &&
		s.AuthorizationEndpoint == o.AuthorizationEndpoint &&
		s.TokenEndpoint == o.TokenEndpoint &&
		s.JwksURI == o.JwksURI &&
		s.EndSessionEndpoint == o.EndSessionEndpoint
}

// IdpConnection is one configured external identity provider.
//
// The client secret is NEVER a field: it is sealed in the secret store under
// IdpConnectionSecretRef(ID), and ClientSecretRef records only that name.
type IdpConnection struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time
	UpdatedAt time.Time

	Token   string  `gorm:"not null;size:128;uniqueIndex:idx_iam_idp_connections_token"`
	Name    string  `gorm:"not null;size:256"`
	Kind    IdpKind `gorm:"not null;size:16"`
	Enabled bool    `gorm:"not null"`

	Issuer          string `gorm:"not null;size:512"`
	ClientID        string `gorm:"not null;size:256"`
	ClientSecretRef string `gorm:"not null;size:256"`

	Scopes         []string          `gorm:"serializer:json"`
	EmailClaim     string            `gorm:"not null;size:64"`
	GroupsClaim    string            `gorm:"not null;size:64"`
	RequiredClaims map[string]string `gorm:"serializer:json"`
	RequiredAmr    []string          `gorm:"serializer:json"`

	JitEnabled         bool     `gorm:"not null"`
	DefaultTenantRoles []string `gorm:"serializer:json"`

	SessionMaxAgeSeconds int `gorm:"not null"`

	// ProviderSignOut is the administrator's choice to end the person's session at the
	// provider too when they sign out (OpenID Connect RP-initiated logout), through the
	// approved snapshot's end_session_endpoint. Off unless switched on: signing out at the
	// provider signs the person out of every application it serves.
	ProviderSignOut bool `gorm:"not null;default:false"`

	// CaBundle is PEM: extra trust anchors (CERTIFICATE blocks only, canonicalised on write)
	// for this provider's TLS certificate, trusted beside the system roots. Public
	// certificates, so it is a plain column, not a secret. Empty means system roots only.
	CaBundle string `gorm:"type:text;not null;default:''"`

	DiscoverySnapshot   *DiscoverySnapshot `gorm:"serializer:json"`
	DiscoveryApprovedAt *time.Time
	DiscoveryApprovedBy string `gorm:"not null;size:256"`
}

func (IdpConnection) TableName() string { return "iam_idp_connections" }

// AuditLabel names the connection in the audit journal by its token.
func (c IdpConnection) AuditLabel() string { return c.Token }

// IdpConnectionSecretRef is the INSTANCE-scoped secret-store handle for a connection's
// client secret, keyed by the connection's immutable id (not its token) so a token rename
// keeps the same entry bound. Instance-scoped because connections are instance-global.
func IdpConnectionSecretRef(id uint) secrets.SecretRef {
	return secrets.SecretRef{Scope: secrets.ScopeInstance, Name: fmt.Sprintf("sso/connection/%d", id)}
}

// IdpConnectionTenant records that a kind=tenant connection serves a tenant.
type IdpConnectionTenant struct {
	ConnectionID uint   `gorm:"primaryKey;autoIncrement:false"`
	TenantId     string `gorm:"primaryKey;size:128"`
}

func (IdpConnectionTenant) TableName() string { return "iam_idp_connection_tenants" }

// IdpConnectionDomain asserts that a connection speaks for an email domain. Domain is
// stored in the normalised form identity.NormalizeDomain returns.
type IdpConnectionDomain struct {
	ID uint `gorm:"primarykey"`

	Domain       string    `gorm:"not null;size:253;uniqueIndex:idx_iam_idp_connection_domains_domain"`
	ConnectionID uint      `gorm:"not null;index:idx_iam_idp_connection_domains_connection"`
	VerifiedAt   time.Time `gorm:"not null"`
	VerifiedBy   string    `gorm:"not null;size:256"`
}

func (IdpConnectionDomain) TableName() string { return "iam_idp_connection_domains" }

// AuditLabel names the domain in the audit journal.
func (d IdpConnectionDomain) AuditLabel() string { return d.Domain }

// IdpRoleMapping maps an IdP group value to a tenant role within one tenant a connection
// serves. Nothing writes it yet; the model exists so the table the migration created has
// a typed reader the role-delete guard can count through.
type IdpRoleMapping struct {
	ID uint `gorm:"primarykey"`

	ConnectionID uint   `gorm:"not null;uniqueIndex:idx_iam_idp_role_mappings_unique,priority:1"`
	TenantId     string `gorm:"not null;size:128;uniqueIndex:idx_iam_idp_role_mappings_unique,priority:2"`
	GroupValue   string `gorm:"not null;size:512;uniqueIndex:idx_iam_idp_role_mappings_unique,priority:3"`
	RoleID       uint   `gorm:"not null;uniqueIndex:idx_iam_idp_role_mappings_unique,priority:4"`
}

func (IdpRoleMapping) TableName() string { return "iam_idp_role_mappings" }

// IdentityLink binds an external (connection, subject) to an Identity. Created only by an
// explicit act; nothing links by email.
//
// The two unique indexes are the migration's (schema/migration_sso.go), repeated here so
// every store built from the live models enforces them too: (connection, subject) is what
// makes the loser of two concurrent first sign-ins fail instead of linking a second
// identity, and (connection, identity) refuses a second subject for a linked identity.
type IdentityLink struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time

	IdentityID   uint   `gorm:"not null;uniqueIndex:idx_iam_identity_links_connection_identity,priority:2"`
	ConnectionID uint   `gorm:"not null;uniqueIndex:idx_iam_identity_links_connection_subject,priority:1;uniqueIndex:idx_iam_identity_links_connection_identity,priority:1"`
	Subject      string `gorm:"not null;size:512;uniqueIndex:idx_iam_identity_links_connection_subject,priority:2"`
	CreatedBy    string `gorm:"not null;size:256"`
}

func (IdentityLink) TableName() string { return "iam_identity_links" }

// SsoLinkRequest is a refused sign-in waiting for a superuser's decision. It grants
// nothing; it records that someone proved control of an email that already belongs to an
// identity with no link.
type SsoLinkRequest struct {
	ID        uint `gorm:"primarykey"`
	CreatedAt time.Time

	ConnectionID uint   `gorm:"not null;uniqueIndex:idx_iam_sso_link_requests_connection_subject,priority:1"`
	Subject      string `gorm:"not null;size:512;uniqueIndex:idx_iam_sso_link_requests_connection_subject,priority:2"`
	Email        string `gorm:"not null;size:256"`
	ClaimsDigest string `gorm:"not null;size:128"`
}

func (SsoLinkRequest) TableName() string { return "iam_sso_link_requests" }
