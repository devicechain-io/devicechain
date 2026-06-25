// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

package identity

import (
	"context"
	"fmt"
	"sort"

	"github.com/devicechain-io/dc-microservice/auth"
	"github.com/devicechain-io/dc-microservice/core"
	"github.com/devicechain-io/dc-microservice/rdb"
	"github.com/devicechain-io/dc-user-management/model"
	"golang.org/x/crypto/bcrypt"
)

// This file holds the user + role directory (ADR-008 RBAC): the tenant-scoped
// management of roles, users, and their assignment. It runs on authenticated
// requests, so all access is tenant-scoped via the request context (unlike the
// login lookup, which must run system-context before a tenant is known).

// validateAuthorities rejects a role definition that grants an authority outside
// the known vocabulary (core/auth), so a typo fails at write time rather than
// silently granting nothing.
func validateAuthorities(authorities []string) error {
	for _, a := range authorities {
		if !auth.ValidAuthority(a) {
			return fmt.Errorf("unknown authority %q", a)
		}
	}
	return nil
}

// CreateRole creates a tenant-scoped role granting the requested authorities.
func (m *Manager) CreateRole(ctx context.Context, request *model.RoleCreateRequest) (*model.Role, error) {
	if err := validateAuthorities(request.Authorities); err != nil {
		return nil, err
	}
	role := &model.Role{
		TokenReference: rdb.TokenReference{Token: request.Token},
		NamedEntity: rdb.NamedEntity{
			Name:        rdb.NullStrOf(request.Name),
			Description: rdb.NullStrOf(request.Description),
		},
		Authorities: request.Authorities,
	}
	if err := m.db.DB(ctx).Create(role).Error; err != nil {
		return nil, err
	}
	return role, nil
}

// UpdateRole replaces a role's name/description/authorities by token.
func (m *Manager) UpdateRole(ctx context.Context, token string, request *model.RoleCreateRequest) (*model.Role, error) {
	if err := validateAuthorities(request.Authorities); err != nil {
		return nil, err
	}
	var role model.Role
	if err := m.db.DB(ctx).Where("token = ?", token).First(&role).Error; err != nil {
		return nil, err
	}
	role.Token = request.Token
	role.Name = rdb.NullStrOf(request.Name)
	role.Description = rdb.NullStrOf(request.Description)
	role.Authorities = request.Authorities
	if err := m.db.DB(ctx).Save(&role).Error; err != nil {
		return nil, err
	}
	return &role, nil
}

// DeleteRole removes a role by token. Returns false (no error) when no such role
// exists, so the call is idempotent.
func (m *Manager) DeleteRole(ctx context.Context, token string) (bool, error) {
	result := m.db.DB(ctx).Where("token = ?", token).Delete(&model.Role{})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

// RolesByToken loads roles by token.
func (m *Manager) RolesByToken(ctx context.Context, tokens []string) ([]*model.Role, error) {
	found := make([]*model.Role, 0)
	if err := m.db.DB(ctx).Where("token in ?", tokens).Find(&found).Error; err != nil {
		return nil, err
	}
	return found, nil
}

// Roles lists all roles in the tenant.
func (m *Manager) Roles(ctx context.Context) ([]*model.Role, error) {
	found := make([]*model.Role, 0)
	if err := m.db.DB(ctx).Find(&found).Error; err != nil {
		return nil, err
	}
	return found, nil
}

// CreateUser creates a tenant-scoped user with a bcrypt-hashed password. The
// tenant is stamped from the request context by the tenant-scope callback.
func (m *Manager) CreateUser(ctx context.Context, request *model.UserCreateRequest) (*model.User, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(request.Password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	user := &model.User{
		Username:     request.Username,
		Email:        strOf(request.Email),
		FirstName:    strOf(request.FirstName),
		LastName:     strOf(request.LastName),
		Enabled:      request.Enabled,
		PasswordHash: string(hash),
	}
	if err := m.db.DB(ctx).Create(user).Error; err != nil {
		return nil, err
	}
	return user, nil
}

// Users lists all users in the tenant with their assigned roles preloaded.
func (m *Manager) Users(ctx context.Context) ([]*model.User, error) {
	found := make([]*model.User, 0)
	if err := m.db.DB(ctx).Preload("Roles").Find(&found).Error; err != nil {
		return nil, err
	}
	return found, nil
}

// UserByUsername loads a single user (with roles) by username within the tenant.
func (m *Manager) UserByUsername(ctx context.Context, username string) (*model.User, error) {
	var user model.User
	if err := m.db.DB(ctx).Preload("Roles").Where("username = ?", username).First(&user).Error; err != nil {
		return nil, err
	}
	return &user, nil
}

// SetUserRoles replaces a user's role assignments with the roles named by
// roleTokens (within the tenant). It returns the updated user with roles loaded.
func (m *Manager) SetUserRoles(ctx context.Context, username string, roleTokens []string) (*model.User, error) {
	user, err := m.UserByUsername(ctx, username)
	if err != nil {
		return nil, err
	}
	roles := make([]model.Role, 0)
	if len(roleTokens) > 0 {
		if err := m.db.DB(ctx).Where("token in ?", roleTokens).Find(&roles).Error; err != nil {
			return nil, err
		}
		if len(roles) != len(roleTokens) {
			return nil, fmt.Errorf("one or more roles not found: %v", roleTokens)
		}
	}
	if err := m.db.DB(ctx).Model(user).Association("Roles").Replace(roles); err != nil {
		return nil, err
	}
	return m.UserByUsername(ctx, username)
}

// effectiveAuthorities loads a user's assigned roles and returns their role
// tokens plus the deduplicated union of every authority those roles grant. Both
// are sorted for determinism. It scopes to the user's own tenant (issuance can be
// reached from the system-context login path, where ctx carries no tenant yet).
func (m *Manager) effectiveAuthorities(ctx context.Context, user *model.User) (roleTokens []string, authorities []string, err error) {
	tctx := core.WithTenant(ctx, user.TenantId)
	var roles []model.Role
	if err := m.db.DB(tctx).Model(user).Association("Roles").Find(&roles); err != nil {
		return nil, nil, err
	}
	authSet := map[string]struct{}{}
	for _, r := range roles {
		roleTokens = append(roleTokens, r.Token)
		for _, a := range r.Authorities {
			authSet[a] = struct{}{}
		}
	}
	for a := range authSet {
		authorities = append(authorities, a)
	}
	sort.Strings(roleTokens)
	sort.Strings(authorities)
	return roleTokens, authorities, nil
}

// strOf dereferences an optional string to its value or "".
func strOf(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
