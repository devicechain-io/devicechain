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

// ListTenants returns every control-plane tenant row (ADR-033), ordered by token.
func (s *Store) ListTenants(ctx context.Context) ([]Tenant, error) {
	var tenants []Tenant
	err := s.sys(ctx).Order("token").Find(&tenants).Error
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

// TenantByToken resolves a control-plane tenant by its token, or returns
// gorm.ErrRecordNotFound.
func (s *Store) TenantByToken(ctx context.Context, token string) (*Tenant, error) {
	var t Tenant
	if err := s.sys(ctx).Where("token = ?", token).First(&t).Error; err != nil {
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

// SeedSuperuser creates, in one transaction, the superuser identity with the
// `superuser` system role and a scaffold membership in `tenant` carrying the
// `tenant-admin` role. The two authority sets are passed in so this package stays
// decoupled from the authority vocabulary (the caller passes the super-authority
// for both). Doing it transactionally avoids a half-seeded, locked-out superuser.
func (s *Store) SeedSuperuser(ctx context.Context, email, passwordHash, tenant string, systemAuthorities, tenantAdminAuthorities []string) error {
	suName, adminName := "Superuser", "Tenant Administrator"
	return s.sys(ctx).Transaction(func(tx *gorm.DB) error {
		// The scaffold tenant as a control-plane row (ADR-033).
		tName := "Default"
		ten := Tenant{Token: tenant, Enabled: true,
			NamedEntity: rdb.NamedEntity{Name: rdb.NullStrOf(&tName)}}
		if err := tx.Where(Tenant{Token: tenant}).FirstOrCreate(&ten).Error; err != nil {
			return err
		}

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
		if err := tx.Model(&id).Association("SystemRoles").Append(&suRole); err != nil {
			return err
		}
		mem := Membership{IdentityID: id.ID, TenantId: tenant, Enabled: true}
		if err := tx.Create(&mem).Error; err != nil {
			return err
		}
		return tx.Model(&mem).Association("TenantRoles").Append(&adminRole)
	})
}
