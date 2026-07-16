// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package iam

import (
	"context"
	"fmt"

	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"gorm.io/gorm"
)

// Store is the persistence layer for the iam model. The entities are
// instance-global (not tenant-scoped), so every access goes through the system
// context: reads need no tenant filter, and writes must carry it to pass the
// fail-closed tenant guard (ADR-015) — the same lane the SigningKey uses.
type Store struct {
	db *rdb.RdbManager
}

// NewStore wraps an RdbManager.
func NewStore(db *rdb.RdbManager) *Store { return &Store{db: db} }

// sys returns a gorm handle scoped to the instance-global system context.
func (s *Store) sys(ctx context.Context) *gorm.DB {
	return s.db.DB(core.WithSystemContext(ctx))
}

// AuditEvents reads this instance's user-management audit journal — the auth
// events (login/login_failed/refresh) plus the identity/role/tenant/membership
// administration mutations. It runs in the system context so the read is
// instance-wide (not tenant-scoped): the admin console is cross-tenant and must
// surface tenant-less auth events and every tenant's administrative changes. The
// query itself is the core-owned RdbManager.AuditEvents helper.
func (s *Store) AuditEvents(ctx context.Context, criteria rdb.AuditEventSearchCriteria) (*rdb.AuditEventSearchResults, error) {
	return s.db.AuditEvents(core.WithSystemContext(ctx), criteria)
}

// CountIdentities returns the number of identities — the guard a first-run seed
// uses to decide whether to create the superuser.
func (s *Store) CountIdentities(ctx context.Context) (int64, error) {
	var n int64
	err := s.sys(ctx).Model(&Identity{}).Count(&n).Error
	return n, err
}

// IdentityByEmail resolves a global identity by email, preloading its system
// roles and its memberships (each with its tenant roles) so login can return the
// membership list and mint either token tier without a second round trip. The
// caller passes an already-normalized (lower-cased) email.
func (s *Store) IdentityByEmail(ctx context.Context, email string) (*Identity, error) {
	var id Identity
	err := s.sys(ctx).
		Preload("SystemRoles").
		Preload("Memberships.TenantRoles").
		Where("email = ?", email).
		First(&id).Error
	if err != nil {
		return nil, err
	}
	return &id, nil
}

// ListIdentities returns every identity, preloading system roles and memberships
// (each with its tenant roles) so the admin console can render the full directory
// in one call. Ordered by email for a stable listing.
func (s *Store) ListIdentities(ctx context.Context) ([]Identity, error) {
	var ids []Identity
	err := s.sys(ctx).
		Preload("SystemRoles").
		Preload("Memberships.TenantRoles").
		Order("email").
		Find(&ids).Error
	return ids, err
}

// ListTenants returns every control-plane tenant row (ADR-033), ordered by token,
// each with its tier preloaded (ADR-065) — the tier is a required FK, so a tenant
// read without it is incomplete by construction.
func (s *Store) ListTenants(ctx context.Context) ([]Tenant, error) {
	var tenants []Tenant
	err := s.sys(ctx).Preload("Tier").Order("token").Find(&tenants).Error
	return tenants, err
}

// CreateIdentity inserts an identity (with any associated roles/memberships set
// on the struct).
func (s *Store) CreateIdentity(ctx context.Context, id *Identity) error {
	return s.sys(ctx).Create(id).Error
}

// RolesByScopeTokens resolves a set of role tokens within a single scope, erroring
// if any token is unknown — so an admin assignment that names a non-existent (or
// wrong-scope) role fails closed rather than silently granting nothing. Duplicate
// input tokens are de-duplicated.
func (s *Store) RolesByScopeTokens(ctx context.Context, scope RoleScope, tokens []string) ([]Role, error) {
	want := make(map[string]struct{}, len(tokens))
	uniq := make([]string, 0, len(tokens))
	for _, t := range tokens {
		if _, ok := want[t]; ok {
			continue
		}
		want[t] = struct{}{}
		uniq = append(uniq, t)
	}
	if len(uniq) == 0 {
		return nil, nil
	}
	var roles []Role
	if err := s.sys(ctx).Where("scope = ? AND token IN ?", scope, uniq).Find(&roles).Error; err != nil {
		return nil, err
	}
	if len(roles) != len(uniq) {
		found := make(map[string]struct{}, len(roles))
		for _, r := range roles {
			found[r.Token] = struct{}{}
		}
		for _, t := range uniq {
			if _, ok := found[t]; !ok {
				return nil, fmt.Errorf("no %s role with token %q", scope, t)
			}
		}
	}
	return roles, nil
}

// SetIdentityEnabled flips an identity's enabled flag in place.
func (s *Store) SetIdentityEnabled(ctx context.Context, id *Identity, enabled bool) error {
	return s.sys(ctx).Model(id).Update("enabled", enabled).Error
}

// SetPasswordHash replaces an identity's bcrypt hash.
func (s *Store) SetPasswordHash(ctx context.Context, id *Identity, hash string) error {
	return s.sys(ctx).Model(id).Update("password_hash", hash).Error
}

// ReplaceSystemRoles replaces an identity's system-role assignments with roles.
func (s *Store) ReplaceSystemRoles(ctx context.Context, id *Identity, roles []Role) error {
	return s.sys(ctx).Model(id).Association("SystemRoles").Replace(roles)
}

// DeleteIdentity removes an identity and its dependent rows in one transaction:
// the system-role join, every membership's tenant-role join, and the memberships
// themselves — so no orphaned join rows survive (intra-iam cascade, ADR-033). The
// caller passes an identity loaded with its Memberships preloaded.
//
// Deletes are Unscoped (hard): the iam entities embed gorm.Model, so a default
// delete would only set deleted_at and leave the row occupying the unique email
// (and identity+tenant) index, blocking re-creation of the same email/membership.
// An admin "delete" frees the slot immediately; the audit trail lives in the
// separate auth-event log, not in tombstoned identity rows.
func (s *Store) DeleteIdentity(ctx context.Context, id *Identity) error {
	return s.sys(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(id).Association("SystemRoles").Clear(); err != nil {
			return err
		}
		for i := range id.Memberships {
			if err := tx.Model(&id.Memberships[i]).Association("TenantRoles").Clear(); err != nil {
				return err
			}
		}
		if err := tx.Unscoped().Where("identity_id = ?", id.ID).Delete(&Membership{}).Error; err != nil {
			return err
		}
		return tx.Unscoped().Delete(id).Error
	})
}

// TenantByToken resolves a control-plane tenant by its token (with its ADR-065
// tier preloaded), or returns gorm.ErrRecordNotFound.
func (s *Store) TenantByToken(ctx context.Context, token string) (*Tenant, error) {
	var t Tenant
	if err := s.sys(ctx).Preload("Tier").Where("token = ?", token).First(&t).Error; err != nil {
		return nil, err
	}
	return &t, nil
}

// MembershipByIdentityTenant resolves an identity's membership in a tenant (with
// its tenant roles preloaded), or returns gorm.ErrRecordNotFound.
func (s *Store) MembershipByIdentityTenant(ctx context.Context, identityID uint, tenant string) (*Membership, error) {
	var m Membership
	if err := s.sys(ctx).Preload("TenantRoles").
		Where("identity_id = ? AND tenant_id = ?", identityID, tenant).First(&m).Error; err != nil {
		return nil, err
	}
	return &m, nil
}

// ReplaceMembershipRoles replaces a membership's tenant-role assignments.
func (s *Store) ReplaceMembershipRoles(ctx context.Context, mem *Membership, roles []Role) error {
	return s.sys(ctx).Model(mem).Association("TenantRoles").Replace(roles)
}

// SetMembershipEnabled flips a membership's enabled flag in place.
func (s *Store) SetMembershipEnabled(ctx context.Context, mem *Membership, enabled bool) error {
	return s.sys(ctx).Model(mem).Update("enabled", enabled).Error
}

// RemoveMembership deletes a membership and its tenant-role join rows in one
// transaction (intra-iam cascade). Unscoped (hard) delete so the identity can be
// re-added to the same tenant without colliding on the soft-deleted row's
// (identity, tenant) unique index — see DeleteIdentity.
func (s *Store) RemoveMembership(ctx context.Context, mem *Membership) error {
	return s.sys(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(mem).Association("TenantRoles").Clear(); err != nil {
			return err
		}
		return tx.Unscoped().Delete(mem).Error
	})
}

// UpsertRole returns the role for (scope, token), creating it from r if absent.
// On a hit r is overwritten with the stored row; on a miss r is inserted as-is.
func (s *Store) UpsertRole(ctx context.Context, r *Role) error {
	return s.sys(ctx).Where(Role{Scope: r.Scope, Token: r.Token}).FirstOrCreate(r).Error
}

// RoleByScopeToken resolves a single role by its (scope, token) identity.
func (s *Store) RoleByScopeToken(ctx context.Context, scope RoleScope, token string) (*Role, error) {
	var r Role
	err := s.sys(ctx).Where("scope = ? AND token = ?", scope, token).First(&r).Error
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// ListRoles returns the role catalog, optionally filtered to a single scope,
// ordered by scope then token for a stable listing.
func (s *Store) ListRoles(ctx context.Context, scope *RoleScope) ([]Role, error) {
	q := s.sys(ctx).Order("scope").Order("token")
	if scope != nil {
		q = q.Where("scope = ?", *scope)
	}
	var roles []Role
	err := q.Find(&roles).Error
	return roles, err
}

// CreateRole inserts a new role; a duplicate (scope, token) violates the unique
// index and surfaces as an error.
func (s *Store) CreateRole(ctx context.Context, r *Role) error {
	return s.sys(ctx).Create(r).Error
}

// UpdateRole persists the mutable fields of an already-loaded role (name,
// description, authorities). Save writes by primary key.
func (s *Store) UpdateRole(ctx context.Context, r *Role) error {
	return s.sys(ctx).Save(r).Error
}

// DeleteRole clears the role's assignment join rows (from both the identity
// system-role and membership tenant-role join tables) and hard-deletes it, so a
// deleted role leaves no dangling assignment. The join-table names match the
// many2many tags on Identity.SystemRoles / Membership.TenantRoles; like every
// other unqualified table reference here they resolve via the connection's
// schema search path.
func (s *Store) DeleteRole(ctx context.Context, r *Role) error {
	return s.sys(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("DELETE FROM iam_identity_system_roles WHERE role_id = ?", r.ID).Error; err != nil {
			return err
		}
		if err := tx.Exec("DELETE FROM iam_membership_tenant_roles WHERE role_id = ?", r.ID).Error; err != nil {
			return err
		}
		return tx.Unscoped().Delete(r).Error
	})
}

// CreateTenant inserts a new control-plane tenant; a duplicate token violates the
// unique index.
func (s *Store) CreateTenant(ctx context.Context, t *Tenant) error {
	return s.sys(ctx).Create(t).Error
}

// UpdateTenant persists the ADMIN-mutable fields of an already-loaded tenant (name,
// config, governance overrides). It Selects exactly those columns rather than a
// full-row Save: the self-service branding_* columns (ADR-038/058) must NOT be
// rewritten from the value loaded here — a concurrent logo upload could have moved
// branding_logo (and GC'd the old blob), so a stale full-row write would point it at
// a just-deleted object and orphan the new one. Select preserves the JSON serializer
// on Config and writes a nil governance pointer as NULL (clear-to-inherit).
//
// AiExternalEnabled (ADR-056 §6) is in the Select list so an operator can both
// grant AND revoke consent on update: an explicit Select writes the column even at
// its zero value (false), and a nil pointer writes NULL — so a revoked consent
// actually clears rather than leaving a stale `true` that would keep routing the
// tenant's data to an external model (a fail-OPEN bug this list guards against).
//
// TierID (ADR-065) is in the Select list because a tenant's tier may change live
// (decision 14: settings-only, no flush, no drain — nothing durable is keyed on the
// tier, so it converges on core/governance's existing 60s TTL). Its NOT NULL FK is
// what keeps a bad write loud: the caller resolves a tier TOKEN to this id, so an
// omitted tier would land as 0 and violate the constraint rather than silently
// re-tiering the tenant.
//
// EVERY admin-mutable governance column must appear here. One omitted from the list
// is silently unwritable: the update appears to succeed while the old value
// survives, which for a limit means a tightened ceiling never takes effect.
func (s *Store) UpdateTenant(ctx context.Context, t *Tenant) error {
	return s.sys(ctx).Model(t).
		Select("Name", "Config", "TierID", "IngestMessagesPerSecond", "IngestBurst",
			"OutboundMessagesPerSecond", "OutboundBurst", "AiExternalEnabled",
			"AiInferenceRequestsPerMinute", "AiInferenceBurst").
		Updates(t).Error
}

// UpdateTenantFields writes only the named columns of an already-loaded tenant,
// leaving every other column untouched. Passed a map, GORM writes each key even
// when its value is nil (a typed nil pointer sets the column to NULL) — unlike a
// struct update, which skips zero values — so this is how a nullable-override
// column (e.g. a branding_* field, ADR-038) is set OR cleared-to-inherit. A no-op
// for an empty map. Mirrors UpdateIdentityFields.
func (s *Store) UpdateTenantFields(ctx context.Context, t *Tenant, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	return s.sys(ctx).Model(t).Updates(fields).Error
}

// SetTenantEnabled flips a tenant's enabled flag in place.
func (s *Store) SetTenantEnabled(ctx context.Context, t *Tenant, enabled bool) error {
	return s.sys(ctx).Model(t).Update("enabled", enabled).Error
}

// DeleteTenant hard-deletes a tenant row. The caller is responsible for ensuring
// no memberships still reference it (see CountMembershipsInTenant).
func (s *Store) DeleteTenant(ctx context.Context, t *Tenant) error {
	return s.sys(ctx).Unscoped().Delete(t).Error
}

// ListTenantTiers returns the tier catalog (ADR-065), ordered by token for a
// stable listing.
func (s *Store) ListTenantTiers(ctx context.Context) ([]TenantTier, error) {
	var tiers []TenantTier
	err := s.sys(ctx).Order("token").Find(&tiers).Error
	return tiers, err
}

// TenantTierByToken resolves a single tier by its token, or returns
// gorm.ErrRecordNotFound.
func (s *Store) TenantTierByToken(ctx context.Context, token string) (*TenantTier, error) {
	var t TenantTier
	if err := s.sys(ctx).Where("token = ?", token).First(&t).Error; err != nil {
		return nil, err
	}
	return &t, nil
}

// CreateTenantTier inserts a new tier; a duplicate token violates the unique index
// and surfaces as an error.
func (s *Store) CreateTenantTier(ctx context.Context, t *TenantTier) error {
	return s.sys(ctx).Create(t).Error
}

// UpdateTenantTier persists the mutable fields of an already-loaded tier (name,
// description, config) by primary key. Save writes every column, so the caller
// loads-then-mutates to avoid clobbering the token.
func (s *Store) UpdateTenantTier(ctx context.Context, t *TenantTier) error {
	return s.sys(ctx).Save(t).Error
}

// DeleteTenantTier hard-deletes a tier row. The caller must first establish that no
// tenant still references it (see CountTenantsAtTier); the FK is RESTRICT, so a
// missed check fails loudly rather than orphaning tenants — but it fails as a raw
// constraint violation, which is why the caller checks.
//
// Unscoped (hard), like every other iam delete: these entities embed gorm.Model, so
// a default delete would only set deleted_at and leave the row occupying the unique
// token index, blocking re-creation of a tier with the same name.
func (s *Store) DeleteTenantTier(ctx context.Context, t *TenantTier) error {
	return s.sys(ctx).Unscoped().Delete(t).Error
}

// CountTenantsAtTier returns how many tenants are packaged at a tier — the guard
// the admin uses before deleting one (ADR-065 decision 9 / the ADR-044
// ErrEntityInUse pattern).
func (s *Store) CountTenantsAtTier(ctx context.Context, tierID uint) (int64, error) {
	var n int64
	err := s.sys(ctx).Model(&Tenant{}).Where("tier_id = ?", tierID).Count(&n).Error
	return n, err
}

// ListOAuthClients returns the OAuth client registry, ordered by client_id for a
// stable listing (ADR-047).
func (s *Store) ListOAuthClients(ctx context.Context) ([]OAuthClient, error) {
	var clients []OAuthClient
	err := s.sys(ctx).Order("client_id").Find(&clients).Error
	return clients, err
}

// OAuthClientByClientId resolves a single registered client by its client_id.
func (s *Store) OAuthClientByClientId(ctx context.Context, clientId string) (*OAuthClient, error) {
	var c OAuthClient
	if err := s.sys(ctx).Where("client_id = ?", clientId).First(&c).Error; err != nil {
		return nil, err
	}
	return &c, nil
}

// CreateOAuthClient inserts a new client; a duplicate client_id violates the
// unique index and surfaces as an error.
func (s *Store) CreateOAuthClient(ctx context.Context, c *OAuthClient) error {
	return s.sys(ctx).Create(c).Error
}

// UpdateOAuthClientProvisioned re-syncs the CONFIG-MANAGED fields of an
// already-loaded seeded client (redirect URIs, scopes, secret hash) by primary key.
// It deliberately does NOT touch `enabled`: enable/disable is an operational lever
// that must survive a restart, so an admin who disables a compromised seeded client
// is not overridden on the next boot. name/description are also left untouched
// (seed clients do not set them). The caller only invokes this when a field actually
// drifted, so a steady-state boot writes nothing.
func (s *Store) UpdateOAuthClientProvisioned(ctx context.Context, c *OAuthClient) error {
	return s.sys(ctx).Model(c).Select("redirect_uris", "scopes", "secret_hash").Updates(c).Error
}

// UpdateOAuthClient persists the mutable fields of an already-loaded client
// (name/description, redirect URIs, scopes) by primary key. Save writes every
// column, so the caller loads-then-mutates to avoid clobbering client_id.
func (s *Store) UpdateOAuthClient(ctx context.Context, c *OAuthClient) error {
	return s.sys(ctx).Save(c).Error
}

// SetOAuthClientEnabled flips a client's enabled flag in place.
func (s *Store) SetOAuthClientEnabled(ctx context.Context, c *OAuthClient, enabled bool) error {
	return s.sys(ctx).Model(c).Update("enabled", enabled).Error
}

// SetOAuthClientSecretHash writes a client's bcrypt secret hash in place (a
// single-column update, like SetOAuthClientEnabled) — used to mint or rotate a
// confidential client's secret without rewriting its other columns. An empty hash
// reverts the client to public.
func (s *Store) SetOAuthClientSecretHash(ctx context.Context, c *OAuthClient, hash string) error {
	return s.sys(ctx).Model(c).Update("secret_hash", hash).Error
}

// DeleteOAuthClient hard-deletes a client row.
func (s *Store) DeleteOAuthClient(ctx context.Context, c *OAuthClient) error {
	return s.sys(ctx).Unscoped().Delete(c).Error
}

// CountMembershipsInTenant returns how many memberships reference a tenant — the
// guard the admin uses before deleting a tenant so it cannot orphan a person's
// access.
func (s *Store) CountMembershipsInTenant(ctx context.Context, tenant string) (int64, error) {
	var n int64
	err := s.sys(ctx).Model(&Membership{}).Where("tenant_id = ?", tenant).Count(&n).Error
	return n, err
}

// AddMembership binds an identity to a tenant with the given (already-resolved)
// tenant roles.
func (s *Store) AddMembership(ctx context.Context, identityID uint, tenantID string, roles []Role) (*Membership, error) {
	mem := &Membership{IdentityID: identityID, TenantId: tenantID, Enabled: true, TenantRoles: roles}
	if err := s.sys(ctx).Create(mem).Error; err != nil {
		return nil, err
	}
	return mem, nil
}

// AssignSystemRoles appends system roles to an identity (idempotent on the join).
func (s *Store) AssignSystemRoles(ctx context.Context, id *Identity, roles []Role) error {
	return s.sys(ctx).Model(id).Association("SystemRoles").Append(roles)
}

// UpdateIdentityFields persists the given profile columns of an already-loaded
// identity by primary key. A column map is used (not a struct) so an explicit
// empty string is written, and only the supplied columns are touched — the
// email, password, and role associations are never affected. A no-op for an
// empty map.
func (s *Store) UpdateIdentityFields(ctx context.Context, id *Identity, fields map[string]any) error {
	if len(fields) == 0 {
		return nil
	}
	return s.sys(ctx).Model(id).Updates(fields).Error
}

// EnsureRole idempotently upserts a well-known role by (scope, token), keeping
// its name and authorities current. Safe to call on every startup — used to keep
// the built-in `viewer` role in the catalog and in sync with the code.
func (s *Store) EnsureRole(ctx context.Context, scope RoleScope, token, name string, authorities []string) error {
	role := Role{Scope: scope, Token: token}
	return s.sys(ctx).
		Where(Role{Scope: scope, Token: token}).
		Assign(Role{NamedEntity: rdb.NamedEntity{Name: rdb.NullStrOf(&name)}, Authorities: authorities}).
		FirstOrCreate(&role).Error
}

// SeedSuperuser creates, in one transaction, the superuser identity with the
// `superuser` system role (ADR-033). The bootstrap is tenant-less: no scaffold
// tenant or membership is created. A convenience `tenant-admin` tenant role is
// still seeded into the catalog so the admin has a full-authority role to assign
// when it creates the first tenant. The two authority sets are passed in so this
// package stays decoupled from the authority vocabulary (the caller passes the
// super-authority for both). Doing it transactionally avoids a half-seeded,
// locked-out superuser.
func (s *Store) SeedSuperuser(ctx context.Context, email, passwordHash string, systemAuthorities, tenantAdminAuthorities []string) error {
	suName, adminName := "Superuser", "Tenant Administrator"
	return s.sys(ctx).Transaction(func(tx *gorm.DB) error {
		suRole := Role{Scope: ScopeSystem, Token: SuperuserRoleToken,
			NamedEntity: rdb.NamedEntity{Name: rdb.NullStrOf(&suName)}, Authorities: systemAuthorities}
		if err := tx.Where(Role{Scope: ScopeSystem, Token: SuperuserRoleToken}).FirstOrCreate(&suRole).Error; err != nil {
			return err
		}
		adminRole := Role{Scope: ScopeTenant, Token: TenantAdminRoleToken,
			NamedEntity: rdb.NamedEntity{Name: rdb.NullStrOf(&adminName)}, Authorities: tenantAdminAuthorities}
		if err := tx.Where(Role{Scope: ScopeTenant, Token: TenantAdminRoleToken}).FirstOrCreate(&adminRole).Error; err != nil {
			return err
		}

		id := Identity{Email: email, Enabled: true, PasswordHash: passwordHash}
		if err := tx.Create(&id).Error; err != nil {
			return err
		}
		return tx.Model(&id).Association("SystemRoles").Append(&suRole)
	})
}
