// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package iam

import (
	"context"

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

// CreateIdentity inserts an identity (with any associated roles/memberships set
// on the struct).
func (s *Store) CreateIdentity(ctx context.Context, id *Identity) error {
	return s.sys(ctx).Create(id).Error
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
