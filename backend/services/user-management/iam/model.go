// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// Package iam holds the multi-tenant identity model (ADR-033): a global,
// email-keyed Identity that can hold N per-tenant Memberships, and a global Role
// catalog whose entries are scoped system (assigned to identities, gate the
// instance/admin API) or tenant (assigned to memberships, gate the data plane).
//
// It replaced the legacy tenant-bound user/role model (now removed, ADR-033). The
// tables are prefixed `iam_` — originally to coexist with the legacy
// `users`/`roles`/`user_roles` tables during the cutover, retained as a clear
// namespace now that those are gone.
//
// Like the SigningKey, these entities are instance-global — they are NOT
// rdb.TenantScoped, so the fail-closed tenant callback does not filter them; the
// store reaches them through the system context (see store.go).
package iam

import (
	"strings"

	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// NormalizeEmail lower-cases and trims an email so identity lookups and the
// uniqueness constraint are case-insensitive. The single normalizer shared by the
// auth path (login) and the admin path (identity CRUD) so the two cannot diverge.
func NormalizeEmail(e string) string { return strings.ToLower(strings.TrimSpace(e)) }

// RoleScope decides where a role may be assigned and which token tier carries
// its authorities (ADR-033): system roles attach to an Identity and ride the
// identity token (admin API); tenant roles attach to a Membership and ride the
// tenant token (data plane). The scope is what keeps the two assignment surfaces
// from leaking into each other.
type RoleScope string

const (
	ScopeSystem RoleScope = "system"
	ScopeTenant RoleScope = "tenant"
)

// Valid reports whether s is a known scope.
func (s RoleScope) Valid() bool { return s == ScopeSystem || s == ScopeTenant }

// SuperuserRoleToken is the well-known system role seeded with the instance: it
// grants the super-authority (auth.AuthorityAll) and lets its holder enter the
// admin console and act in any tenant. Its authorities are set at seed time
// (Phase 2) so this package need not import the authority vocabulary.
const SuperuserRoleToken = "superuser"

// TenantAdminRoleToken is the well-known tenant role granting full authority
// within a tenant — seeded for the superuser's scaffold membership.
const TenantAdminRoleToken = "tenant-admin"

// ViewerRoleToken is the well-known tenant role granting read access to all
// domain objects. Its authorities mirror the read-only baseline every enabled
// tenant member receives by default (see identity.viewerAuthorities); it is kept
// in the catalog so the access is visible/assignable in the admin console.
const ViewerRoleToken = "viewer"

// Role is a globally-defined, named bundle of authorities. Uniqueness is the
// composite (scope, token) — a token like "admin" can exist once per scope — so
// the catalog can hold both a system "superuser" and tenant roles without
// collision. Authorities is a JSON-encoded set of auth.Authority strings,
// validated against the code vocabulary on write (same as the legacy Role).
type Role struct {
	gorm.Model
	rdb.NamedEntity

	Scope       RoleScope `gorm:"uniqueIndex:idx_iam_role_scope_token;not null;size:16"`
	Token       string    `gorm:"uniqueIndex:idx_iam_role_scope_token;not null;size:128"`
	Authorities []string  `gorm:"serializer:json"`
}

func (Role) TableName() string { return "iam_roles" }

// Identity is a global principal keyed by email — the person, independent of any
// tenant. It holds N Memberships (one per tenant it can act in) and zero or more
// system roles (instance-level capabilities, e.g. superuser). The bcrypt hash is
// never exposed through the API.
type Identity struct {
	gorm.Model

	Email        string `gorm:"uniqueIndex;not null;size:256"`
	FirstName    string `gorm:"size:128"`
	LastName     string `gorm:"size:128"`
	Enabled      bool   `gorm:"not null;default:true"`
	PasswordHash string `gorm:"not null;size:256" json:"-"`

	// System (instance-scoped) roles, e.g. superuser. Their authorities ride the
	// identity token and gate the admin API.
	SystemRoles []Role `gorm:"many2many:iam_identity_system_roles;"`
	// The tenants this identity can act in, with per-tenant roles.
	Memberships []Membership
}

func (Identity) TableName() string { return "iam_identities" }

// Membership binds an Identity to a single tenant with a set of tenant-scoped
// roles (ADR-033). A person holds at most one membership per tenant. TenantId is
// a plain reference column, not rdb.TenantScoped: memberships are control-plane
// data the admin manages across tenants, reached through the system context.
type Membership struct {
	gorm.Model

	IdentityID uint   `gorm:"uniqueIndex:idx_iam_membership_identity_tenant;not null"`
	TenantId   string `gorm:"uniqueIndex:idx_iam_membership_identity_tenant;not null;size:128"`
	Enabled    bool   `gorm:"not null;default:true"`

	// Tenant (data-plane) roles for this identity in this tenant. Their
	// authorities ride the tenant token and gate the data services.
	TenantRoles []Role `gorm:"many2many:iam_membership_tenant_roles;"`
}

func (Membership) TableName() string { return "iam_memberships" }

// Tenant is the control-plane record of a tenant (ADR-033). The data plane scopes
// purely by the tenant id string (the JWT claim, the rows' TenantId, the NATS
// subject), so a tenant is a registry entry + per-tenant config, NOT a
// provisioned resource — this replaces the former DeviceChainTenant CRD, whose
// reconciler only maintained an (empty) ConfigMap that nothing read. Token is the
// tenant id used everywhere. Config is freeform JSON for now; structured concerns
// (quotas, retention) graduate to their own FK tables later.
type Tenant struct {
	gorm.Model
	rdb.NamedEntity

	Token   string         `gorm:"uniqueIndex;not null;size:128"`
	Enabled bool           `gorm:"not null;default:true"`
	Config  map[string]any `gorm:"serializer:json"`

	// Per-tenant governance overrides (ADR-023). A nil field means "inherit the
	// platform default"; a set value raises or lowers the ceiling for this tenant
	// only. Fail-safe by construction: the enforcing service treats a nil (or
	// absent) limit as the platform default, never as unlimited, so a tenant with
	// no override is still metered. These are the first of a family of governance
	// knobs (retention windows, API limits) that will land alongside them.
	IngestMessagesPerSecond *float64
	IngestBurst             *int

	// Per-tenant OUTBOUND governance overrides (ADR-060 SD-3): the egress rate for
	// REACT-driven outbound connector actions (httpCall/publish), enforced by the
	// outbound-connectors service. A distinct dimension from ingest — a tenant may
	// ingest heavily yet fan out few outbound calls, or the reverse — with the same
	// fail-safe semantics: nil means "inherit the platform default", never
	// unlimited, and a set value must be positive.
	OutboundMessagesPerSecond *float64
	OutboundBurst             *int

	// Per-tenant white-labeling overrides (ADR-038 Phase 2). Each is a nullable
	// override on the same cascading-column pattern as the governance knobs above:
	// a nil field means "inherit" — the operator's `branding.default` system
	// setting, then the code default, then (for a color/logo still nil at every
	// tier) the console's built-in look. Branding is never in the JWT (ADR-038 §1);
	// it is resolved onto the self-scoped `tenant` query. Colors are hex #rrggbb;
	// BrandingLogo is an https URL or a bounded raster data: URI (validated in the
	// branding package). BrandingLogoMaxHeight caps the chip/sidebar render in px.
	BrandingTitle         *string
	BrandingLogo          *string
	BrandingLogoMaxHeight *int
	BrandingPrimary       *string
	BrandingBackground    *string
	BrandingForeground    *string
	BrandingAccent        *string
}

func (Tenant) TableName() string { return "iam_tenants" }

// OAuthClient is a registered OAuth 2.1 client the Authorization Server validates
// the authorization-code flow against (ADR-047). Instance-global control-plane
// data (like the role/tenant catalog), reached through the system context — an
// OAuth client is a platform-level registration, not tenant-scoped; the tenant is
// chosen per-grant at the authorize step. Every v1 client is a PUBLIC client
// (token_endpoint_auth_method=none) that proves possession via PKCE, so there is
// deliberately no client secret to store. RedirectURIs is the exact-match
// allowlist the authorize endpoint checks; Scopes is the set of scopes this client
// may request (each a member of auth.SupportedScopes).
type OAuthClient struct {
	gorm.Model
	rdb.NamedEntity

	ClientId     string   `gorm:"uniqueIndex;not null;size:128"`
	RedirectURIs []string `gorm:"serializer:json"`
	Scopes       []string `gorm:"serializer:json"`
	Enabled      bool     `gorm:"not null;default:true"`
}

func (OAuthClient) TableName() string { return "iam_oauth_clients" }

// AuditLabel implements rdb.AuditLabeler: label an audited iam row with its
// human-facing, non-sensitive identifier — a role/tenant token, a client_id, or
// an identity's email — so an audit view can show "iam_roles operator" rather than
// "#3". (Membership has no single natural label and is left to fall back to its
// pk.)
func (r Role) AuditLabel() string        { return r.Token }
func (t Tenant) AuditLabel() string      { return t.Token }
func (c OAuthClient) AuditLabel() string { return c.ClientId }
func (i Identity) AuditLabel() string    { return i.Email }

// SystemAuthorities is the deduped union of the identity's system roles'
// authorities — the authorities carried on its identity token. Pure: it reads
// the already-loaded SystemRoles.
func (i *Identity) SystemAuthorities() []string { return unionAuthorities(i.SystemRoles) }

// TenantAuthorities is the deduped union of the membership's tenant roles'
// authorities — the authorities carried on the tenant token minted for it.
func (m *Membership) TenantAuthorities() []string { return unionAuthorities(m.TenantRoles) }

// unionAuthorities flattens roles' authorities into a deduped, order-preserving
// slice.
func unionAuthorities(roles []Role) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0)
	for _, r := range roles {
		for _, a := range r.Authorities {
			if _, ok := seen[a]; ok {
				continue
			}
			seen[a] = struct{}{}
			out = append(out, a)
		}
	}
	return out
}
